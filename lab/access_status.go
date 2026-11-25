package lab

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
)

// AccessStatus uses master even when a restore or an unavailable database
// prevents opening the target. It takes no application or database locks.
func AccessStatus(ctx context.Context, p Profile, login string) (Report, error) {
	l := &Live{P: p}
	defer l.Close()
	if err := l.connect(ctx, "master"); err != nil {
		return Report{}, fmt.Errorf("подключение оператора к master (%s): %s", p.SQLHost, accessError(err))
	}
	return readAccessStatus(ctx, l.conn, p, login)
}

func accessError(err error) string {
	var dbErr mssql.Error
	if errors.As(err, &dbErr) {
		return fmt.Sprintf("SQL %d, state %d: %s", dbErr.Number, dbErr.State, dbErr.Message)
	}
	return err.Error()
}

func readAccessStatus(ctx context.Context, c *sql.Conn, p Profile, login string) (r Report, err error) {
	r = Report{Job: "Доступ к базе после восстановления", Profile: p.Name, Started: time.Now()}
	defer func() {
		r.Finished = time.Now()
		if err != nil {
			r.Error = err.Error()
		}
	}()
	var server, operator string
	var major int
	if err = c.QueryRowContext(ctx, `SELECT CONVERT(nvarchar(128),SERVERPROPERTY('ServerName')),ORIGINAL_LOGIN(),CONVERT(int,SERVERPROPERTY('ProductMajorVersion'))`).Scan(&server, &operator, &major); err != nil {
		return r, err
	}
	s := Section{Title: "Подключение оператора", Columns: []string{"Проверка", "Результат"}, Rows: [][]string{
		{"Экземпляр SQL", server}, {"Ожидается по профилю", p.ExpectedServer}, {"База по профилю", p.Database}, {"Логин оператора", operator},
	}}
	r.Sections = append(r.Sections, s)
	if !strings.EqualFold(server, p.ExpectedServer) {
		return r, fmt.Errorf("подключение ведёт на %s, профиль ожидает %s. Сверь экземпляр в регистрации базы кластера 1С и в профиле", server, p.ExpectedServer)
	}
	var name, state, mode sql.NullString
	var access sql.NullInt64
	err = c.QueryRowContext(ctx, `SELECT d.name,d.state_desc,d.user_access_desc,HAS_DBACCESS(@p1)
 FROM (VALUES(1)) AS seed(n) LEFT JOIN master.sys.databases d ON d.name=@p1`, p.Database).Scan(&name, &state, &mode, &access)
	if err != nil {
		return r, err
	}
	found := name.Valid
	if found {
		s.Rows = append(s.Rows, []string{"Имя базы в SQL", name.String}, []string{"Состояние", state.String}, []string{"Режим доступа", mode.String})
	} else {
		s.Rows = append(s.Rows, []string{"Каталог баз SQL", "База с таким именем отсутствует или скрыта для оператора"})
	}
	hasAccess := "не определено"
	if access.Valid {
		if access.Int64 == 1 {
			hasAccess = "да"
		} else {
			hasAccess = "нет"
		}
	}
	s.Rows = append(s.Rows, []string{"HAS_DBACCESS для оператора", hasAccess})
	open := false
	// A probe must not occupy the only connection to a SINGLE_USER database.
	if found && state.String == "ONLINE" && mode.String == "MULTI_USER" {
		var db, user string
		errOpen := c.QueryRowContext(ctx, "EXEC "+quotedID(name.String)+".sys.sp_executesql N'SELECT DB_NAME(), USER_NAME()'").Scan(&db, &user)
		if errOpen == nil {
			open = true
			s.Rows = append(s.Rows, []string{"Открытие оператором", db + " · пользователь базы " + user})
		} else {
			s.Rows = append(s.Rows, []string{"Ошибка открытия оператором", accessError(errOpen)})
		}
	} else {
		s.Rows = append(s.Rows, []string{"Пробное открытие", "Пропущено до состояния ONLINE и режима MULTI_USER"})
	}
	r.Sections[0] = s
	r.Sections = append(r.Sections, statusRestores(ctx, c, p.Database, major))

	login = strings.TrimSpace(login)
	if login != "" {
		principal := fetchSection(ctx, c, check{0, "Указанный логин 1С на экземпляре SQL", `SELECT name AS [Логин],type_desc AS [Тип],is_disabled AS [Отключён],default_database_name AS [База по умолчанию]
 FROM sys.server_principals WHERE name=@p1 AND type IN ('S','U','G','E','X')`}, login)
		if principal.Error == "" && len(principal.Rows) == 0 {
			principal.Error = fmt.Sprintf("Логин %q не найден среди видимых оператору. Проверь точное имя и доступность метаданных.", login)
		}
		r.Sections = append(r.Sections, principal)
		if open {
			const mapped = `SELECT d.name AS [Пользователь базы],d.type_desc AS [Тип],d.authentication_type_desc AS [Аутентификация],
 CASE WHEN sp.sid IS NULL THEN N'Логин не виден' WHEN d.sid=sp.sid THEN N'SID совпадает' ELSE N'SID отличается' END AS [Сопоставление]
 FROM sys.database_principals AS d LEFT JOIN master.sys.server_principals AS sp ON sp.name=@login
 WHERE d.name=@login OR d.sid=sp.sid`
			mapping := fetchSection(ctx, c, check{0, "Прямая привязка логина в восстановленной базе",
				"EXEC " + quotedID(name.String) + ".sys.sp_executesql " + unicodeLiteral(mapped) + ",N'@login nvarchar(128)',@p1"}, login)
			if mapping.Error == "" && len(mapping.Rows) == 0 {
				mapping.Error = "Прямая привязка не видна. Доступ также может идти через группу Windows или роль sysadmin."
			}
			r.Sections = append(r.Sections, mapping)
		}
	}
	logCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	r.Sections = append(r.Sections, recentLoginFailures(logCtx, c, p.Database, login))
	cancel()

	next := Section{Title: "Что проверить в 1С", Columns: []string{"Шаг", "Что сверить"}}
	next.Rows = append(next.Rows, []string{"Регистрация информационной базы", fmt.Sprintf("В кластере 1С сверь сервер СУБД с %s, имя базы СУБД с %s и логин подключения.", server, p.Database)})
	switch {
	case !found:
		next.Rows = append(next.Rows, []string{"Видимость базы", "Попроси администратора проверить точное имя базы на этом экземпляре и видимость базы для оператора."})
	case state.String != "ONLINE" || mode.String != "MULTI_USER":
		next.Rows = append(next.Rows, []string{"Доступность SQL", "Сверь состояние базы с администратором SQL. Если ещё идёт восстановление, дождись его завершения."})
	case !open:
		next.Rows = append(next.Rows, []string{"Ошибка открытия", "Передай администратору SQL приведённый код и текст ошибки. Проверь SID и CONNECT для учётной записи, которой требуется вход."})
	default:
		next.Rows = append(next.Rows, []string{"Доступ приложения", fmt.Sprintf("Базу открыл оператор %s. Проверь вход из 1С под учётной записью, указанной в регистрации базы.", operator)})
	}
	next.Rows = append(next.Rows, []string{"Повторить один вход", "Запиши время, открой подробности ошибки веб-клиента. Сопоставь запись в ERRORLOG SQL со временем и логином. Если записи нет, проверь в публикации Srvr и Ref и журнал кластера 1С."})
	if login == "" {
		next.Rows = append(next.Rows, []string{"Логин 1С", "Для проверки привязки запусти perf-lab -status -sql-login «имя из регистрации 1С»."})
	}
	r.Sections = append(r.Sections, next)
	return r, ctx.Err()
}

func statusRestores(ctx context.Context, c *sql.Conn, database string, major int) Section {
	permission := "VIEW SERVER STATE"
	if major >= 16 {
		permission = "VIEW SERVER PERFORMANCE STATE"
	}
	var full int
	s := Section{Title: "Видимые RESTORE на экземпляре (контекст сеанса может быть master)"}
	if err := c.QueryRowContext(ctx, "SELECT COALESCE(HAS_PERMS_BY_NAME(NULL,NULL,@p1),0)", permission).Scan(&full); err != nil {
		s.Error = accessError(err)
		return s
	}
	s = fetchSection(ctx, c, check{0, s.Title, `SELECT r.session_id AS [Сеанс],r.command AS [Команда],r.status AS [Состояние],
 CASE WHEN r.database_id=DB_ID(@p1) THEN @p1 WHEN r.database_id=1 THEN N'master' ELSE N'другой контекст' END AS [Контекст запроса],
 CONVERT(decimal(6,2), r.percent_complete) AS [Готово %]
 FROM sys.dm_exec_requests AS r WHERE r.command LIKE N'RESTORE%' ORDER BY r.session_id`}, database)
	if s.Error == "" && full != 1 {
		s.Error = "Видимость ограничена правами оператора. Другие сеансы могут выполнять RESTORE."
	} else if s.Error == "" && len(s.Rows) == 0 {
		s.Columns = []string{"Результат"}
		s.Rows = [][]string{{"На момент проверки активных запросов RESTORE не видно"}}
	}
	return s
}

var failedUserPrefix = regexp.MustCompile(`(?i)^(?:login failed for user|сбой входа для пользователя|ошибка входа (?:для )?пользователя|не удалось выполнить вход для пользователя)\s+`)

var clientAddressSuffix = regexp.MustCompile(`(?i)\s+\[(?:client|клиент):[^\r\n]*\]\s*$`)

// Accept login-failure events only when their reason identifies the exact
// database. Exclude client addresses, SQL text and failures for other logins
// when the caller identified its application account.
func matchingLoginFailure(text, db, filter string) (login, reason string, ok bool) {
	start := failedUserPrefix.FindStringIndex(text)
	if start == nil {
		return "", "", false
	}
	quoted := text[start[1]:]
	var tail string
	for _, quotes := range [][2]string{{"'", "'"}, {`"`, `"`}, {"«", "»"}} {
		if strings.HasPrefix(quoted, quotes[0]) {
			login, tail, ok = strings.Cut(strings.TrimPrefix(quoted, quotes[0]), quotes[1]+".")
			break
		}
	}
	if !ok || (filter != "" && !strings.EqualFold(filter, login)) {
		return "", "", false
	}
	tail = clientAddressSuffix.ReplaceAllString(tail, "")
	lower := strings.ToLower(tail)
	for _, quotes := range [][2]string{{"'", "'"}, {`"`, `"`}, {"«", "»"}} {
		name := quotes[0] + strings.ToLower(db) + quotes[1]
		for _, noun := range []string{"database ", "базу данных ", "базы данных ", "база данных ", "базе данных "} {
			if strings.Contains(lower, noun+name) {
				return login, strings.TrimSpace(tail), true
			}
		}
	}
	return "", "", false
}

func recentLoginFailures(ctx context.Context, c *sql.Conn, db, login string) Section {
	s := Section{Title: "Отказы входа с именем этой базы · текущий ERRORLOG за 24 часа", Columns: []string{"Время сервера SQL", "Логин", "Причина из SQL"}}
	// sp_readerrorlog filters the current file by the database name. Keep the
	// time comparison on the server to use its own local clock.
	rows, err := c.QueryContext(ctx, `DECLARE @events TABLE(t datetime, source nvarchar(64), msg nvarchar(max));
 INSERT INTO @events EXEC master.sys.sp_readerrorlog 0,1,@p1;
 SELECT t,msg FROM @events WHERE t>=DATEADD(hour,-24,GETDATE())
 AND source IN (N'Logon',N'Вход',N'Вход в систему') ORDER BY t DESC`, db)
	if err != nil {
		s.Error = accessError(err)
		return s
	}
	defer rows.Close()
	for len(s.Rows) < 10 && rows.Next() {
		var stamp time.Time
		var message string
		if err = rows.Scan(&stamp, &message); err != nil {
			s.Error = accessError(err)
			return s
		}
		if observed, reason, ok := matchingLoginFailure(message, db, login); ok {
			s.Rows = append(s.Rows, []string{stamp.Format("2006-01-02 15:04:05"), observed, reason})
		}
	}
	if err = rows.Err(); err != nil {
		s.Error = accessError(err)
	} else if len(s.Rows) == 0 {
		s.Columns = []string{"Результат"}
		s.Rows = [][]string{{"Совпадений не найдено. Отказы без имени этой базы и архивные журналы в выборку не входят."}}
	}
	return s
}
