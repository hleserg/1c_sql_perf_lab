package lab

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type Backend interface {
	Identify(context.Context) (map[string]string, error)
	Get(context.Context, int) (int, error)
	Set(context.Context, int, int) error
	Measure(context.Context) (map[string]float64, error)
}
type Event struct {
	SuiteName              string           `json:",omitempty"`
	SuiteStage, SuiteTotal int              `json:",omitempty"`
	Restore                *RestoreProgress `json:",omitempty"`
	Delivery               bool             `json:",omitempty"`
	Section                *Section         `json:",omitempty"`
	Phase, Detail          string
	Step, Total            int
	Values                 map[string]float64
}
type Phase struct {
	Started                         time.Time `json:",omitzero"`
	Source, Baseline                string    `json:",omitempty"`
	Sessions                        int       `json:",omitempty"`
	ElapsedMS                       int64     `json:",omitempty"`
	Name                            string
	IDs                             []int
	Raw                             []map[string]float64
	TelemetryBefore, TelemetryAfter *Counters
	Complete                        bool
}
type Report struct {
	SuiteComplete             bool             `json:",omitempty"`
	Workload                  string           `json:",omitempty"`
	Coverage                  []ItemResult     `json:",omitempty"`
	Steps                     []SuiteStep      `json:",omitempty"`
	Restore                   *RestoreProgress `json:",omitempty"`
	Delivery                  string           `json:",omitempty"`
	Job                       string           `json:",omitempty"`
	Sections                  []Section        `json:",omitempty"`
	Started                   time.Time
	Finished                  time.Time
	Demo                      bool
	Profile                   string
	Inventory                 map[string]string
	Samples, Warmup, Sessions int
	Before, Targets           map[int]int
	Phases                    []Phase
	Journal                   []string
	Error                     string
}
type Entry struct{ ID, Before, After int }
type Journal struct {
	Server, Database  string `json:",omitempty"`
	Identity, Cluster string
	Entries           []Entry
}
type Run struct {
	RequireERP      bool
	ERPPrepareError error
	ERPDiagnostics  []string
	Restore         *RestorePlan
	JobKey          string
	Profile         Profile
	Root            string
	Demo            bool
	Emit            func(Event)
	Backend         Backend
}

func (r Run) pending() string {
	return filepath.Join(r.Root, "local", "rollback-"+r.Profile.Key()+".json")
}
func (r Run) say(e Event) {
	if r.Emit != nil {
		r.Emit(e)
	}
}
func (r Run) restore(ctx context.Context, j *Journal, path string) error {
	for len(j.Entries) > 0 {
		if err := r.verify(ctx, j.Identity); err != nil {
			return err
		}
		i := len(j.Entries) - 1
		ch := j.Entries[i]
		if e := r.change(ctx, ch, true); e != nil {
			return e
		}
		j.Entries = j.Entries[:i]
		if e := SaveJSON(path, j); e != nil {
			return e
		}
	}
	if e := os.Remove(path); e != nil && !os.IsNotExist(e) {
		return e
	}
	return nil
}
func (r Run) Recover(ctx context.Context) error {
	// The live backend holds a database session lock from Identify through Close.
	facts, e := r.Backend.Identify(ctx)
	if e != nil {
		return e
	}
	j, e := readPending(r.pending())
	if e != nil {
		return e
	}
	if j.Identity != facts["Идентификатор базы"] {
		return errors.New("база отличается от журнала")
	}
	for _, entry := range j.Entries {
		if entry.ID != ClusterID {
			continue
		}
		if live, ok := r.Backend.(*Live); ok && live.needsRecoveryRAS(ctx, j.Cluster) {
			release, err := live.recoverCluster(ctx, j.Cluster, func(s string) { r.say(Event{Phase: "Возврат настроек", Detail: s}) })
			if err != nil {
				return err
			}
			defer release()
			r.Profile.Cluster = live.P.Cluster
		}
		if j.Cluster != clusterKey(r.Profile) {
			return errors.New("кластер отличается от журнала")
		}
		break
	}
	return r.restore(ctx, &j, r.pending())
}
func (r Run) Execute(ctx context.Context, ids []int) (rep Report, path string, err error) {
	p := r.Profile
	now := time.Now()
	baseDir := filepath.Join(r.Root, "runs")
	if e := os.MkdirAll(baseDir, 0700); e != nil {
		return rep, "", e
	}
	dir, e := os.MkdirTemp(baseDir, now.Format("20060102-150405")+"-")
	if e != nil {
		return rep, "", e
	}
	path = filepath.Join(dir, "report.json")
	rep = Report{Journal: append([]string(nil), r.ERPDiagnostics...), Started: now, Demo: r.Demo, Profile: p.Name, Samples: p.Samples, Warmup: p.Warmup, Sessions: p.Sessions, Before: map[int]int{}, Targets: map[int]int{}}
	if r.JobKey == FullJob.Key {
		initSuite(&rep)
	}
	defer func() {
		if r.JobKey == FullJob.Key {
			finishCoverage(&rep, err)
		}
		rep.Finished = time.Now()
		if err != nil {
			rep.Error = err.Error()
		}
		err = errors.Join(err, SaveJSON(path, rep), WriteHTML(filepath.Join(dir, "report.html"), rep))
	}()
	if r.ERPPrepareError != nil {
		rep.Workload = "1С · исходный замер не получен"
		return rep, path, fmt.Errorf("Подготовка подключения 1С: %w", r.ERPPrepareError)
	}
	if r.Restore != nil && r.JobKey == FullJob.Key {
		return rep, path, errors.New("полный прогон выполняется на уже восстановленной базе")
	}
	if r.Restore != nil {
		err = runRestore(ctx, r, &rep, *r.Restore)
		return
	}
	r.say(Event{Phase: "Инвентаризация", Detail: "Проверка стенда и исходных настроек"})
	rep.Inventory, err = r.Backend.Identify(ctx)
	if err != nil {
		return
	}
	pending := r.pending()
	if r.Demo {
		pending = filepath.Join(dir, "demo-state.json")
	}
	if _, e := os.Stat(pending); e == nil {
		return rep, path, errors.New("остался журнал изменения: нажми «Вернуть настройки»")
	} else if !os.IsNotExist(e) {
		return rep, path, e
	}
	if !r.Demo {
		if e := r.checkInstanceJournals(); e != nil {
			return rep, path, e
		}
	}
	x := experiment{run: r, rep: &rep, path: path, pending: pending, journal: Journal{Server: r.Profile.ExpectedServer, Database: r.Profile.Database, Identity: rep.Inventory["Идентификатор базы"], Cluster: clusterKey(p)}, load: r.Backend.Measure}
	if t, ok := r.Backend.(Telemetry); ok {
		x.snapshot = t.Snapshot
	}
	if r.JobKey != "" {
		if len(ids) > 0 {
			return rep, path, errors.New("выбери работу SQL или серию экспериментов")
		}
		if r.JobKey == FullJob.Key {
			err = x.fullSuite(ctx)
			return
		}
		err = r.job(ctx, r.JobKey, &rep, path, func(name string, group []int) error { return x.measure(ctx, name, group) })
		return
	}
	if !r.Demo {
		x.sessions = p.Sessions
		if len(p.Workload) > 0 {
			x.source = "1С · сценарий профиля"
		} else {
			x.source = p.builtinSource()
		}
		rep.Workload = x.source
	}
	var active []int
	seen := map[int]bool{}
	for _, id := range ids {
		if !Runnable(id) || seen[id] {
			return rep, path, fmt.Errorf("неверный или повторный пункт %d", id)
		}
		seen[id] = true
		if err = x.capture(ctx, id); err != nil {
			return
		}
		if rep.Before[id] != rep.Targets[id] {
			active = append(active, id)
		} else {
			rep.Journal = append(rep.Journal, Label(id)+" · уже установлено")
		}
	}
	if err = x.measure(ctx, "Исходный замер", nil); err != nil {
		return
	}
	x.baseline = "Исходный замер"
	for _, id := range active {
		if err = x.trial(ctx, ByID(id).Title, []int{id}); err != nil {
			return
		}
	}
	if len(active) > 1 {
		if err = x.trial(ctx, "Все выбранные вместе", active); err != nil {
			return
		}
	}
	if len(active) > 0 {
		err = x.measure(ctx, "Контроль после отката", nil)
	}
	return
}

func SortedKeys(m map[string]float64) []string {
	k := make([]string, 0, len(m))
	for s := range m {
		k = append(k, s)
	}
	sort.Strings(k)
	return k
}
func Median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	v := append([]float64(nil), vals...)
	sort.Float64s(v)
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}
