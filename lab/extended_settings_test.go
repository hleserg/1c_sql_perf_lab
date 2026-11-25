package lab

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOptionPlanRoundTripsEveryValidCombination(t *testing.T) {
	for initial := 0; initial < 16; initial++ {
		for target := 0; target < 16; target++ {
			entries, err := optionPlan(Entry{23, initial, target})
			if !validOptions(initial) || !validOptions(target) {
				if err == nil {
					t.Fatal(initial, target)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			v := initial
			for _, e := range entries {
				if e.Before != v {
					t.Fatal(entries)
				}
				diff := e.After ^ e.Before
				if diff == 0 || (diff&^pageMask != 0 && diff != closeMask && diff != shrinkMask) {
					t.Fatal(entries)
				}
				v = e.After
			}
			if v != target {
				t.Fatal(entries)
			}
			for i := len(entries) - 1; i >= 0; i-- {
				if entries[i].After != v {
					t.Fatal(entries)
				}
				v = entries[i].Before
			}
			if v != initial {
				t.Fatal(entries)
			}
		}
	}
}

type optionFailure struct {
	*Demo
	writes int
}

func (b *optionFailure) Set(ctx context.Context, id, v int) error {
	b.writes++
	if b.writes == 2 {
		return errors.New("остановка перед вторым ALTER")
	}
	return b.Demo.Set(ctx, id, v)
}
func TestPartialDBOptionsRevertWithoutTouchingOtherFlags(t *testing.T) {
	r, d := testRun(t)
	r.Demo = false
	d.Settings[23] = 13 // TORN_PAGE_DETECTION, both automatic options ON
	b := &optionFailure{Demo: d}
	r.Backend = b
	_, _, err := r.Execute(context.Background(), []int{23})
	if err == nil || b.Settings[23] != 13 || b.writes != 3 {
		t.Fatal(err, b.Settings, b.writes)
	}
	if _, err := os.Stat(r.pending()); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestRecoveryOfStagedAndAppliedInstanceChanges(t *testing.T) {
	ch := Entry{28, 4096, 6144}
	for _, pair := range [][2]int{{4096, 4096}, {4096, 6144}, {6144, 4096}, {6144, 6144}, {8192, 4096}, {6144, 8192}} {
		s := configState{configured: pair[0], running: pair[1]}
		want := pair[0] != 8192 && pair[1] != 8192
		if validConfigTransition(ch, s, true) != want {
			t.Fatal(pair)
		}
		if validConfigTransition(ch, s, false) != (pair == [2]int{4096, 4096}) {
			t.Fatal(pair)
		}
	}
}

type instanceStand struct {
	*suiteStand
	ops     []Entry
	noPlans bool
}

func (s *instanceStand) CheckInstanceTrial(_ context.Context, id, target int) (string, error) {
	if id == 34 && s.noPlans {
		return "", skippedStep{"нет одноразовых планов"}
	}
	return "измерено на уровне экземпляра", nil
}
func (s *instanceStand) ChangeInstance(ctx context.Context, ch Entry, undo bool) error {
	if undo {
		ch.Before, ch.After = ch.After, ch.Before
	}
	if ch.ID != advancedOptions && s.Settings[advancedOptions] != 1 {
		return errors.New("advanced options off")
	}
	if s.Settings[ch.ID] != ch.Before {
		return errors.New("изменено извне")
	}
	s.ops = append(s.ops, ch)
	return s.Set(ctx, ch.ID, ch.After)
}
func TestServerTrialsRequireSeparateConsentAndRestoreAdvancedOptions(t *testing.T) {
	r, base := newSuiteRun(t)
	b := &instanceStand{suiteStand: base}
	r.Backend = b
	b.Settings[28] = 8192
	r.Profile.MaxServerMemoryMB = 4096
	r.Profile.TestAdHocWorkloads = true
	rep, _, err := r.Execute(context.Background(), nil)
	if err != nil || len(b.ops) != 0 || coverage(t, rep, 28).Status != statusSkipped || coverage(t, rep, 34).Status != statusSkipped {
		t.Fatal(err, b.ops)
	}
	r.Profile.IsolatedInstanceTrials = true
	rep, _, err = r.Execute(context.Background(), nil)
	if err != nil || coverage(t, rep, 28).Status != statusControl || coverage(t, rep, 34).Status != statusControl || b.Settings[28] != 8192 || b.Settings[34] != 0 || b.Settings[advancedOptions] != 0 {
		t.Fatal(err, b.ops, rep.Coverage)
	}
	if !reflect.DeepEqual(b.ops[:4], []Entry{{advancedOptions, 0, 1}, {28, 8192, 4096}, {28, 4096, 8192}, {advancedOptions, 1, 0}}) {
		t.Fatal(b.ops)
	}
	b.noPlans = true
	r.Profile.MaxServerMemoryMB = 0
	rep, _, err = r.Execute(context.Background(), nil)
	if err != nil || coverage(t, rep, 28).Status != statusSkipped || coverage(t, rep, 34).Status != statusSkipped {
		t.Fatal(err, rep.Coverage)
	}
}
func TestOtherDatabaseWithUnfinishedInstanceChangeBlocksNewRun(t *testing.T) {
	r, b := newSuiteRun(t)
	j := Journal{Server: r.Profile.ExpectedServer, Database: "stand_two", Entries: []Entry{{34, 0, 1}}}
	if err := SaveJSON(filepath.Join(r.Root, "local", "rollback-other.json"), j); err != nil {
		t.Fatal(err)
	}
	_, _, err := r.Execute(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "stand_two") || len(b.jobs) != 0 {
		t.Fatal(err, b.jobs)
	}
}

// The SQL service in CI is disposable. A separate explicit memory target is
// required even when somebody supplies a DSN to run integration tests locally.
func testExtendedSettingsSQL(t *testing.T, ctx context.Context, l *Live) {
	value := os.Getenv("PERFLAB_CI_MAX_SERVER_MEMORY_MB")
	if value == "" {
		t.Skip("requires explicit opt-in for disposable instance-wide SQL configuration")
	}
	capMB, err := strconv.Atoi(value)
	if err != nil || capMB < 1024 {
		t.Fatal("invalid cap for disposable test fixture")
	}
	var total, free int
	if err := l.conn.QueryRowContext(ctx, `SELECT total_physical_memory_kb/1024,available_physical_memory_kb/1024 FROM sys.dm_os_sys_memory`).Scan(&total, &free); err != nil {
		t.Fatal(err)
	}
	if total < capMB+512 || free < 512 {
		t.Fatalf("not enough RAM for disposable fixture, total %d MB, available %d MB, SQL target %d MB", total, free, capMB)
	}
	previous := l.P
	defer func() { l.P = previous }()
	l.P.IsolatedInstanceTrials = true
	l.P.TestAdHocWorkloads = true
	l.P.MaxServerMemoryMB = capMB
	// The outer TestSQLIntegration owns the database lock. Reuse its session.
	var identity string
	if err := l.conn.QueryRowContext(ctx, `SELECT CONVERT(varchar(36),service_broker_guid) FROM sys.databases WHERE database_id=DB_ID()`).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	r := Run{Profile: l.P, Root: t.TempDir(), Backend: l}
	report := Report{Before: map[int]int{}, Targets: map[int]int{}}
	x := experiment{run: r, rep: &report, path: filepath.Join(r.Root, "report.json"), pending: r.pending(), journal: Journal{Server: l.P.ExpectedServer, Database: l.P.Database, Identity: identity}}
	initialDB, err := l.Get(ctx, 23)
	if err != nil {
		t.Fatal(err)
	}
	// Model a restored disposable database with TORN_PAGE_DETECTION; return the
	// original actual SQL options on test exit even on assertion failure.
	fixture := initialDB&^pageMask | 1
	if fixture != initialDB {
		ch := Entry{23, initialDB, fixture}
		if err := l.setDBOption(ctx, ch, false); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if e := l.setDBOption(ctx, ch, true); e != nil {
				t.Error(e)
			}
		}()
	}
	if _, err := l.conn.ExecContext(ctx, `SELECT DISTINCT TOP (37) name, type, object_id FROM sys.all_objects ORDER BY name, type, object_id /* Perf Lab disposable ad hoc fixture */`); err != nil {
		t.Fatal(err)
	}
	advanced, err := l.Get(ctx, advancedOptions)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{23, 28, 34} {
		t.Run(Label(id), func(t *testing.T) {
			if err := x.capture(ctx, id); err != nil {
				t.Fatal(err)
			}
			before, goal := report.Before[id], report.Targets[id]
			if before == goal {
				t.Fatalf("fixture has no contrast for %d (%d)", id, goal)
			}
			x.load = func(ctx context.Context) (map[string]float64, error) {
				start := time.Now()
				v, e := l.Get(ctx, id)
				if e != nil {
					return nil, e
				}
				if v != goal {
					return nil, errors.New("не подтверждено целевое значение на экземпляре")
				}
				return map[string]float64{"SQL": max(.001, float64(time.Since(start).Nanoseconds())/1e6)}, nil
			}
			if err := x.trial(ctx, ByID(id).Title, []int{id}); err != nil {
				t.Fatal(err)
			}
			after, err := l.Get(ctx, id)
			if err != nil || after != before {
				t.Fatal(after, before, err)
			}
			a, err := l.Get(ctx, advancedOptions)
			if err != nil || a != advanced {
				t.Fatal(a, advanced, err)
			}
		})
	}
}
