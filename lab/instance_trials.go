package lab

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Kept only in the rollback journal, never a catalog item or user selection.
const advancedOptions = -100

func instanceOption(id int) string {
	switch id {
	case 28:
		return "max server memory (MB)"
	case 34:
		return "optimize for ad hoc workloads"
	case advancedOptions:
		return "show advanced options"
	}
	return ""
}

type instanceTrials interface {
	CheckInstanceTrial(context.Context, int, int) (string, error)
	ChangeInstance(context.Context, Entry, bool) error
}

func instanceLock(ctx context.Context, conn *sql.Conn, mode string) error {
	// App locks are database scoped; selecting master inside sp_executesql makes
	// different test databases on this instance contend on the same resource.
	var n int
	err := conn.QueryRowContext(ctx, `EXEC master.sys.sp_executesql
 N'DECLARE @rc int=0;
 IF COALESCE(APPLOCK_MODE(N''public'',N''1c_sql_perf_lab_instance_options'',N''Session''),N'''') NOT IN (N''Exclusive'',@mode)
 EXEC @rc=sys.sp_getapplock @Resource=N''1c_sql_perf_lab_instance_options'',@LockMode=@mode,@LockOwner=N''Session'',@LockTimeout=0;
 SELECT @rc',N'@mode nvarchar(32)',@mode=@p1`, mode).Scan(&n)
	if err != nil {
		return err
	}
	if n < 0 {
		return skippedStep{"На экземпляре идёт другая работа Perf Lab. Для серверного изменения требуется выделенный экземпляр."}
	}
	return nil
}

// The Shared-to-Exclusive upgrade holds two acquisitions of the same
// resource. Release both while this physical session is still pinned.
func releaseInstanceLock(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `EXEC master.sys.sp_executesql N'
 DECLARE @rc int;
 WHILE APPLOCK_MODE(N''public'',N''1c_sql_perf_lab_instance_options'',N''Session'') IN(N''Shared'',N''Exclusive'')
 BEGIN
     EXEC @rc=sys.sp_releaseapplock @Resource=N''1c_sql_perf_lab_instance_options'',@LockOwner=N''Session'';
     IF @rc<0 THROW 50004, N''Cannot release instance application lock'', 1;
 END;'`)
	return err
}

type configState struct{ configured, running, min, max int }

func (l *Live) configState(ctx context.Context, id int) (configState, error) {
	var s configState
	err := l.conn.QueryRowContext(ctx, `SELECT CONVERT(int,value),CONVERT(int,value_in_use),CONVERT(int,minimum),CONVERT(int,maximum)
 FROM sys.configurations WHERE name=@p1 AND is_dynamic=1`, instanceOption(id)).Scan(&s.configured, &s.running, &s.min, &s.max)
	return s, err
}
func effectiveOption(id, configured int) int {
	if id == 28 && configured == 0 {
		return 2147483647
	}
	return configured
}
func (l *Live) readInstanceOption(ctx context.Context, id int) (int, error) {
	s, err := l.configState(ctx, id)
	if err != nil {
		return 0, err
	}
	if s.running != effectiveOption(id, s.configured) {
		return 0, fmt.Errorf("%s: задано %d, действует %d; есть неприменённое изменение", instanceOption(id), s.configured, s.running)
	}
	return s.configured, nil
}

// RECONFIGURE operates on the whole instance. Refuse staged changes belonging
// to another operation. The two documented default memory pairs are stable.
const pendingConfiguration = `SELECT name FROM sys.configurations WHERE name<>@p1 AND value<>value_in_use
 AND NOT (name=N'max server memory (MB)' AND CONVERT(bigint,value)=0 AND CONVERT(bigint,value_in_use)=2147483647)
 AND NOT (name=N'min server memory (MB)' AND CONVERT(bigint,value)=0 AND CONVERT(bigint,value_in_use) IN(8,16))`

func (l *Live) CheckInstanceTrial(ctx context.Context, id, target int) (string, error) {
	if !l.P.IsolatedInstanceTrials {
		return "", skippedStep{"Серверные эксперименты отключены в профиле."}
	}
	if err := instanceLock(ctx, l.conn, "Exclusive"); err != nil {
		return "", err
	}
	l.instanceTrialsUsed = true
	var other string
	err := l.conn.QueryRowContext(ctx, `SELECT TOP(1) name FROM (`+pendingConfiguration+`) p ORDER BY name`, "").Scan(&other)
	if err == nil {
		return "", skippedStep{"В экземпляре уже ожидает применения изменение " + other}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	state, err := l.configState(ctx, id)
	if err != nil {
		return "", err
	}
	if state.configured < state.min || state.configured > state.max {
		return "", skippedStep{fmt.Sprintf("%s: исходное значение %d нельзя восстановить через sp_configure (диапазон %d…%d)", instanceOption(id), state.configured, state.min, state.max)}
	}
	if target < state.min || target > state.max {
		return "", skippedStep{fmt.Sprintf("%s: цель %d вне диапазона SQL %d…%d", instanceOption(id), target, state.min, state.max)}
	}
	switch id {
	case 28:
		var minimum int
		if err := l.conn.QueryRowContext(ctx, `SELECT CONVERT(int,value_in_use) FROM sys.configurations WHERE name=N'min server memory (MB)'`).Scan(&minimum); err != nil {
			return "", err
		}
		if target < minimum {
			return "", skippedStep{fmt.Sprintf("Цель %d МБ меньше действующего min server memory %d МБ", target, minimum)}
		}
		return fmt.Sprintf("Экземпляр SQL · max server memory: задано %d МБ, действует %d МБ, цель профиля %d МБ.", state.configured, state.running, target), nil
	case 34:
		var plans, bytes int64
		err := l.conn.QueryRowContext(ctx, `SELECT COUNT_BIG(*),COALESCE(SUM(CONVERT(bigint,size_in_bytes)),0) FROM sys.dm_exec_cached_plans
 WHERE objtype=N'Adhoc' AND cacheobjtype=N'Compiled Plan' AND usecounts=1`).Scan(&plans, &bytes)
		if err != nil {
			return "", err
		}
		if state.running != target && (plans == 0 || bytes == 0) {
			return "", skippedStep{"В кэше не обнаружены одноразовые полные ad hoc планы."}
		}
		return fmt.Sprintf("Экземпляр SQL · %d одноразовых полных ad hoc планов, %d байт. Переключатель влияет на новые планы. Кэш не очищается.", plans, bytes), nil
	}
	return "", errors.New("неизвестный серверный эксперимент")
}

func validConfigTransition(ch Entry, s configState, undo bool) bool {
	if !undo {
		return s.configured == ch.Before && s.running == effectiveOption(ch.ID, ch.Before)
	}
	// sp_configure and RECONFIGURE are not atomic. A canceled forward or reverse
	// change may have staged just one half. Restore only these two known values.
	return (s.configured == ch.Before || s.configured == ch.After) && (s.running == effectiveOption(ch.ID, ch.Before) || s.running == effectiveOption(ch.ID, ch.After))
}
func (l *Live) ChangeInstance(ctx context.Context, ch Entry, undo bool) error {
	key := instanceOption(ch.ID)
	if !undo && (!l.P.IsolatedInstanceTrials || (ch.ID == 28 && (l.P.MaxServerMemoryMB <= 0 || ch.After != l.P.MaxServerMemoryMB)) || (ch.ID == 34 && (!l.P.TestAdHocWorkloads || ch.After != 1)) || (ch.ID == advancedOptions && ch.After != 1)) {
		return errors.New("цель серверной настройки не согласована в профиле")
	}
	if key == "" {
		return errors.New("неизвестная настройка экземпляра")
	}
	if err := instanceLock(ctx, l.conn, "Exclusive"); err != nil {
		return err
	}
	l.instanceTrialsUsed = true
	s, err := l.configState(ctx, ch.ID)
	if err != nil {
		return err
	}
	if !validConfigTransition(ch, s, undo) {
		return fmt.Errorf("%s: настройку изменили извне, задано %d и действует %d. Проверь журнал", key, s.configured, s.running)
	}
	dest := ch.After
	if undo {
		dest = ch.Before
	}
	if s.configured == dest && s.running == effectiveOption(ch.ID, dest) {
		return nil
	}
	if dest < s.min || dest > s.max {
		return fmt.Errorf("%s: значение %d вне диапазона SQL", key, dest)
	}
	const update = `IF NOT EXISTS (SELECT 1 FROM sys.configurations WHERE name=@p1 AND CONVERT(int,value)=@p2 AND CONVERT(int,value_in_use)=@p3)
 THROW 50001, 'Configuration changed outside this trial', 1;
 IF EXISTS (` + pendingConfiguration + `) THROW 50002, 'Another SQL configuration is pending; RECONFIGURE canceled', 1;
 IF @p2<>@p4 EXEC master.sys.sp_configure @configname=@p1,@configvalue=@p4;
 IF EXISTS (` + pendingConfiguration + `) THROW 50002, 'Another SQL configuration is pending; RECONFIGURE canceled', 1;
 RECONFIGURE;`
	if _, err = l.conn.ExecContext(ctx, update, key, s.configured, s.running, dest); err != nil {
		return err
	}
	actual, err := l.readInstanceOption(ctx, ch.ID)
	if err != nil {
		return err
	}
	if actual != dest {
		return fmt.Errorf("%s: проверка не прошла, задано %d вместо %d", key, actual, dest)
	}
	return nil
}

func (x *experiment) checkInstance(ctx context.Context, id int) error {
	p := x.run.Profile
	switch {
	case !p.IsolatedInstanceTrials:
		return skippedStep{"Сняты доступные сведения. Для изменения параметров всего выделенного экземпляра укажи IsolatedInstanceTrials=true."}
	case id == 28 && p.MaxServerMemoryMB <= 0:
		return skippedStep{"Укажи согласованную цель MaxServerMemoryMB для выделенного экземпляра."}
	case id == 34 && !p.TestAdHocWorkloads:
		return skippedStep{"Сняты сведения о кэше. Для эксперимента укажи TestAdHocWorkloads=true."}
	}
	if x.run.Demo {
		return nil
	}
	adapter, ok := x.run.Backend.(instanceTrials)
	if !ok {
		return errors.New("адаптер не поддерживает эксперименты экземпляра")
	}
	observation, err := adapter.CheckInstanceTrial(ctx, id, p.Target(id))
	if err != nil {
		return err
	}
	x.rep.Journal = append(x.rep.Journal, observation)
	return nil
}

func (r Run) checkInstanceJournals() error {
	// All normal runs hold a shared master lock. After a process dies on a
	// different test database its local journal can still own server settings.
	files, err := filepath.Glob(filepath.Join(r.Root, "local", "rollback-*.json"))
	if err != nil {
		return err
	}
	for _, file := range files {
		j, e := readPending(file)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return e
		}
		if !strings.EqualFold(j.Server, r.Profile.ExpectedServer) {
			continue
		}
		for _, ch := range j.Entries {
			if instanceOption(ch.ID) != "" {
				return skippedStep{fmt.Sprintf("В журнале %s осталось изменение экземпляра. Верни настройки базы %s.", filepath.Base(file), j.Database)}
			}
		}
	}
	return nil
}

func (l *Live) settingEvidence(ctx context.Context, id int, when string) Section {
	if id == 28 {
		return fetchSection(ctx, l.conn, check{28, when + " · память экземпляра и ОС", `SELECT
 (SELECT CONVERT(int,value_in_use) FROM sys.configurations WHERE name=N'max server memory (MB)') AS [Действующий лимит МБ],
 p.physical_memory_in_use_kb/1024 AS [Память процесса SQL МБ],s.committed_kb/1024 AS [Выделено SQL МБ],s.committed_target_kb/1024 AS [Цель SQL МБ],
 m.available_physical_memory_kb/1024 AS [Доступно ОС МБ],p.process_physical_memory_low AS [Нехватка физической памяти],
 p.process_virtual_memory_low AS [Нехватка виртуальной памяти]
 FROM sys.dm_os_process_memory p CROSS JOIN sys.dm_os_sys_info s CROSS JOIN sys.dm_os_sys_memory m`})
	}
	return fetchSection(ctx, l.conn, check{34, when + " · одноразовые планы и заглушки экземпляра", `SELECT cacheobjtype AS [Вид объекта],COUNT_BIG(*) AS [Объектов],SUM(CONVERT(bigint,size_in_bytes)) AS [Байт]
 FROM sys.dm_exec_cached_plans WHERE objtype=N'Adhoc' AND usecounts=1 GROUP BY cacheobjtype ORDER BY cacheobjtype`})
}

func (x *experiment) settingEvidence(ctx context.Context, group []int, when string) error {
	if len(group) != 1 || (group[0] != 28 && group[0] != 34) {
		return nil
	}
	recorder, ok := x.run.Backend.(interface {
		settingEvidence(context.Context, int, string) Section
	})
	if !ok {
		return nil
	}
	sec := recorder.settingEvidence(ctx, group[0], when)
	x.rep.Sections = append(x.rep.Sections, sec)
	x.run.say(Event{Phase: when, Detail: sec.Title, Section: &sec})
	if err := x.save(); err != nil {
		return err
	}
	if sec.Error != "" {
		return errors.New(sec.Error)
	}
	return nil
}
