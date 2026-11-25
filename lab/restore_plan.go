package lab

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

const DefaultRestoreDir = `D:\temp`

type BackupFile struct {
	Path     string
	Modified time.Time
}
type RestoreFile struct {
	Logical, Physical, Kind string
	ID                      int
}
type RestorePlan struct {
	Path, Server, Target, Source, SetID, Finished, TargetCreated string
	Position, TargetID                                           int
	Checksums, Demo                                              bool
	Files                                                        []RestoreFile
}

func (p Profile) RestoreFolder() string {
	if p.RestoreDir != "" {
		return p.RestoreDir
	}
	return DefaultRestoreDir
}
func (p RestorePlan) Summary() string {
	action := "Будет создана"
	if p.TargetID != 0 {
		action = "Будет заменена"
	}
	return fmt.Sprintf("%s\nПолная копия %s · %s · набор %d\n%s база %s / %s", p.Path, p.Source, p.Finished, p.Position, action, p.Server, p.Target)
}
func serverPath(dir, name string) string {
	sep := "\\"
	if strings.HasPrefix(dir, "/") {
		sep = "/"
	}
	return strings.TrimRight(dir, "/\\") + sep + name
}
func safeTarget(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "master", "model", "msdb", "tempdb", "resource", "mssqlsystemresource", "distribution":
		return false
	}
	return len([]rune(name)) <= 128 && !strings.ContainsRune(name, 0)
}
func quotedID(s string) string       { return "[" + strings.ReplaceAll(s, "]", "]]") + "]" }
func unicodeLiteral(s string) string { return "N'" + strings.ReplaceAll(s, "'", "''") + "'" }

func openRestore(ctx context.Context, p Profile) (*Live, error) {
	l := &Live{P: p}
	if e := l.connect(ctx, "master"); e != nil {
		l.Close()
		return nil, e
	}
	l.db.SetMaxOpenConns(2) // control session plus a progress reader in master
	if e := checkMaster(ctx, l.conn, p); e != nil {
		l.Close()
		return nil, e
	}
	return l, nil
}
func checkMaster(ctx context.Context, c *sql.Conn, p Profile) error {
	if !safeTarget(p.Database) {
		return errors.New("укажи пользовательскую базу стенда")
	}
	var server, current string
	if e := c.QueryRowContext(ctx, "SELECT CONVERT(nvarchar(128),SERVERPROPERTY('ServerName')),DB_NAME()").Scan(&server, &current); e != nil {
		return e
	}
	if !strings.EqualFold(server, p.ExpectedServer) || current != "master" {
		return fmt.Errorf("профиль ожидает %s; подключение к %s / %s", p.ExpectedServer, server, current)
	}
	return nil
}
func ListBackups(ctx context.Context, p Profile, demo bool) ([]BackupFile, error) {
	if demo {
		return []BackupFile{{serverPath(p.RestoreFolder(), "erp_demo_latest.bak"), time.Now()}, {serverPath(p.RestoreFolder(), "erp_demo_previous.bak"), time.Now().Add(-24 * time.Hour)}}, nil
	}
	l, e := openRestore(ctx, p)
	if e != nil {
		return nil, e
	}
	defer l.Close()
	return listBackups(ctx, l.conn, p.RestoreFolder())
}
func listBackups(ctx context.Context, c *sql.Conn, dir string) ([]BackupFile, error) {
	if !serverAbs(dir) {
		return nil, errors.New("укажи абсолютную папку на сервере SQL")
	}
	rows, e := c.QueryContext(ctx, `SELECT full_filesystem_path,last_write_time FROM sys.dm_os_enumerate_filesystem(@p1,N'*.bak') WHERE is_directory=0 ORDER BY last_write_time DESC,full_filesystem_path`, dir)
	if e != nil {
		return nil, fmt.Errorf("SQL не смог перечислить %s. Можно ввести полный путь к .bak: %w", dir, e)
	}
	defer rows.Close()
	var out []BackupFile
	for rows.Next() {
		var f BackupFile
		var modified sql.NullTime
		if e = rows.Scan(&f.Path, &modified); e != nil {
			return nil, e
		}
		f.Modified = modified.Time
		i := strings.LastIndexAny(f.Path, "/\\")
		if i < 0 || !strings.EqualFold(strings.TrimRight(f.Path[:i], "/\\"), strings.TrimRight(dir, "/\\")) || !strings.HasSuffix(strings.ToLower(f.Path), ".bak") {
			continue
		}
		out = append(out, f)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("SQL не видит файлов .bak в %s", dir)
	}
	return out, nil
}

// RESTORE metadata gains columns between SQL releases. Read by column name.
type metaRow map[string]string

func (r metaRow) get(name string) string {
	for k, v := range r {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func metadata(ctx context.Context, c *sql.Conn, q, path string) ([]metaRow, error) {
	rows, e := c.QueryContext(ctx, q, path)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	names, e := rows.Columns()
	if e != nil {
		return nil, e
	}
	var out []metaRow
	for rows.Next() {
		vals := make([]any, len(names))
		ptr := make([]any, len(vals))
		for i := range vals {
			ptr[i] = &vals[i]
		}
		if e = rows.Scan(ptr...); e != nil {
			return nil, e
		}
		r := metaRow{}
		for i, v := range vals {
			switch x := v.(type) {
			case nil:
			case time.Time:
				r[names[i]] = x.Format("2006-01-02 15:04:05.0000000")
			case []byte:
				if strings.HasSuffix(names[i], "GUID") {
					r[names[i]] = fmt.Sprintf("%x", x)
				} else {
					r[names[i]] = string(x)
				}
			default:
				r[names[i]] = fmt.Sprint(x)
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func truth(s string) bool { return s == "1" || strings.EqualFold(s, "true") }
func fullSet(rows []metaRow) (metaRow, error) {
	var best metaRow
	pos := 0
	for _, r := range rows {
		n, e := strconv.Atoi(r.get("Position"))
		if e != nil || n < 1 || r.get("BackupType") != "1" || r.get("BackupSetGUID") == "" || r.get("BackupFinishDate") == "" || truth(r.get("IsDamaged")) || truth(r.get("IsSnapshot")) {
			continue
		}
		if best == nil || r.get("BackupFinishDate") > best.get("BackupFinishDate") || r.get("BackupFinishDate") == best.get("BackupFinishDate") && n > pos {
			best = r
			pos = n
		}
	}
	if best == nil {
		return nil, errors.New("в файле нет завершённой полной копии базы, пригодной для восстановления")
	}
	return best, nil
}
func PrepareRestore(ctx context.Context, p Profile, path string, demo bool) (RestorePlan, error) {
	if demo {
		return RestorePlan{Demo: true, Path: path, Server: "DEMO", Target: "erp_lab", TargetID: 5, Source: "erp_demo", SetID: "demo", Position: 1, Finished: "демонстрационный набор"}, nil
	}
	l, e := openRestore(ctx, p)
	if e != nil {
		return RestorePlan{}, e
	}
	defer l.Close()
	return prepareRestore(ctx, l.conn, p, path)
}
func prepareRestore(ctx context.Context, c *sql.Conn, p Profile, path string) (RestorePlan, error) {
	var plan RestorePlan
	if !serverAbs(path) {
		return plan, errors.New("укажи полный путь к .bak на сервере SQL")
	}
	if e := checkMaster(ctx, c, p); e != nil {
		return plan, e
	}
	media, e := metadata(ctx, c, "RESTORE LABELONLY FROM DISK=@p1", path)
	if e != nil {
		return plan, e
	}
	if len(media) != 1 || media[0].get("FamilyCount") != "1" {
		return plan, errors.New("копия записана на несколько носителей: этот режим принимает один полный .bak")
	}
	hs, e := metadata(ctx, c, "RESTORE HEADERONLY FROM DISK=@p1", path)
	if e != nil {
		return plan, e
	}
	h, e := fullSet(hs)
	if e != nil {
		return plan, e
	}
	n, _ := strconv.Atoi(h.get("Position"))
	plan = RestorePlan{Path: path, Server: p.ExpectedServer, Target: p.Database, Position: n, Source: h.get("DatabaseName"), SetID: h.get("BackupSetGUID"), Finished: h.get("BackupFinishDate"), Checksums: truth(h.get("HasBackupChecksums"))}
	var state string
	var protected int
	e = c.QueryRowContext(ctx, `SELECT database_id,CONVERT(varchar(30),create_date,126),state_desc,CASE WHEN database_id<=4 OR is_distributor=1 OR is_published=1 OR is_subscribed=1 OR is_merge_published=1 OR source_database_id IS NOT NULL OR replica_id IS NOT NULL OR EXISTS(SELECT 1 FROM sys.databases s WHERE s.source_database_id=d.database_id) THEN 1 ELSE 0 END FROM sys.databases d WHERE name=@p1`, p.Database).Scan(&plan.TargetID, &plan.TargetCreated, &state, &protected)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return plan, e
	}
	if plan.TargetID != 0 && (protected == 1 || state != "ONLINE" && state != "OFFLINE" && state != "RESTORING") {
		return plan, fmt.Errorf("цель %s недоступна для обычной замены, состояние %s", p.Database, state)
	}
	old := map[int]RestoreFile{}
	if plan.TargetID != 0 {
		rows, e := c.QueryContext(ctx, `SELECT file_id,type_desc,physical_name FROM sys.master_files WHERE database_id=@p1`, plan.TargetID)
		if e != nil {
			return plan, e
		}
		for rows.Next() {
			var f RestoreFile
			if e = rows.Scan(&f.ID, &f.Kind, &f.Physical); e != nil {
				rows.Close()
				return plan, e
			}
			old[f.ID] = f
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return plan, e
		}
	}
	fs, e := metadata(ctx, c, fmt.Sprintf("RESTORE FILELISTONLY FROM DISK=@p1 WITH FILE=%d", n), path)
	if e != nil {
		return plan, e
	}
	if len(fs) == 0 {
		return plan, errors.New("в копии отсутствуют файлы")
	}
	var dataDir, logDir string
	e = c.QueryRowContext(ctx, `SELECT CONVERT(nvarchar(4000),SERVERPROPERTY('InstanceDefaultDataPath')),CONVERT(nvarchar(4000),SERVERPROPERTY('InstanceDefaultLogPath'))`).Scan(&dataDir, &logDir)
	if e != nil {
		return plan, e
	}
	hash := sha256.Sum256([]byte(p.Database + "|" + plan.SetID))
	for _, row := range fs {
		id, e := strconv.Atoi(row.get("FileID"))
		kind := row.get("Type")
		if e != nil || id < 1 || row.get("LogicalName") == "" || (kind != "D" && kind != "L") || !truth(row.get("IsPresent")) {
			return plan, fmt.Errorf("неподдерживаемый файл копии: имя %q, тип %q, FileId %q, IsPresent %q", row.get("LogicalName"), kind, row.get("FileID"), row.get("IsPresent"))
		}
		f := RestoreFile{ID: id, Kind: kind, Logical: row.get("LogicalName")}
		prev, exists := old[id]
		if exists && ((kind == "D" && prev.Kind == "ROWS") || (kind == "L" && prev.Kind == "LOG")) {
			f.Physical = prev.Physical
		} else {
			dir, ext := dataDir, "ndf"
			if kind == "L" {
				dir, ext = logDir, "ldf"
			} else if id == 1 {
				ext = "mdf"
			}
			f.Physical = serverPath(dir, fmt.Sprintf("perflab_%x_%d.%s", hash[:10], id, ext))
			var occupied int
			e = c.QueryRowContext(ctx, `SELECT CASE WHEN EXISTS(SELECT 1 FROM sys.master_files WHERE physical_name=@p1) OR EXISTS(SELECT 1 FROM sys.dm_os_file_exists(@p1) WHERE file_exists=1 OR file_is_a_directory=1) THEN 1 ELSE 0 END`, f.Physical).Scan(&occupied)
			if e != nil {
				return plan, e
			}
			if occupied != 0 {
				return plan, fmt.Errorf("путь для нового файла занят: %s", f.Physical)
			}
		}
		plan.Files = append(plan.Files, f)
	}
	sort.Slice(plan.Files, func(i, j int) bool { return plan.Files[i].ID < plan.Files[j].ID })
	return plan, nil
}
func samePlan(a, b RestorePlan) bool { return reflect.DeepEqual(a, b) }
