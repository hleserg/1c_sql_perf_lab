package lab

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

type FileLayout struct {
	Server, Database string
	Files            []FileTarget
}

type FileTarget struct {
	Name, NewDataPath, Filegroup string
	SizeMB, GrowthMB             *int64
}

func LoadFileLayout(path string) (FileLayout, error) {
	var plan FileLayout
	data, err := os.ReadFile(path)
	if err != nil {
		return plan, err
	}
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&plan); err != nil {
		return plan, err
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return plan, errors.New("после плана в JSON есть лишние данные")
	}
	return plan, nil
}

const pagesPerMB = 128

// sys.database_files.size/growth are signed 32-bit counts of 8 KiB pages.
const maxFileLayoutMB = (1<<31 - 1) / pagesPerMB

func (p FileLayout) validate(profile Profile, apply, isolated bool) error {
	if p.Server == "" || !strings.EqualFold(p.Server, profile.ExpectedServer) || !safeTarget(profile.Database) ||
		(p.Database != "tempdb" && p.Database != profile.Database) {
		return errors.New("сервер или база плана отличаются от стендового профиля")
	}
	if p.Database == "tempdb" && apply && !isolated {
		return errors.New("tempdb общая для всего экземпляра. Для выделенного стенда добавь -isolated-instance")
	}
	if len(p.Files) == 0 {
		return errors.New("в плане нет логических файлов")
	}
	seen := make(map[string]bool)
	paths := make(map[string]bool)
	for _, f := range p.Files {
		if strings.TrimSpace(f.Name) == "" || len(utf16.Encode([]rune(f.Name))) > 128 || strings.IndexFunc(f.Name, unicode.IsControl) >= 0 || seen[f.Name] {
			return fmt.Errorf("недопустимое или повторное имя файла %q", f.Name)
		}
		seen[f.Name] = true
		if f.NewDataPath != "" || f.Filegroup != "" {
			path := canonicalLayoutPath(f.NewDataPath)
			if !serverAbs(f.NewDataPath) || strings.IndexFunc(path, unicode.IsControl) >= 0 || len(utf16.Encode([]rune(path))) > 260 ||
				strings.HasSuffix(path, "/") || strings.Contains("/"+path+"/", "/../") || strings.Contains("/"+path+"/", "/./") ||
				strings.TrimSpace(f.Filegroup) == "" || len(utf16.Encode([]rune(f.Filegroup))) > 128 || strings.IndexFunc(f.Filegroup, unicode.IsControl) >= 0 || paths[path] || f.SizeMB == nil || f.GrowthMB == nil {
				return fmt.Errorf("%s: создание ROWS-файла требует уникального полного NewDataPath, Filegroup, SizeMB и GrowthMB", f.Name)
			}
			paths[path] = true
		}
		if f.SizeMB == nil && f.GrowthMB == nil {
			return fmt.Errorf("%s: задай SizeMB или GrowthMB", f.Name)
		}
		for _, n := range []*int64{f.SizeMB, f.GrowthMB} {
			if n != nil && (*n < 1 || *n > maxFileLayoutMB) {
				return fmt.Errorf("%s: допустимы целые значения от 1 до %d МБ", f.Name, maxFileLayoutMB)
			}
		}
	}
	return nil
}

type layoutFile struct {
	ID                                               int
	Name, Kind, Path, GUID, Volume, Filegroup        string
	Pages, StartupPages, Growth, MaxPages, FreeBytes int64
	Percent, Writable                                bool
	IO                                               FileIO
}

const layoutQuery = `SELECT f.file_id,f.name,f.type_desc,f.physical_name,COALESCE(CONVERT(nvarchar(36),f.file_guid),N''),COALESCE(v.volume_mount_point,N''),COALESCE(fg.name,N''),
 CONVERT(bigint,f.size),CONVERT(bigint,m.size),CONVERT(bigint,f.growth),CONVERT(bigint,f.max_size),COALESCE(v.available_bytes,CONVERT(bigint,-1)),f.is_percent_growth,
 CONVERT(bit,CASE WHEN f.state=0 AND f.is_read_only=0 THEN 1 ELSE 0 END),io.num_of_reads,io.num_of_writes,io.io_stall_read_ms,io.io_stall_write_ms
 FROM sys.database_files f JOIN sys.master_files m ON m.database_id=DB_ID() AND m.file_id=f.file_id
 LEFT JOIN sys.filegroups fg ON fg.data_space_id=f.data_space_id
 JOIN sys.dm_io_virtual_file_stats(DB_ID(),NULL) io ON io.file_id=f.file_id
 OUTER APPLY sys.dm_os_volume_stats(DB_ID(),f.file_id) v WHERE f.type IN(0,1) ORDER BY f.file_id`

func layoutSnapshot(ctx context.Context, c *sql.Conn) (map[string]layoutFile, error) {
	rows, e := c.QueryContext(ctx, layoutQuery)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]layoutFile{}
	for rows.Next() {
		var f layoutFile
		if e = rows.Scan(&f.ID, &f.Name, &f.Kind, &f.Path, &f.GUID, &f.Volume, &f.Filegroup, &f.Pages, &f.StartupPages, &f.Growth, &f.MaxPages, &f.FreeBytes, &f.Percent, &f.Writable, &f.IO.Reads, &f.IO.Writes, &f.IO.ReadWaitMS, &f.IO.WriteWaitMS); e != nil {
			return nil, e
		}
		out[f.Name] = f
	}
	return out, rows.Err()
}

type layoutChange struct {
	file               string
	kind               string
	mb                 int64
	newPath, filegroup string
	growthMB           int64
}

func canonicalLayoutPath(path string) string {
	if strings.HasPrefix(path, "/") {
		return path
	}
	return strings.ToLower(strings.ReplaceAll(path, `\`, "/"))
}

func (x layoutChange) statement(database string) string {
	if x.kind == "ADD" {
		return fmt.Sprintf("ALTER DATABASE %s ADD FILE (NAME = %s, FILENAME = %s, SIZE = %dMB, FILEGROWTH = %dMB) TO FILEGROUP %s",
			quotedID(database), unicodeLiteral(x.file), unicodeLiteral(x.newPath), x.mb, x.growthMB, quotedID(x.filegroup))
	}
	return "ALTER DATABASE " + quotedID(database) + " MODIFY FILE (NAME = " + unicodeLiteral(x.file) + ", " + x.kind + " = " + strconv.FormatInt(x.mb, 10) + "MB)"
}

func planFileChanges(plan FileLayout, files map[string]layoutFile) ([]layoutChange, error) {
	var changes []layoutChange
	volumeIncrease := map[string]int64{}
	for _, want := range plan.Files {
		f, ok := files[want.Name]
		if !ok && want.NewDataPath != "" {
			changes = append(changes, layoutChange{file: want.Name, kind: "ADD", mb: *want.SizeMB, newPath: want.NewDataPath, filegroup: want.Filegroup, growthMB: *want.GrowthMB})
			continue
		}
		if ok && want.NewDataPath != "" && (f.Kind != "ROWS" || canonicalLayoutPath(f.Path) != canonicalLayoutPath(want.NewDataPath) || f.Filegroup != want.Filegroup) {
			return nil, fmt.Errorf("%s: существующий файл имеет другой путь, тип или файловую группу", want.Name)
		}
		if !ok || !f.Writable {
			return nil, fmt.Errorf("файл %q отсутствует, недоступен для записи или не относится к ROWS/LOG", want.Name)
		}
		if want.SizeMB != nil {
			pages := *want.SizeMB * pagesPerMB
			switch {
			case pages < f.Pages:
				return nil, fmt.Errorf("%s: требуемый размер меньше текущего. Уменьшение запрещено", f.Name)
			case f.Kind == "LOG" && *want.SizeMB > 2*1024*1024:
				return nil, fmt.Errorf("%s: размер превосходит предел файла журнала SQL Server", f.Name)
			case pages == f.Pages && plan.Database == "tempdb" && f.StartupPages != pages:
				return nil, fmt.Errorf("%s: текущий размер уже равен цели, а стартовый размер tempdb отличается. Для MODIFY FILE SIZE нужен размер больше текущего", f.Name)
			case pages > f.Pages:
				if f.MaxPages == 0 || f.MaxPages > 0 && pages > f.MaxPages {
					return nil, fmt.Errorf("%s: цель превосходит установленный MAXSIZE", f.Name)
				}
				needed := (pages - f.Pages) * 8192
				if f.Volume != "" {
					volumeIncrease[f.Volume] += needed
					needed = volumeIncrease[f.Volume]
				}
				if f.FreeBytes >= 0 && needed > f.FreeBytes {
					return nil, fmt.Errorf("%s: на томе недостаточно места для плана", f.Name)
				}
				changes = append(changes, layoutChange{file: f.Name, kind: "SIZE", mb: *want.SizeMB})
			}
		}
		if want.GrowthMB != nil && (f.Percent || f.Growth != *want.GrowthMB*pagesPerMB) {
			if f.Kind == "LOG" && *want.GrowthMB > 2*1024*1024 {
				return nil, fmt.Errorf("%s: шаг превосходит предел файла журнала SQL Server", f.Name)
			}
			changes = append(changes, layoutChange{file: f.Name, kind: "FILEGROWTH", mb: *want.GrowthMB})
		}
	}
	return changes, nil
}

// SQL Server also checks physical file existence when executing ADD FILE.
const availableLayoutPath = `DB_ID()=@p1 AND NOT EXISTS (SELECT 1 FROM sys.database_files WHERE name=@p2)
 AND NOT EXISTS(SELECT 1 FROM sys.master_files WHERE physical_name=@p3)
 AND EXISTS(SELECT 1 FROM sys.dm_os_file_exists(@p3) WHERE file_exists=0 AND file_is_a_directory=0 AND parent_directory_exists=1)
 AND EXISTS(SELECT 1 FROM sys.filegroups WHERE name=@p4 AND type='FG' AND is_read_only=0)`

func (x layoutChange) checkDestination(ctx context.Context, c *sql.Conn, dbID int) error {
	var ok bool
	err := c.QueryRowContext(ctx, "SELECT CONVERT(bit,CASE WHEN "+availableLayoutPath+" THEN 1 ELSE 0 END)", dbID, x.file, x.newPath, x.filegroup).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s: путь занят, каталог недоступен или файловая группа недоступна для записи", x.file)
	}
	return nil
}

func (x layoutChange) apply(ctx context.Context, c *sql.Conn, dbID int, database string, f layoutFile) error {
	if x.kind == "ADD" {
		guard := "IF NOT (" + availableLayoutPath + ") THROW 51000,'New file destination no longer available',1; "
		_, err := c.ExecContext(ctx, guard+x.statement(database), dbID, x.file, x.newPath, x.filegroup)
		return err
	}
	// Check file identity and old settings in the same SQL batch as the change.
	const guard = `IF DB_ID()<>@p1 OR NOT EXISTS(SELECT 1 FROM sys.database_files WHERE file_id=@p2 AND name=@p3 AND physical_name=@p4 AND size=@p5 AND growth=@p6 AND is_percent_growth=@p7 AND max_size=@p8 AND COALESCE(CONVERT(nvarchar(36),file_guid),N'')=@p9)
 THROW 51000,'File settings changed since inspection',1; `
	_, err := c.ExecContext(ctx, guard+x.statement(database), dbID, f.ID, f.Name, f.Path, f.Pages, f.Growth, f.Percent, f.MaxPages, f.GUID)
	return err
}

func (x layoutChange) verify(old, actual layoutFile, database string) error {
	if x.kind == "ADD" {
		if actual.Name != x.file || actual.Kind != "ROWS" || actual.Filegroup != x.filegroup || canonicalLayoutPath(actual.Path) != canonicalLayoutPath(x.newPath) ||
			actual.Percent || actual.Growth != x.growthMB*pagesPerMB {
			return errors.New("добавленный файл или его прирост не подтверждён")
		}
	} else if !fileIDsMatch(old, actual) {
		return errors.New("логический файл изменился во время проверки")
	}
	if x.kind == "FILEGROWTH" {
		if actual.Percent || actual.Growth != x.mb*pagesPerMB {
			return errors.New("фиксированный прирост не подтверждён")
		}
	} else if actual.Pages != x.mb*pagesPerMB || database == "tempdb" && actual.StartupPages != x.mb*pagesPerMB {
		return errors.New("заданный размер не подтверждён")
	}
	return nil
}

func layoutMB(pages int64) string {
	// An 8KiB page is exactly 0.0078125 MiB.
	return strconv.FormatFloat(float64(pages)/pagesPerMB, 'f', -1, 64)
}
func layoutGrowth(f layoutFile) string {
	if f.Percent {
		return fmt.Sprintf("%d %%", f.Growth)
	}
	return layoutMB(f.Growth) + " МБ"
}
func approvedMB(v *int64) string {
	if v == nil {
		return "—"
	}
	return strconv.FormatInt(*v, 10)
}
func fileIDsMatch(a, b layoutFile) bool {
	return a.ID == b.ID && a.Name == b.Name && a.Path == b.Path && a.GUID == b.GUID && a.Kind == b.Kind
}

func layoutDetails(plan FileLayout, files map[string]layoutFile, title string) Section {
	s := Section{ID: 26, Title: title, Columns: []string{"Файл", "Тип", "Размер МБ", "Старт tempdb МБ", "Рост", "Свободно на томе МБ", "Цель МБ", "Цель роста МБ"}}
	if plan.Database == "tempdb" {
		s.ID = 17
	}
	for _, w := range plan.Files {
		f, ok := files[w.Name]
		if !ok {
			s.Rows = append(s.Rows, []string{w.Name, "нет файла", "—", "—", "—", "—", approvedMB(w.SizeMB), approvedMB(w.GrowthMB)})
			continue
		}
		startup, free := "—", "—"
		if plan.Database == "tempdb" {
			startup = layoutMB(f.StartupPages)
		}
		if f.FreeBytes >= 0 {
			free = strconv.FormatInt(f.FreeBytes/(1<<20), 10)
		}
		s.Rows = append(s.Rows, []string{f.Name, f.Kind, layoutMB(f.Pages), startup, layoutGrowth(f), free, approvedMB(w.SizeMB), approvedMB(w.GrowthMB)})
	}
	return s
}

func layoutLatency(ctx context.Context, c *sql.Conn, plan FileLayout, first map[string]layoutFile, interval time.Duration, title string) (Section, map[string]layoutFile, error) {
	s := Section{ID: 10, Title: title, Columns: []string{"Файл", "Чтений", "Записей", "Чтение мс/IO", "Запись мс/IO", "Интервал мс"}}
	begin := time.Now()
	select {
	case <-ctx.Done():
		return s, nil, ctx.Err()
	case <-time.After(interval):
	}
	last, e := layoutSnapshot(ctx, c)
	if e != nil {
		return s, nil, e
	}
	s.ElapsedMS = time.Since(begin).Milliseconds()
	for _, f := range plan.Files {
		a, ok := first[f.Name]
		b, found := last[f.Name]
		if !ok || !found || !fileIDsMatch(a, b) {
			continue
		}
		r, w := b.IO.Reads-a.IO.Reads, b.IO.Writes-a.IO.Writes
		rd, wr := b.IO.ReadWaitMS-a.IO.ReadWaitMS, b.IO.WriteWaitMS-a.IO.WriteWaitMS
		if r < 0 || w < 0 || rd < 0 || wr < 0 {
			return s, nil, fmt.Errorf("%s: счётчики I/O сброшены", f.Name)
		}
		avg := func(n, ms int64) string {
			if n == 0 {
				return "—"
			}
			return fmt.Sprintf("%.2f", float64(ms)/float64(n))
		}
		s.Rows = append(s.Rows, []string{f.Name, fmt.Sprint(r), fmt.Sprint(w), avg(r, rd), avg(w, wr), fmt.Sprint(s.ElapsedMS)})
	}
	return s, last, nil
}

func layoutGuard(ctx context.Context, c *sql.Conn, plan FileLayout, apply bool) (int, error) {
	var server, name string
	var id int
	var writable bool
	e := c.QueryRowContext(ctx, `SELECT CONVERT(nvarchar(128),SERVERPROPERTY('ServerName')),d.name,d.database_id,
 CONVERT(bit,CASE WHEN d.state=0 AND d.is_read_only=0 AND (d.database_id=2 OR NOT (`+protectedDatabase+`)) THEN 1 ELSE 0 END)
 FROM sys.databases d WHERE d.database_id=DB_ID()`).Scan(&server, &name, &id, &writable)
	if e != nil {
		return 0, e
	}
	if !strings.EqualFold(server, plan.Server) || name != plan.Database || (id <= 4 && !(id == 2 && name == "tempdb")) || apply && !writable {
		return 0, fmt.Errorf("неподходящая цель %s / %s, ID %d", server, name, id)
	}
	mode := "Shared"
	if apply {
		mode = "Exclusive"
	}
	return id, labLock(ctx, c, name, mode)
}

func operateLayout(ctx context.Context, c *sql.Conn, plan FileLayout, apply bool, report *Report, save func() error, window time.Duration) error {
	id, e := layoutGuard(ctx, c, plan, apply)
	if e != nil {
		return e
	}
	read, e := layoutSnapshot(ctx, c)
	if e != nil {
		return e
	}
	report.Sections = append(report.Sections, layoutDetails(plan, read, "До изменения"))
	before, latest, e := layoutLatency(ctx, c, plan, read, window, "I/O до изменения")
	if e != nil {
		return e
	}
	report.Sections = append(report.Sections, before)
	changes, e := planFileChanges(plan, latest)
	if e != nil {
		return e
	}
	for _, change := range changes {
		if change.kind == "ADD" {
			if err := change.checkDestination(ctx, c, id); err != nil {
				return err
			}
		}
	}
	sectionID := 26
	if plan.Database == "tempdb" {
		sectionID = 17
	}
	ops := Section{ID: sectionID, Title: "Согласованные операции", Columns: []string{"Файл", "Команда", "Состояние", "Время мс"}}
	for _, w := range plan.Files {
		matched := false
		for _, x := range changes {
			if x.file != w.Name {
				continue
			}
			matched = true
			ops.Rows = append(ops.Rows, []string{x.file, x.statement(plan.Database), "Запланировано", "—"})
		}
		if !matched {
			ops.Rows = append(ops.Rows, []string{w.Name, "—", "Уже соответствует", "—"})
		}
	}
	report.Sections = append(report.Sections, ops)
	opIndex := len(report.Sections) - 1
	if e = save(); e != nil {
		return e
	}
	if !apply {
		return nil
	}
	row := 0
	for _, x := range changes {
		for report.Sections[opIndex].Rows[row][1] == "—" {
			row++
		}
		result := report.Sections[opIndex].Rows[row]
		row++
		f := latest[x.file]
		result[2] = "Начато. При обрыве сверь фактические параметры"
		if e = save(); e != nil {
			return e
		}
		start := time.Now()
		execErr := x.apply(ctx, c, id, plan.Database, f)
		result[3] = strconv.FormatInt(time.Since(start).Milliseconds(), 10)
		current, readErr := layoutSnapshot(ctx, c)
		if readErr == nil {
			readErr = x.verify(f, current[x.file], plan.Database)
		}
		if err := errors.Join(execErr, readErr); err != nil {
			result[2] = "Ошибка. Сверь действующие значения"
			if current != nil {
				report.Sections = append(report.Sections, layoutDetails(plan, current, "После ошибки"))
			}
			return errors.Join(err, save())
		}
		result[2] = "Применено и проверено"
		// Keep the original identity of other files across partial progress.
		latest[x.file] = current[x.file]
		if e = save(); e != nil {
			return e
		}
	}
	read, e = layoutSnapshot(ctx, c)
	if e != nil {
		return e
	}
	after, last, e := layoutLatency(ctx, c, plan, read, window, "I/O после изменения")
	if e != nil {
		return e
	}
	report.Sections = append(report.Sections, layoutDetails(plan, last, "После проверки"), after)
	remaining, e := planFileChanges(plan, last)
	if e != nil {
		return e
	}
	if len(remaining) != 0 {
		return errors.New("при финальной проверке обнаружено расхождение с планом")
	}
	return save()
}

// FileLayoutRun changes only approved files, leaves existing MAXSIZE untouched,
// and writes a checkpoint before and after each individual SQL alteration.
func FileLayoutRun(ctx context.Context, p Profile, config FileLayout, apply, isolated bool, root string) (rep Report, path string, err error) {
	if err = config.validate(p, apply, isolated); err != nil {
		return
	}
	runs := filepath.Join(root, "runs")
	if err = os.MkdirAll(runs, 0700); err != nil {
		return
	}
	dir, e := os.MkdirTemp(runs, time.Now().Format("20060102-150405")+"-files-")
	if e != nil {
		return rep, "", e
	}
	path = filepath.Join(dir, "report.json")
	mode := "Предпросмотр"
	if apply {
		mode = "Применение"
	}
	rep = Report{Started: time.Now(), Profile: p.Name, Job: "Размер и прирост файлов SQL", Inventory: map[string]string{"SQL": config.Server, "База": config.Database, "Режим": mode}}
	defer func() {
		rep.Finished = time.Now()
		if err != nil {
			rep.Error = err.Error()
		}
		err = errors.Join(err, SaveJSON(path, rep), WriteHTML(filepath.Join(dir, "report.html"), rep))
	}()
	l := &Live{P: p}
	defer l.Close()
	if err = l.connect(ctx, config.Database); err != nil {
		return
	}
	err = operateLayout(ctx, l.conn, config, apply, &rep, func() error { return SaveJSON(path, rep) }, time.Second)
	return
}
