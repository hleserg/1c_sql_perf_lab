package lab

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The same executable, arguments and package are used for both copies.
// Database names and the run nonce reach the adapter through its environment.
type RestructurePlan struct {
	Server, BaselineDB, VariantDB, BackupSetUUID string
	PackageID, PackagePath, PackageSHA256        string
	AdapterPath, AdapterSHA256                   string
	AdapterArgs                                  []string
	VariantMaxDOP, TimeoutSeconds                int
}

func (p RestructurePlan) targets(stand Profile) error {
	if p.Server == "" || !strings.EqualFold(stand.ExpectedServer, p.Server) || !safeTarget(p.BaselineDB) || !safeTarget(p.VariantDB) || strings.EqualFold(p.BaselineDB, p.VariantDB) || !uuid.MatchString(p.BackupSetUUID) {
		return errors.New("закрепи экземпляр профиля, две разные пользовательские базы и UUID общего полного бэкапа")
	}
	return nil
}
func (p RestructurePlan) validate() error {
	for _, h := range []string{p.PackageSHA256, p.AdapterSHA256} {
		b, err := hex.DecodeString(h)
		if err != nil || len(b) != sha256.Size {
			return errors.New("задай SHA-256 поставки и адаптера, по 64 шестнадцатеричных знака")
		}
	}
	if !filepath.IsAbs(p.PackagePath) || !filepath.IsAbs(p.AdapterPath) || p.PackagePath == p.AdapterPath || strings.TrimSpace(p.PackageID) == "" || p.VariantMaxDOP < 1 || p.VariantMaxDOP > 64 || p.TimeoutSeconds < 1 || p.TimeoutSeconds > 86400 {
		return errors.New("проверь абсолютные пути, ID поставки, VariantMaxDOP (1…64) и TimeoutSeconds (1…86400)")
	}
	if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(p.AdapterPath), ".exe") {
		return errors.New("адаптер Windows должен быть отдельным EXE")
	}
	switch strings.ToLower(strings.TrimSuffix(filepath.Base(p.AdapterPath), filepath.Ext(p.AdapterPath))) {
	case "cmd", "powershell", "pwsh", "sh", "bash", "dash", "zsh":
		return errors.New("укажи исполняемый адаптер обновления, запускаемый напрямую")
	}
	return nil
}
func verifyTrialFile(ctx context.Context, path, digest string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("файл поставки или адаптера должен быть обычным файлом")
	}
	h := sha256.New()
	buf := make([]byte, 256*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := f.Read(buf)
		h.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), digest) {
		return fmt.Errorf("SHA-256 не совпал: %s", filepath.Base(path))
	}
	return nil
}
func (p RestructurePlan) verifyFiles(ctx context.Context) error {
	if err := verifyTrialFile(ctx, p.AdapterPath, p.AdapterSHA256); err != nil {
		return err
	}
	return verifyTrialFile(ctx, p.PackagePath, p.PackageSHA256)
}

type restructureDB struct {
	*Live
	broker, guid string
}

// Link the existing rollback journal to a particular restored copy even when
// two copies share a broker GUID. Regular recovery rejects this composite key.
func (l *restructureDB) Identify(ctx context.Context) (map[string]string, error) {
	f, err := l.Live.Identify(ctx)
	if err != nil {
		return nil, err
	}
	l.broker = f["Идентификатор базы"]
	if err = l.conn.QueryRowContext(ctx, `SELECT CONVERT(varchar(36),database_guid) FROM sys.database_recovery_status WHERE database_id=DB_ID()`).Scan(&l.guid); err != nil {
		return nil, err
	}
	f["Идентификатор базы"] = l.broker + "/" + l.guid
	return f, nil
}
func (l *restructureDB) VerifyIdentity(ctx context.Context, identity string) error {
	if identity != l.broker+"/"+l.guid {
		return errors.New("журнал относится к другой восстановленной копии")
	}
	if err := l.Live.VerifyIdentity(ctx, l.broker); err != nil {
		return err
	}
	var guid string
	err := l.conn.QueryRowContext(ctx, `SELECT CONVERT(varchar(36),database_guid) FROM sys.database_recovery_status WHERE database_id=DB_ID()`).Scan(&guid)
	if err == nil && guid != l.guid {
		return errors.New("изменился GUID восстановленной базы")
	}
	return err
}
func newStandLive(p Profile) *Live { return &Live{P: p} }

func (p RestructurePlan) copy(stand Profile, root, db string, open func(Profile) *Live) (*restructureDB, Run) {
	stand.Database = db
	stand.Cluster.RAC = "" // SQL-side operation; never contact a cluster discovered on this computer.
	l := &restructureDB{Live: open(stand)}
	return l, Run{Root: root, Profile: stand, Backend: l}
}
func RecoverRestructure(ctx context.Context, stand Profile, root string, p RestructurePlan) error {
	if err := p.targets(stand); err != nil {
		return err
	}
	l, r := p.copy(stand, root, p.VariantDB, newStandLive)
	defer l.Close()
	facts, err := l.Identify(ctx)
	if err != nil {
		return err
	}
	j, err := readPending(r.pending())
	if err != nil {
		return err
	}
	if j.Identity != facts["Идентификатор базы"] || j.Cluster != clusterKey(r.Profile) || !strings.EqualFold(j.Server, p.Server) || j.Database != p.VariantDB || !strings.Contains(j.Identity, "/") || len(j.Entries) > 1 || (len(j.Entries) == 1 && j.Entries[0].ID != 19) {
		return errors.New("журнал не относится к изменению MAXDOP этой копии")
	}
	return r.restore(ctx, &j, r.pending())
}

type restructureState struct {
	guid, set, family, source, restored, schema string
	restoreID                                   int
	dop, instanceDOP, compatibility             int
}

func readRestructureState(ctx context.Context, l *restructureDB) (v restructureState, err error) {
	// Inspect the most recent actual restore of any kind before filtering the
	// type. A later log/differential/file restore disqualifies the copy.
	const q = serverStateVisible + `
IF NOT EXISTS (SELECT 1 FROM sys.databases d WHERE d.database_id=DB_ID() AND d.state=0 AND d.is_read_only=0 AND d.user_access=0 AND NOT (` + protectedDatabase + `))
 THROW 50003,N'Недоступная или защищённая копия',1;
IF HAS_PERMS_BY_NAME(DB_NAME(),'DATABASE','VIEW DEFINITION')<>1 THROW 50004,N'Требуется VIEW DEFINITION на копии',1;
IF EXISTS(SELECT 1 FROM sys.dm_exec_requests WHERE database_id=DB_ID() AND session_id<>@@SPID AND session_id IN(SELECT session_id FROM sys.dm_exec_sessions WHERE is_user_process=1))
 OR EXISTS(SELECT 1 FROM sys.dm_tran_database_transactions d JOIN sys.dm_tran_session_transactions t ON t.transaction_id=d.transaction_id WHERE d.database_id=DB_ID() AND t.session_id<>@@SPID AND t.is_user_transaction=1)
 THROW 50005,N'В копии активны другие запросы или транзакции',1;
WITH latest AS (SELECT TOP(1) h.restore_history_id,h.backup_set_id,h.restore_type,h.restore_date,h.recovery FROM msdb.dbo.restorehistory h
 WHERE h.destination_database_name=DB_NAME() AND h.restore_type<>'V' ORDER BY h.restore_history_id DESC)
SELECT CONVERT(varchar(36),rs.database_guid),CONVERT(varchar(36),b.backup_set_uuid),CONVERT(varchar(36),rs.family_guid),
 b.database_name,CONVERT(varchar(23),h.restore_date,121),h.restore_history_id,d.compatibility_level,
 (SELECT CONVERT(int,value_in_use) FROM sys.configurations WHERE name='max degree of parallelism'),
 (SELECT CONVERT(int,value) FROM sys.database_scoped_configurations WHERE name='MAXDOP')
FROM latest h JOIN msdb.dbo.backupset b ON b.backup_set_id=h.backup_set_id
 JOIN sys.database_recovery_status rs ON rs.database_id=DB_ID() AND rs.family_guid=b.family_guid
 JOIN sys.databases d ON d.database_id=rs.database_id
WHERE h.restore_type='D' AND h.recovery=1 AND b.type='D' AND b.is_damaged=0`
	err = l.conn.QueryRowContext(ctx, q).Scan(&v.guid, &v.set, &v.family, &v.source, &v.restored, &v.restoreID, &v.compatibility, &v.instanceDOP, &v.dop)
	if err != nil {
		return v, fmt.Errorf("копия %s: проверь последний полный RESTORE WITH RECOVERY, историю msdb и отсутствие активных операций: %w", l.P.Database, err)
	}
	v.schema, err = schemaDigest(ctx, l)
	return
}
func schemaDigest(ctx context.Context, l *restructureDB) (string, error) {
	// Stream the column and index definitions of each table. No table data
	// or object names are retained in the report.
	const q = `SELECT s.name,t.name,CONVERT(nvarchar(max),(SELECT c.name AS n,c.column_id AS id,SCHEMA_NAME(ty.schema_id) AS ts,ty.name AS t,c.max_length AS l,c.precision AS p,c.scale AS sc,c.is_nullable AS nu,c.is_identity AS ident,c.is_computed AS comp,c.collation_name AS coll
 FROM sys.columns c JOIN sys.types ty ON ty.user_type_id=c.user_type_id WHERE c.object_id=t.object_id ORDER BY c.column_id FOR XML RAW('c'),TYPE)),
 CONVERT(nvarchar(max),(SELECT i.name AS n,i.type AS t,i.is_unique AS u,i.has_filter AS f,i.filter_definition AS fd,c.name AS col,ic.key_ordinal AS ko,ic.is_descending_key AS d,ic.is_included_column AS incl
 FROM sys.indexes i JOIN sys.index_columns ic ON ic.object_id=i.object_id AND ic.index_id=i.index_id JOIN sys.columns c ON c.object_id=ic.object_id AND c.column_id=ic.column_id
 WHERE i.object_id=t.object_id AND i.index_id>0 ORDER BY i.name,ic.index_column_id FOR XML RAW('i'),TYPE))
 FROM sys.tables t JOIN sys.schemas s ON s.schema_id=t.schema_id WHERE t.is_ms_shipped=0 ORDER BY s.name,t.name`
	rows, err := l.conn.QueryContext(ctx, q)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	h := sha256.New()
	enc := json.NewEncoder(h)
	count := 0
	for rows.Next() {
		var values [4]string
		if err := rows.Scan(&values[0], &values[1], &values[2], &values[3]); err != nil {
			return "", err
		}
		if err := enc.Encode(values); err != nil {
			return "", err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if count == 0 {
		return "", errors.New("в копии не видны таблицы приложения")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func effectiveDOP(v, instance int) int {
	if v == 0 {
		return instance
	}
	return v
}
func checkRestructurePair(p RestructurePlan, a, b restructureState) error {
	if !strings.EqualFold(a.set, p.BackupSetUUID) || !strings.EqualFold(b.set, p.BackupSetUUID) || a.family != b.family || a.guid == b.guid || a.source != b.source || strings.EqualFold(a.source, p.BaselineDB) || strings.EqualFold(b.source, p.VariantDB) {
		return errors.New("копии должны иметь разные действующие GUID, один заданный полный набор и отдельные от источника имена")
	}
	if a.schema == "" || a.schema != b.schema || a.compatibility != b.compatibility || a.dop != b.dop || a.instanceDOP != b.instanceDOP {
		return errors.New("исходные схемы таблиц/индексов, совместимость или настройки MAXDOP копий различаются")
	}
	if effectiveDOP(a.dop, a.instanceDOP) == p.VariantMaxDOP {
		return errors.New("целевой MAXDOP совпадает с исходным эффективным значением")
	}
	return nil
}

func RunRestructure(ctx context.Context, stand Profile, root string, p RestructurePlan, apply bool) (string, error) {
	return runRestructure(ctx, stand, root, p, apply, newStandLive)
}
func runRestructure(ctx context.Context, stand Profile, root string, p RestructurePlan, apply bool, open func(Profile) *Live) (path string, err error) {
	if err = p.targets(stand); err != nil {
		return
	}
	if err = p.validate(); err != nil {
		return
	}
	dir := filepath.Join(root, "runs")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return
	}
	dir, err = os.MkdirTemp(dir, "restructure-")
	if err != nil {
		return
	}
	path = filepath.Join(dir, "report.json")
	rep := Report{Started: time.Now(), Profile: stand.Name, Job: "№33 · реструктуризация двух копий", Inventory: map[string]string{"Экземпляр": p.Server, "UUID полного бэкапа": p.BackupSetUUID, "Поставка": p.PackageID, "SHA-256 поставки": p.PackageSHA256, "SHA-256 адаптера": p.AdapterSHA256}}
	defer func() {
		rep.Finished = time.Now()
		if err != nil {
			rep.Error = err.Error()
		}
		err = errors.Join(err, SaveJSON(path, rep), WriteHTML(filepath.Join(dir, "report.html"), rep))
	}()
	if err = p.verifyFiles(ctx); err != nil {
		return
	}
	a, ra := p.copy(stand, root, p.BaselineDB, open)
	defer a.Close()
	b, rb := p.copy(stand, root, p.VariantDB, open)
	defer b.Close()
	for _, r := range []Run{ra, rb} {
		if _, err = r.Backend.Identify(ctx); err != nil {
			return
		}
		if _, e := os.Stat(r.pending()); !os.IsNotExist(e) {
			if e != nil {
				return path, e
			}
			return path, errors.New("обнаружен незакрытый журнал настройки копии")
		}
		if err = r.checkInstanceJournals(); err != nil {
			return
		}
	}
	left, e := readRestructureState(ctx, a)
	if e != nil {
		return path, e
	}
	right, e := readRestructureState(ctx, b)
	if e != nil {
		return path, e
	}
	info := Section{ID: 33, Title: "Проверка копий перед обновлением", Columns: []string{"База", "GUID копии", "UUID набора", "Дата RESTORE на SQL", "MAXDOP базы", "MAXDOP экземпляра", "Совместимость", "SHA-256 схемы"}}
	for i, s := range []restructureState{left, right} {
		info.Rows = append(info.Rows, []string{[]string{p.BaselineDB, p.VariantDB}[i], s.guid, s.set, s.restored, strconv.Itoa(s.dop), strconv.Itoa(s.instanceDOP), strconv.Itoa(s.compatibility), s.schema})
	}
	rep.Sections = append(rep.Sections, info)
	if err = checkRestructurePair(p, left, right); err != nil {
		return
	}
	rep.Journal = append(rep.Journal, "Один последовательный запуск на каждой копии. Порядок A → B, состояние кэшей и фоновая нагрузка могут повлиять на результат. Отпечатки описывают столбцы и индексы таблиц. Содержимое строк не сравнивается.")
	attemptPath := func(s restructureState) string {
		return filepath.Join(root, "local", "restructure-attempts", fmt.Sprintf("%s-%d", s.guid, s.restoreID))
	}
	for _, state := range []restructureState{left, right} {
		if _, e := os.Stat(attemptPath(state)); !os.IsNotExist(e) {
			if e != nil {
				return path, e
			}
			return path, errors.New("одну из копий уже передавали адаптеру. Для нового прогона подготовь две свежие копии")
		}
	}
	if !apply {
		rep.Journal = append(rep.Journal, "Предварительная проверка завершена. Для фактического обновления обеих копий используется -apply.")
		return
	}
	if err = SaveJSON(path, rep); err != nil {
		return
	}
	test := func(l *restructureDB, r Run, initial restructureState, dop int) (restructureResult, error) {
		if err := r.verify(ctx, l.broker+"/"+l.guid); err != nil {
			return restructureResult{}, err
		}
		state, err := readRestructureState(ctx, l)
		if err != nil {
			return restructureResult{}, err
		}
		if state.guid != initial.guid || state.set != initial.set || state.restoreID != initial.restoreID || state.schema != initial.schema || state.dop != dop {
			return restructureResult{}, errors.New("состояние копии изменилось перед запуском")
		}
		if err = p.verifyFiles(ctx); err != nil {
			return restructureResult{}, err
		}
		var nonce [16]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return restructureResult{}, err
		}
		receipt := attemptPath(initial)
		if err = os.MkdirAll(filepath.Dir(receipt), 0700); err != nil {
			return restructureResult{}, err
		}
		flag, err := os.OpenFile(receipt, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return restructureResult{}, err
		}
		_, writeErr := fmt.Fprintln(flag, path)
		if err = errors.Join(writeErr, flag.Sync(), flag.Close()); err != nil {
			return restructureResult{}, err
		}
		result, err := p.runAdapter(ctx, l.P.Database, initial.guid, hex.EncodeToString(nonce[:]), dop)
		rep.Sections = append(rep.Sections, result.section(l.P.Database, dop))
		if err != nil {
			return result, err
		}
		if err = r.verify(ctx, l.broker+"/"+l.guid); err != nil {
			return result, err
		}
		end, err := readRestructureState(ctx, l)
		if err != nil {
			return result, err
		}
		if end.guid != initial.guid || end.set != initial.set || end.restoreID != initial.restoreID || end.dop != dop || end.schema == initial.schema {
			return result, errors.New("SQL не подтвердил изменение схемы в той же копии с заданным MAXDOP")
		}
		result.schema = end.schema
		rep.Inventory["SHA-256 схемы после · "+l.P.Database] = end.schema
		return result, SaveJSON(path, rep)
	}
	first, e := test(a, ra, left, left.dop)
	if e != nil {
		return path, e
	}
	x := experiment{run: rb, rep: &rep, pending: rb.pending(), journal: Journal{Server: p.Server, Database: p.VariantDB, Identity: b.broker + "/" + b.guid, Cluster: clusterKey(rb.Profile)}}
	defer func() {
		if len(x.journal.Entries) > 0 {
			clean, stop := context.WithTimeout(context.Background(), 2*time.Minute)
			defer stop()
			if e := rb.restore(clean, &x.journal, x.pending); e != nil {
				err = errors.Join(err, fmt.Errorf("откат не подтверждён, используй restructure-trial -recover: %w", e))
			} else {
				rep.Journal = append(rep.Journal, "Исходный MAXDOP второй копии возвращён и проверен.")
			}
		}
	}()
	if err = rb.verify(ctx, x.journal.Identity); err != nil {
		return
	}
	if err = x.apply(ctx, Entry{ID: 19, Before: right.dop, After: p.VariantMaxDOP}); err != nil {
		return
	}
	second, e := test(b, rb, right, p.VariantMaxDOP)
	if e != nil {
		return path, e
	}
	if first.schema != second.schema || first.proof.Operations != second.proof.Operations || strings.EqualFold(first.proof.EvidenceSHA256, second.proof.EvidenceSHA256) {
		return path, errors.New("для сравнения нужны одинаковые итоговые схемы, одинаковое число операций и отдельный журнал каждой копии")
	}
	rep.Sections = append(rep.Sections, Section{ID: 33, Title: "Длительность реструктуризации по журналу адаптера", Columns: []string{"A, мс", "B, мс", "B − A, мс", "Операций в каждой копии"}, Rows: [][]string{{strconv.FormatInt(first.elapsed(), 10), strconv.FormatInt(second.elapsed(), 10), strconv.FormatInt(second.elapsed()-first.elapsed(), 10), strconv.Itoa(first.proof.Operations)}}})
	return
}
