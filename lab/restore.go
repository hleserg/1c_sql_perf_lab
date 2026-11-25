package lab

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Serialize a restore with Perf Lab runs in this database, including when it is offline.
func labLock(ctx context.Context, c *sql.Conn, database, mode string) error {
	if err := instanceLock(ctx, c, "Shared"); err != nil {
		return err
	}
	var n int
	e := c.QueryRowContext(ctx, `DECLARE @r int,@key nvarchar(255)=N'1c_sql_perf_lab:'+UPPER(@p1); EXEC @r=master.sys.sp_getapplock @Resource=@key,@LockMode=@p2,@LockOwner='Session',@LockTimeout=0; SELECT @r`, database, mode).Scan(&n)
	if e != nil {
		return e
	}
	if n < 0 {
		return errors.New("на этой базе уже выполняется работа Perf Lab")
	}
	return nil
}
func runRestore(ctx context.Context, r Run, rep *Report, p RestorePlan) error {
	rep.Job = RestoreReportTitle
	rep.Inventory = map[string]string{"SQL": p.Server, "База": p.Target, "Исходная база": p.Source, "Файл бэкапа": p.Path, "Окончание копирования": p.Finished}
	log := &restoreLog{emit: func(s string, p RestoreProgress) {
		rep.Restore = &p
		rep.Journal = append(rep.Journal, s)
		r.say(Event{Phase: rep.Job, Detail: s, Restore: &p})
	}}
	log.stage(RestorePrepare, "Подготовка восстановления")
	if p.Demo != r.Demo {
		return errors.New("режим восстановления изменился, выбери копию заново")
	}
	if _, e := os.Stat(r.pending()); e == nil && !r.Demo {
		return errors.New("сначала верни настройки по журналу предыдущего эксперимента")
	} else if e != nil && !os.IsNotExist(e) {
		return e
	}
	if !r.Demo {
		if err := r.checkInstanceJournals(); err != nil {
			return err
		}
	}
	if r.Demo {
		for _, step := range []struct {
			stage RestoreStage
			text  string
		}{
			{RestoreVerify, "Демо: проверка выбранного набора"},
			{RestoreExclusive, "Демо: отключение сеансов копии"},
			{RestoreWrite, "Демо: восстановление файлов"},
			{RestoreAccess, "Демо: проверка доступа"},
		} {
			if step.stage == RestoreExclusive {
				log.p.Verify.Complete = true
			}
			if step.stage == RestoreAccess {
				log.p.Write.Complete = true
			}
			log.stage(step.stage, step.text)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
		}
		log.p.State = "ONLINE / MULTI_USER"
		log.stage(RestoreReady, "Демо: ONLINE / MULTI_USER")
	} else {
		l, e := openRestore(ctx, r.Profile)
		if e != nil {
			return e
		}
		defer l.Close()
		var login string
		if e = l.conn.QueryRowContext(ctx, "SELECT ORIGINAL_LOGIN()").Scan(&login); e != nil {
			return fmt.Errorf("проверка SQL-входа: %w", e)
		}
		log.note("Подключение к " + p.Server + " · SQL-вход " + login)
		if e = applyRestore(ctx, l, r.Profile, p, log); e != nil {
			return fmt.Errorf("%s, SQL-вход %s: %w", p.Server, login, e)
		}
	}
	section := Section{ID: 15, Title: "Копия восстановлена", Columns: []string{"База стенда", "Состояние", "Доступ", "Файл", "Набор"}, Rows: [][]string{{p.Target, "ONLINE", "MULTI_USER", p.Path, strconv.Itoa(p.Position)}}}
	rep.Sections = append(rep.Sections, section)
	r.say(Event{Phase: rep.Job, Detail: section.Title, Section: &section})
	return nil
}

// Use a second connection to read progress without disturbing the session
// that owns the restore. SQL permissions can restrict the progress counter.
func restoreCommand(ctx context.Context, l *Live, spid int, query, path string, log *restoreLog) error {
	done := make(chan error, 1)
	go func() { _, e := l.conn.ExecContext(ctx, query, path); done <- e }()
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()
	last := -1
	for {
		select {
		case e := <-done:
			return e
		case <-ticker.C:
			probe, cancel := context.WithTimeout(ctx, time.Second)
			var pct int
			e := l.db.QueryRowContext(probe, "SELECT CONVERT(int,percent_complete) FROM sys.dm_exec_requests WHERE session_id=@p1 AND command LIKE 'RESTORE%'", spid).Scan(&pct)
			cancel()
			if e == nil && pct != last {
				log.sample(pct)
				last = pct
			}
		}
	}
}
func applyRestore(ctx context.Context, l *Live, p Profile, chosen RestorePlan, log *restoreLog) error {
	c := l.conn
	if e := labLock(ctx, c, p.Database, "Exclusive"); e != nil {
		return e
	}
	compare := func() error {
		fresh, e := prepareRestore(ctx, c, p, chosen.Path)
		if e != nil {
			return e
		}
		if !samePlan(fresh, chosen) {
			return errors.New("источник или база назначения изменились с момента выбора. Выбери файл заново")
		}
		return nil
	}
	if e := compare(); e != nil {
		return e
	}
	options := fmt.Sprintf("FILE=%d", chosen.Position)
	if chosen.Checksums {
		options += ", CHECKSUM"
	}
	var spid int
	if e := c.QueryRowContext(ctx, "SELECT @@SPID").Scan(&spid); e != nil {
		return e
	}
	log.stage(RestoreVerify, "Проверка носителя RESTORE VERIFYONLY · "+chosen.Path)
	if e := restoreCommand(ctx, l, spid, "RESTORE VERIFYONLY FROM DISK=@p1 WITH "+options, chosen.Path, log); e != nil {
		return fmt.Errorf("проверка копии до отключения сеансов: %w", e)
	}
	log.p.Verify.Complete = true
	log.stage(RestoreRecheck, "Повторная проверка источника и назначения")
	if e := compare(); e != nil {
		return e
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	// From this point complete restoration even if Esc is pressed. The connection
	// remains in master, and OFFLINE prevents reconnecting 1C clients from racing RESTORE.
	work := context.WithoutCancel(ctx)
	target := quotedID(chosen.Target)
	var state string
	if chosen.TargetID != 0 {
		if e := c.QueryRowContext(work, "SELECT state_desc FROM sys.databases WHERE database_id=@p1 AND name=@p2", chosen.TargetID, chosen.Target).Scan(&state); e != nil {
			return e
		}
		if state == "ONLINE" {
			log.stage(RestoreExclusive, "Отключение сеансов базы "+chosen.Target+" с откатом незавершённых транзакций")
			if _, e := c.ExecContext(work, "ALTER DATABASE "+target+" SET OFFLINE WITH ROLLBACK IMMEDIATE"); e != nil {
				return fmt.Errorf("не удалось получить монопольный доступ: %w", e)
			}
		} else if state != "OFFLINE" && state != "RESTORING" {
			return fmt.Errorf("состояние цели изменилось: %s", state)
		}
		options += ", REPLACE"
	}
	for _, f := range chosen.Files {
		options += ", MOVE " + unicodeLiteral(f.Logical) + " TO " + unicodeLiteral(f.Physical)
	}
	log.stage(RestoreWrite, "Восстановление "+chosen.Target+" · набор "+strconv.Itoa(chosen.Position))
	e := restoreCommand(work, l, spid, "RESTORE DATABASE "+target+" FROM DISK=@p1 WITH "+options+", RECOVERY", chosen.Path, log)
	if e != nil {
		return restoreFailure(l, chosen, state, e, log)
	}
	log.p.Write.Complete = true
	log.stage(RestoreAccess, "Проверка состояния базы и доступа")
	var mode string
	e = c.QueryRowContext(work, "SELECT state_desc,user_access_desc FROM sys.databases WHERE name=@p1", chosen.Target).Scan(&state, &mode)
	if e != nil {
		return e
	}
	if state != "ONLINE" {
		log.p.State = state + " / " + mode
		log.note("Состояние после RESTORE: " + log.p.State)
		return fmt.Errorf("RESTORE завершён, состояние базы %s", state)
	}
	if mode != "MULTI_USER" {
		if _, e = c.ExecContext(work, "ALTER DATABASE "+target+" SET MULTI_USER WITH ROLLBACK IMMEDIATE"); e != nil {
			return fmt.Errorf("база восстановлена, не удалось вернуть MULTI_USER: %w", e)
		}
	}
	e = c.QueryRowContext(work, "SELECT state_desc,user_access_desc FROM sys.databases WHERE name=@p1", chosen.Target).Scan(&state, &mode)
	if e != nil {
		return e
	}
	log.p.State = state + " / " + mode
	if state != "ONLINE" || mode != "MULTI_USER" {
		log.note("Состояние после RESTORE: " + log.p.State)
		return fmt.Errorf("состояние базы после RESTORE: %s / %s", state, mode)
	}
	log.stage(RestoreReady, "SQL подтвердил ONLINE / MULTI_USER")
	return nil
}
func restoreFailure(l *Live, plan RestorePlan, prior string, cause error, log *restoreLog) error {
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	var state string
	var id int
	err := l.conn.QueryRowContext(ctx, "SELECT state_desc,database_id FROM sys.databases WHERE name=@p1", plan.Target).Scan(&state, &id)
	if errors.Is(err, sql.ErrNoRows) {
		log.p.State = "база отсутствует"
		log.note("Состояние после ошибки: " + log.p.State)
		return fmt.Errorf("RESTORE не завершён, база назначения отсутствует: %w", cause)
	}
	if err != nil {
		return errors.Join(cause, fmt.Errorf("не удалось проверить состояние базы: %w", err))
	}
	if id == plan.TargetID && state == "OFFLINE" && prior == "ONLINE" {
		if _, err = l.conn.ExecContext(ctx, "ALTER DATABASE "+quotedID(plan.Target)+" SET ONLINE"); err != nil {
			return errors.Join(cause, fmt.Errorf("ошибка возврата ONLINE: %w", err))
		}
		if err = l.conn.QueryRowContext(ctx, "SELECT state_desc FROM sys.databases WHERE database_id=@p1", id).Scan(&state); err != nil {
			return errors.Join(cause, err)
		}
	}
	log.p.State = state
	log.note("Состояние после ошибки: " + state)
	return fmt.Errorf("RESTORE не завершён, %s · %s: %w", plan.Target, strings.TrimSpace(state), cause)
}
