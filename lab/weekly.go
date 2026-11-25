package lab

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var weeklyName = regexp.MustCompile("^perflab_weekly_[0-9a-f]{32}$")

// WeeklyOptions pins the operator's intended lab instance, measurement database
// and backup source. The target database name is never supplied by the caller.
type WeeklyOptions struct {
	Server, Measurement, Source, Folder string
	MaxAge                              time.Duration
}

func (o WeeklyOptions) Validate(p Profile) error {
	if !strings.EqualFold(o.Server, p.ExpectedServer) || !strings.EqualFold(o.Measurement, p.Database) || o.Server == "" || !safeTarget(o.Measurement) {
		return errors.New("профиль отличается от закреплённого экземпляра или измерительной базы")
	}
	if !safeTarget(o.Source) || !serverAbs(o.Folder) || o.MaxAge <= 0 || o.MaxAge > 7*24*time.Hour {
		return errors.New("для недельной проверки укажи исходную базу, абсолютную папку бэкапов SQL и срок свежести до 168 часов")
	}
	return nil
}

func WeeklyVerify(ctx context.Context, p Profile, root string, o WeeklyOptions, say func(string)) (path string, err error) {
	if err = o.Validate(p); err != nil {
		return
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return
	}
	target := "perflab_weekly_" + hex.EncodeToString(id[:])
	start := time.Now()
	dir := filepath.Join(root, "runs", "weekly", start.Format("20060102-150405")+"-"+hex.EncodeToString(id[:]))
	if err = os.MkdirAll(dir, 0700); err != nil {
		return
	}
	path = filepath.Join(dir, "report.json")
	rep := Report{Started: start, Profile: p.Name, Job: "Недельное восстановление и CHECKDB", Inventory: map[string]string{"SQL": o.Server, "Измерительная база": o.Measurement, "Исходная база": o.Source, "Одноразовая база": target}}
	defer func() {
		rep.Finished = time.Now()
		if err != nil {
			rep.Error = err.Error()
		}
		err = errors.Join(err, SaveJSON(path, &rep), WriteHTML(filepath.Join(dir, "report.html"), rep))
	}()
	p.Database = target
	l, e := openRestore(ctx, p)
	if e != nil {
		return path, e
	}
	defer l.Close()
	scope := sha256.Sum256([]byte(strings.ToUpper(o.Server + "|" + o.Source)))
	key := fmt.Sprintf("weekly-%x", scope[:16])
	if e = labLock(ctx, l.conn, key, "Exclusive"); e != nil {
		return path, e
	}
	history := filepath.Join(root, "local", key+".json")
	pending := filepath.Join(root, "local", key+"-pending.json")
	if e = checkWeeklyPending(ctx, l.conn, pending); e != nil {
		return path, e
	}
	var previous string
	if data, e := os.ReadFile(history); e == nil {
		if e = json.Unmarshal(data, &previous); e != nil {
			return path, e
		}
	} else if !os.IsNotExist(e) {
		return path, e
	}
	plan, e := weeklyPlan(ctx, l.conn, p, o, previous)
	if e != nil {
		return path, e
	}
	rep.Inventory["Файл бэкапа"] = plan.Path
	rep.Inventory["Окончание копирования"] = plan.Finished
	rep.Inventory["Идентификатор набора"] = plan.SetID
	rep.Inventory["CHECKSUM бэкапа"] = strconv.FormatBool(plan.Checksums)
	note := func(s string) {
		rep.Journal = append(rep.Journal, s)
		if say != nil {
			say(s)
		}
	}
	var writeStart time.Time
	var writeDuration time.Duration
	log := &restoreLog{emit: func(s string, v RestoreProgress) {
		if v.Stage == RestoreWrite && writeStart.IsZero() {
			writeStart = time.Now()
		} else if v.Stage == RestoreAccess && writeDuration == 0 && !writeStart.IsZero() {
			writeDuration = time.Since(writeStart)
		}
		rep.Restore = &v
		note(s)
	}}
	note("Новая одноразовая база: " + target)
	if e = SaveJSON(path, &rep); e != nil {
		return path, e
	}
	// A failed or interrupted verification can leave a full copy behind.
	// Do not allocate another one next week until that database has been reviewed.
	if e = SaveJSON(pending, target); e != nil {
		return path, e
	}
	if e = applyRestore(ctx, l, p, plan, log); e != nil {
		return path, fmt.Errorf("восстановление в %s: %w", target, e)
	}
	rep.Sections = append(rep.Sections, Section{ID: 15, Title: "RESTORE DATABASE", ElapsedMS: writeDuration.Milliseconds(),
		Columns: []string{"Одноразовая база", "Позиция набора", "Состояние", "RESTORE мс"},
		Rows:    [][]string{{target, strconv.Itoa(plan.Position), log.p.State, strconv.FormatInt(writeDuration.Milliseconds(), 10)}}})
	if e = SaveJSON(path, &rep); e != nil {
		return path, e
	}
	// Retain a failed restore or integrity check for investigation. Only a
	// database successfully created and checked in this invocation may be dropped.
	if e = weeklyCheckAndRemove(ctx, l, plan, &rep, note); e != nil {
		return path, fmt.Errorf("проверь одноразовую базу %s на %s: %w", target, o.Server, e)
	}
	// Persist the positive evidence before deduplicating future weekly runs.
	if e = SaveJSON(path, &rep); e != nil {
		return path, e
	}
	return path, errors.Join(SaveJSON(history, plan.SetID), os.Remove(pending))
}

func checkWeeklyPending(ctx context.Context, c *sql.Conn, path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var name string
	if err = json.Unmarshal(data, &name); err != nil {
		return err
	}
	if !weeklyName.MatchString(name) {
		return errors.New("повреждена запись предыдущей недельной проверки")
	}
	var exists int
	if err = c.QueryRowContext(ctx, "SELECT COALESCE(DB_ID(@p1),0)", name).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return fmt.Errorf("осталась одноразовая база %s предыдущего прогона. Разбери результат до нового восстановления", name)
	}
	return nil
}

func eligibleWeekly(h metaRow, source, previous string, now time.Time, age time.Duration) bool {
	if !strings.EqualFold(h.get("DatabaseName"), source) || strings.EqualFold(h.get("BackupSetGUID"), previous) {
		return false
	}
	// RESTORE HEADERONLY records source-local wall time. The lab and backup
	// source must use the same time zone. Compare against SQL Server's clock.
	ended, e := time.Parse("2006-01-02 15:04:05.9999999", h.get("BackupFinishDate"))
	if e != nil {
		return false
	}
	elapsed := now.Sub(ended)
	return elapsed >= 0 && elapsed <= age
}

func weeklyPlan(ctx context.Context, c *sql.Conn, p Profile, o WeeklyOptions, previous string) (RestorePlan, error) {
	if !weeklyName.MatchString(p.Database) || strings.EqualFold(p.Database, o.Measurement) || strings.EqualFold(p.Database, o.Source) {
		return RestorePlan{}, errors.New("неверное имя одноразовой базы")
	}
	files, e := listBackups(ctx, c, o.Folder)
	if e != nil {
		return RestorePlan{}, e
	}
	var clock string
	if e = c.QueryRowContext(ctx, "SELECT CONVERT(varchar(27),SYSDATETIME(),121)").Scan(&clock); e != nil {
		return RestorePlan{}, e
	}
	now, e := time.Parse("2006-01-02 15:04:05.9999999", clock)
	if e != nil {
		return RestorePlan{}, e
	}
	var chosen metaRow
	var path string
	for _, f := range files {
		hs, e := metadata(ctx, c, "RESTORE HEADERONLY FROM DISK=@p1", f.Path)
		if e != nil {
			return RestorePlan{}, fmt.Errorf("заголовок %s: %w", f.Path, e)
		}
		h, e := fullSet(hs)
		if e != nil || !strings.EqualFold(h.get("DatabaseName"), o.Source) {
			continue
		}
		if chosen == nil || h.get("BackupFinishDate") > chosen.get("BackupFinishDate") {
			chosen = h
			path = f.Path
		}
	}
	if chosen == nil || !eligibleWeekly(chosen, o.Source, previous, now, o.MaxAge) {
		return RestorePlan{}, errors.New("в папке нет свежей новой полной копии указанной базы; проверка не выполнена")
	}
	plan, e := prepareRestore(ctx, c, p, path)
	if e != nil {
		return RestorePlan{}, e
	}
	if plan.TargetID != 0 || !strings.EqualFold(plan.SetID, chosen.get("BackupSetGUID")) || !strings.EqualFold(plan.Source, o.Source) {
		return RestorePlan{}, errors.New("источник изменился либо имя одноразовой базы уже занято")
	}
	return plan, nil
}

func weeklyCheckAndRemove(ctx context.Context, l *Live, plan RestorePlan, rep *Report, note func(string)) error {
	if plan.TargetID != 0 || !weeklyName.MatchString(plan.Target) || !strings.EqualFold(plan.Target, l.P.Database) {
		return errors.New("неподтверждённая одноразовая база")
	}
	// Pin the post-restore SQL database GUID, database_id, and exact paths
	// before CHECKDB. Recheck the same identity as part of the DROP batch.
	var id int
	var guid string
	if e := l.conn.QueryRowContext(ctx, "SELECT d.database_id,CONVERT(varchar(36),s.database_guid) FROM sys.databases d JOIN sys.database_recovery_status s ON s.database_id=d.database_id WHERE d.name=@p1 AND d.database_id>4", plan.Target).Scan(&id, &guid); e != nil {
		return e
	}
	verifyFiles := "(SELECT COUNT_BIG(*) FROM sys.master_files WHERE database_id=@p2)=" + strconv.Itoa(len(plan.Files))
	for _, f := range plan.Files {
		verifyFiles += " AND EXISTS (SELECT 1 FROM sys.master_files WHERE database_id=@p2 AND file_id=" + strconv.Itoa(f.ID) + " AND physical_name=" + unicodeLiteral(f.Physical) + ")"
	}
	identity := "EXISTS (SELECT 1 FROM sys.databases d JOIN sys.database_recovery_status s ON s.database_id=d.database_id WHERE d.name=@p1 AND d.database_id=@p2 AND s.database_guid=CONVERT(uniqueidentifier,@p3)) AND " + verifyFiles
	guard := "IF NOT (" + identity + ") THROW 51000, 'Disposable database identity changed', 1;"
	if _, e := l.conn.ExecContext(ctx, guard, plan.Target, id, guid); e != nil {
		return e
	}
	check, e := l.db.Conn(ctx)
	if e != nil {
		return e
	}
	defer check.Close()
	if _, e = check.ExecContext(ctx, "USE "+quotedID(plan.Target)); e != nil {
		return e
	}
	worker := &Live{conn: check}
	e = worker.Work(ctx, "checkdb", func(s Section) error { rep.Sections = append(rep.Sections, s); return nil }, note)
	if e != nil {
		return e
	}
	if _, e = check.ExecContext(ctx, "USE master"); e != nil {
		return e
	}
	if _, e = l.conn.ExecContext(ctx, guard+" DROP DATABASE "+quotedID(plan.Target), plan.Target, id, guid); e != nil {
		return e
	}
	note("CHECKDB завершён. Одноразовая база удалена.")
	return nil
}
