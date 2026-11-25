package lab

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

type suiteStand struct {
	*Demo
	ids, sqlSamples, erpSamples           int
	jobs                                  []string
	failJob                               string
	erpError, rollbackError, identityLost bool
	cancelOnChange                        context.CancelFunc
	setFailID                             int
	sectionOnlyError                      string
	loseIdentityOnSet                     int
	touched                               []int
}

func (d *suiteStand) Identify(ctx context.Context) (map[string]string, error) {
	d.ids++
	m, e := d.Demo.Identify(ctx)
	m["Автостатистика"] = "1"
	m["Сборка SQL"] = "test-build"
	return m, e
}
func (d *suiteStand) VerifyIdentity(ctx context.Context, id string) error {
	if id != "demo" || d.identityLost {
		return errors.New("потеря идентификатора")
	}
	return ctx.Err()
}
func (d *suiteStand) Measure(ctx context.Context) (map[string]float64, error) {
	d.erpSamples++
	if d.erpError {
		return nil, errors.New("база 1С не зарегистрирована")
	}
	return d.Demo.Measure(ctx)
}
func (d *suiteStand) MeasureSessions(ctx context.Context, n int) (map[string]float64, error) {
	if n != 1 {
		return nil, errors.New("неверное число сеансов")
	}
	return d.Measure(ctx)
}
func (d *suiteStand) MeasureSQL(ctx context.Context) (map[string]float64, error) {
	d.sqlSamples++
	return map[string]float64{"SQL · операция": 2}, ctx.Err()
}
func (d *suiteStand) Set(ctx context.Context, id, v int) error {
	if id == d.setFailID && v != 0 {
		return errors.New("нет доступа к изменению")
	}
	if d.rollbackError && id == 19 && v == 0 {
		return errors.New("сбой отката")
	}
	d.touched = append(d.touched, id)
	e := d.Demo.Set(ctx, id, v)
	if id == d.loseIdentityOnSet && v != 0 {
		d.identityLost = true
	}
	if d.cancelOnChange != nil && v != 0 {
		d.cancelOnChange()
	}
	return e
}
func (d *suiteStand) Work(ctx context.Context, key string, save func(Section) error, note func(string)) error {
	d.jobs = append(d.jobs, key)
	if e := ctx.Err(); e != nil {
		return e
	}
	if key == d.failJob {
		return errors.New("сбой: " + key)
	}
	var s Section
	switch key {
	case "inspect":
		s = Section{ID: 4, Title: "Состояние VLF"}
	case "stats":
		s = Section{ID: 3, Title: "Статистика", Columns: []string{"Результат"}, Rows: [][]string{{"Обновлено"}}}
	case "indexes":
		s = Section{ID: 32, Title: "Индекс", Columns: []string{"Результат"}, Rows: [][]string{{"Обслужено"}}}
	case "backup":
		s = Section{ID: 14, Title: "Копии"}
	case "checkdb":
		s = Section{ID: 15, Title: "Целостность", Rows: [][]string{{"CHECKDB завершён"}}}
	default:
		return errors.New("непредусмотренная работа: " + key)
	}
	if key == d.sectionOnlyError {
		s.Error = "ошибка в протоколе SQL"
	}
	return save(s)
}
func newSuiteRun(t *testing.T) (Run, *suiteStand) {
	t.Helper()
	r, d := testRun(t)
	b := &suiteStand{Demo: d}
	r.Demo = false
	r.Backend = b
	r.JobKey = FullJob.Key
	r.Profile.Samples = 3
	r.Profile.OptimizerBuild = "test-build"
	r.Profile.BackupDir = "D:\\stand"
	return r, b
}
func coverage(t *testing.T, rep Report, id int) ItemResult {
	t.Helper()
	for _, row := range rep.Coverage {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("missing coverage %d", id)
	return ItemResult{}
}
func TestSuiteOnFreshRestoreUsesSQLAndLeavesRACAlone(t *testing.T) {
	r, b := newSuiteRun(t)
	rep, path, err := r.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.ids != 1 || b.erpSamples != 0 || b.sqlSamples == 0 || !reflect.DeepEqual(b.jobs, []string{"inspect", "checkdb", "stats", "indexes", "backup"}) {
		t.Fatal(b.ids, b.erpSamples, b.sqlSamples, b.jobs)
	}
	if rep.Workload == "" || !strings.HasPrefix(rep.Workload, "SQL") || len(rep.Coverage) != 39 {
		t.Fatal(rep.Workload, len(rep.Coverage))
	}
	for _, row := range rep.Coverage {
		if row.Status == "" || row.Reason == "" || row.Status == statusPending {
			t.Fatal(row)
		}
	}
	for _, p := range rep.Phases {
		if p.Sessions != 1 || p.Source != rep.Workload || len(p.Raw) != 3 || !p.Complete {
			t.Fatal(p)
		}
	}
	if coverage(t, rep, ClusterID).Status != statusSkipped || coverage(t, rep, 19).Status != statusControl || len(coverage(t, rep, 4).Evidence) != 1 {
		t.Fatal(rep.Coverage)
	}
	for _, id := range b.touched {
		if id == ClusterID {
			t.Fatal(b.touched)
		}
	}
	if b.Settings[19] != 0 || b.Settings[18] != 0 || b.Settings[24] != 0 || b.Settings[25] != 0 {
		t.Fatal(b.Settings)
	}
	if _, e := os.Stat(r.pending()); !os.IsNotExist(e) {
		t.Fatal(e)
	}
	bytes, e := os.ReadFile(path)
	var stored Report
	if e != nil || json.Unmarshal(bytes, &stored) != nil || len(stored.Coverage) != 39 || stored.Finished.IsZero() {
		t.Fatal(e, stored)
	}
	bts, e := os.ReadFile(strings.TrimSuffix(path, ".json") + ".html")
	if e != nil || !strings.Contains(string(bts), FullJob.Name) {
		t.Fatal(e)
	}
}
func TestSuiteConfiguredERPUsesActualSessions(t *testing.T) {
	r, b := newSuiteRun(t)
	r.Profile.Workload = []string{"erp-test"}
	r.Profile.Cluster.RAC = "stand-rac"
	r.Profile.Sessions = 3
	rep, _, err := r.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.erpSamples == 0 || b.sqlSamples != 0 || coverage(t, rep, 11).Status != statusMeasured || coverage(t, rep, ClusterID).Status != statusControl || b.Settings[ClusterID] != 256 {
		t.Fatal(b.erpSamples, b.sqlSamples, rep.Coverage)
	}
	for _, p := range rep.Phases {
		if p.Name == "После обновления статистики" && p.Baseline != "До обновления статистики" {
			t.Fatal(p)
		}
	}
}
func TestSuiteRecoversFromBrokenERPAndIndividualPermissions(t *testing.T) {
	r, b := newSuiteRun(t)
	r.Profile.Workload = []string{"erp-test"}
	b.erpError = true
	b.setFailID = 19
	rep, _, err := r.Execute(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "база 1С") || b.sqlSamples == 0 || coverage(t, rep, 19).Status != statusFailed || coverage(t, rep, 25).Status != statusControl || coverage(t, rep, 14).Status != statusMeasured {
		t.Fatal(err, b.jobs, rep.Coverage)
	}
	if !strings.HasPrefix(rep.Workload, "SQL") || rep.Phases[0].Complete || rep.Phases[1].Baseline != "" {
		t.Fatal(rep.Phases)
	}
}
func TestSuiteFailedIntegrityPreventsMaintenanceAndBackup(t *testing.T) {
	r, b := newSuiteRun(t)
	b.failJob = "checkdb"
	rep, _, err := r.Execute(context.Background(), nil)
	if err == nil || coverage(t, rep, 15).Status != statusFailed || !reflect.DeepEqual(b.jobs, []string{"inspect", "checkdb"}) {
		t.Fatal(err, b.jobs)
	}
}
func TestSuiteUnsafeRollbackPersistsJournalAndStops(t *testing.T) {
	r, b := newSuiteRun(t)
	b.rollbackError = true
	rep, _, err := r.Execute(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "откат") || b.Settings[19] != 1 || coverage(t, rep, 19).Status != statusFailed || !reflect.DeepEqual(b.jobs, []string{"inspect"}) {
		t.Fatal(err, b.jobs, b.Settings)
	}
	if _, e := os.Stat(r.pending()); e != nil {
		t.Fatal(e)
	}
	b.rollbackError = false
	_, _, err = r.Execute(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "журнал") {
		t.Fatal(err)
	}
}
func TestSuiteCancelReturnsChangedSetting(t *testing.T) {
	r, b := newSuiteRun(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.cancelOnChange = cancel
	_, _, err := r.Execute(ctx, nil)
	if !errors.Is(err, context.Canceled) || b.Settings[18] != 0 || !reflect.DeepEqual(b.jobs, []string{"inspect"}) {
		t.Fatal(err, b.Settings, b.jobs)
	}
	if _, e := os.Stat(r.pending()); !os.IsNotExist(e) {
		t.Fatal(e)
	}
}
func TestSuiteRejectsRelativeBackupAndKeepsIndependentWork(t *testing.T) {
	r, b := newSuiteRun(t)
	r.Profile.BackupDir = "relative/backup"
	r.Profile.OptimizerBuild = ""
	rep, _, err := r.Execute(context.Background(), nil)
	if err != nil || coverage(t, rep, 24).Status != statusSkipped || coverage(t, rep, 14).Status != statusSkipped || !reflect.DeepEqual(b.jobs, []string{"inspect", "checkdb", "stats", "indexes"}) {
		t.Fatal(err, b.jobs, rep.Coverage)
	}
}

func TestSuiteIdentityLossNeverWritesToReplacement(t *testing.T) {
	r, b := newSuiteRun(t)
	b.loseIdentityOnSet = 19
	rep, _, err := r.Execute(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "идентификатор") || b.Settings[19] != 1 || rep.SuiteComplete {
		t.Fatal(err, b.Settings)
	}
	if !reflect.DeepEqual(b.jobs, []string{"inspect"}) {
		t.Fatal(b.jobs)
	}
	j, e := readPending(r.pending())
	if e != nil || len(j.Entries) != 1 || j.Entries[0].ID != 19 {
		t.Fatal(e, j)
	}
}
func TestSuiteProgressKeepsStagesSeparateFromRepetitions(t *testing.T) {
	r, _ := newSuiteRun(t)
	var stage int
	r.Emit = func(ev Event) {
		if ev.SuiteTotal == 0 {
			return
		}
		if ev.SuiteTotal != 7 || ev.SuiteStage < stage || ev.SuiteStage > ev.SuiteTotal || ev.SuiteName == "" {
			t.Fatalf("bad stage: %+v", ev)
		}
		if ev.Values != nil && (ev.Step < 1 || ev.Step > r.Profile.Samples || ev.Total != r.Profile.Samples) {
			t.Fatal(ev)
		}
		stage = ev.SuiteStage
	}
	rep, _, err := r.Execute(context.Background(), nil)
	if err != nil || stage != 7 || !rep.SuiteComplete {
		t.Fatal(err, stage)
	}
}

func TestSuiteNeverContinuesAfterDBCCSectionError(t *testing.T) {
	r, b := newSuiteRun(t)
	b.sectionOnlyError = "checkdb"
	rep, _, err := r.Execute(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "ошибка в протоколе") || rep.SuiteComplete || !reflect.DeepEqual(b.jobs, []string{"inspect", "checkdb"}) {
		t.Fatal(err, b.jobs)
	}
}
