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
	rep.Job = "Восстановление из копии"
	rep.Inventory = map[string]string{"SQL": p.Server, "База": p.Target, "Исходная база": p.Source, "Файл бэкапа": p.Path, "Окончание копирования": p.Finished}
	note := func(s string) { rep.Journal = append(rep.Journal, s); r.say(Event{Phase: rep.Job, Detail: s}) }
	if p.Demo != r.Demo {
		return errors.New("режим восстановления изменился, выбери копию заново")
	}
	if _, e := os.Stat(r.pending()); e == nil && !r.Demo {
		return errors.New("сначала верни настройки по журналу предыдущего эксперимента")
	} else if e != nil && !os.IsNotExist(e) {
		return e
	}
	if r.Demo {
		for _, s := range []string{"Демо: проверка выбранного набора", "Демо: отключение сеансов копии", "Демо: восстановление файлов", "Демо: ONLINE / MULTI_USER"} {
			note(s)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
		}
	} else {
		l, e := openRestore(ctx, r.Profile)
		if e != nil {
			return e
		}
		defer l.Close()
		if e = applyRestore(ctx, l, r.Profile, p, note); e != nil {
			return e
		}
	}
	section := Section{ID: 15, Title: "Копия восстановлена", Columns: []string{"База стенда", "Состояние", "Доступ", "Файл", "Набор"}, Rows: [][]string{{p.Target, "ONLINE", "MULTI_USER", p.Path, strconv.Itoa(p.Position)}}}
	rep.Sections = append(rep.Sections, section)
	r.say(Event{Phase: rep.Job, Detail: section.Title, Section: &section})
	return nil
}
func applyRestore(ctx context.Context, l *Live, p Profile, chosen RestorePlan, note func(string)) error {
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
	note("Проверка носителя RESTORE VERIFYONLY · " + chosen.Path)
	if _, e := c.ExecContext(ctx, "RESTORE VERIFYONLY FROM DISK=@p1 WITH "+options, chosen.Path); e != nil {
		return fmt.Errorf("проверка копии до отключения сеансов: %w", e)
	}
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
			note("Отключение сеансов базы " + chosen.Target + " с откатом незавершённых транзакций")
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
	note("Восстановление " + chosen.Target + " · набор " + strconv.Itoa(chosen.Position))
	_, e := c.ExecContext(work, "RESTORE DATABASE "+target+" FROM DISK=@p1 WITH "+options+", RECOVERY, STATS=5", chosen.Path)
	if e != nil {
		return restoreFailure(l, chosen, state, e, note)
	}
	var mode string
	e = c.QueryRowContext(work, "SELECT state_desc,user_access_desc FROM sys.databases WHERE name=@p1", chosen.Target).Scan(&state, &mode)
	if e != nil {
		return e
	}
	if state != "ONLINE" {
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
	if state != "ONLINE" || mode != "MULTI_USER" {
		return fmt.Errorf("состояние базы после RESTORE: %s / %s", state, mode)
	}
	note("SQL подтвердил ONLINE / MULTI_USER")
	return nil
}
func restoreFailure(l *Live, plan RestorePlan, prior string, cause error, note func(string)) error {
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	var state string
	var id int
	err := l.conn.QueryRowContext(ctx, "SELECT state_desc,database_id FROM sys.databases WHERE name=@p1", plan.Target).Scan(&state, &id)
	if errors.Is(err, sql.ErrNoRows) {
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
	note("Состояние после ошибки: " + state)
	return fmt.Errorf("RESTORE не завершён, %s · %s: %w", plan.Target, strings.TrimSpace(state), cause)
}
