package lab

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Backend interface {
	Identify(context.Context) (map[string]string, error)
	Get(context.Context, int) (int, error)
	Set(context.Context, int, int) error
	Measure(context.Context) (map[string]float64, error)
}
type Event struct {
	Delivery      bool     `json:",omitempty"`
	Section       *Section `json:",omitempty"`
	Phase, Detail string
	Step, Total   int
	Values        map[string]float64
}
type Phase struct {
	Name                            string
	IDs                             []int
	Raw                             []map[string]float64
	TelemetryBefore, TelemetryAfter *Counters
	Complete                        bool
}
type Report struct {
	Delivery                  string    `json:",omitempty"`
	Job                       string    `json:",omitempty"`
	Sections                  []Section `json:",omitempty"`
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
	Identity, Cluster string
	Entries           []Entry
}
type Run struct {
	Restore *RestorePlan
	JobKey  string
	Profile Profile
	Root    string
	Demo    bool
	Emit    func(Event)
	Backend Backend
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
		i := len(j.Entries) - 1
		ch := j.Entries[i]
		value, e := r.Backend.Get(ctx, ch.ID)
		if e != nil {
			return e
		}
		if value != ch.Before {
			if value != ch.After {
				return fmt.Errorf("%s: значение изменено извне (%d), проверь вручную", Label(ch.ID), value)
			}
			if e = r.Backend.Set(ctx, ch.ID, ch.Before); e != nil {
				return e
			}
			actual, e := r.Backend.Get(ctx, ch.ID)
			if e != nil {
				return e
			}
			if actual != ch.Before {
				return errors.New("проверка отката не прошла")
			}
		}
		j.Entries = j.Entries[:i]
		if e = SaveJSON(path, j); e != nil {
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
	if j.Identity != facts["Идентификатор базы"] || j.Cluster != clusterKey(r.Profile) {
		return errors.New("база или кластер отличаются от журнала")
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
	rep = Report{Started: now, Demo: r.Demo, Profile: p.Name, Samples: p.Samples, Warmup: p.Warmup, Sessions: p.Sessions, Before: map[int]int{}, Targets: map[int]int{}}
	defer func() {
		rep.Finished = time.Now()
		if err != nil {
			rep.Error = err.Error()
		}
		err = errors.Join(err, SaveJSON(path, rep), WriteHTML(filepath.Join(dir, "report.html"), rep))
	}()
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
		return rep, path, errors.New("остался журнал изменения: выбери «Восстановить»")
	} else if !os.IsNotExist(e) {
		return rep, path, e
	}
	var active []int
	seen := map[int]bool{}
	for _, id := range ids {
		if !Runnable(id) || seen[id] {
			return rep, path, fmt.Errorf("неверный или повторный пункт %d", id)
		}
		seen[id] = true
		if id == 24 && !r.Demo && (p.OptimizerBuild == "" || p.OptimizerBuild != rep.Inventory["Сборка SQL"]) {
			return rep, path, errors.New("для №24 укажи в OptimizerBuild проверенную сборку из пункта 2")
		}
		if id == 25 && !r.Demo && rep.Inventory["Автостатистика"] != "1" {
			return rep, path, errors.New("№25 требует включённой AUTO_UPDATE_STATISTICS")
		}
		if id == ClusterID && (p.Cluster.Connections < 1 || p.Cluster.Connections > 100000) {
			return rep, path, errors.New("задай целевой лимит соединений рабочего сервера")
		}
		before, e := r.Backend.Get(ctx, id)
		if e != nil {
			return rep, path, e
		}
		rep.Before[id] = before
		rep.Targets[id] = p.Target(id)
		if before != p.Target(id) {
			active = append(active, id)
		} else {
			rep.Journal = append(rep.Journal, Label(id)+" · уже установлено")
		}
	}
	j := Journal{Identity: rep.Inventory["Идентификатор базы"], Cluster: clusterKey(p)}
	restore := func() error {
		if len(j.Entries) == 0 {
			return nil
		}
		r.say(Event{Phase: "Откат", Detail: "Возвращаем настройки и сверяем фактические значения"})
		clean, stop := context.WithTimeout(context.Background(), 2*time.Minute)
		defer stop()
		if e := r.restore(clean, &j, pending); e != nil {
			return e
		}
		rep.Journal = append(rep.Journal, "Исходные значения восстановлены и проверены")
		return nil
	}
	defer func() { err = errors.Join(err, restore()) }()
	var reference map[string]float64
	measure := func(name string, group []int) error {
		rep.Phases = append(rep.Phases, Phase{Name: name, IDs: append([]int(nil), group...)})
		phase := &rep.Phases[len(rep.Phases)-1]
		for i := 1 - p.Warmup; i <= p.Samples; i++ {
			msg := "Замер"
			if i <= 0 {
				msg = "Прогрев"
			}
			r.say(Event{Phase: name, Detail: msg, Step: max(i, 0), Total: p.Samples})
			if i == 1 {
				if t, ok := r.Backend.(Telemetry); ok {
					v := t.Snapshot(ctx)
					phase.TelemetryBefore = &v
				}
			}
			one, cancel := context.WithTimeout(ctx, p.Timeout())
			values, e := r.Backend.Measure(one)
			cancel()
			if e != nil {
				return e
			}
			if len(values) == 0 {
				return errors.New("нагрузка вернула пустой результат")
			}
			for k, v := range values {
				if strings.TrimSpace(k) == "" || v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
					return errors.New("ошибка в названии или длительности операции")
				}
			}
			if reference == nil {
				reference = values
			}
			if len(values) != len(reference) {
				return errors.New("изменился набор операций")
			}
			for k := range reference {
				if _, ok := values[k]; !ok {
					return errors.New("изменился набор операций")
				}
			}
			if i > 0 {
				phase.Raw = append(phase.Raw, values)
				r.say(Event{Phase: name, Detail: "Образец записан", Step: i, Total: p.Samples, Values: values})
			}
			if e = SaveJSON(path, rep); e != nil {
				return e
			}
		}
		if t, ok := r.Backend.(Telemetry); ok {
			v := t.Snapshot(ctx)
			phase.TelemetryAfter = &v
		}
		phase.Complete = true
		return nil
	}
	trial := func(name string, group []int) error {
		for _, id := range group {
			v, e := r.Backend.Get(ctx, id)
			if e != nil {
				return e
			}
			if v != rep.Before[id] {
				return errors.New("исходная настройка изменилась между сериями")
			}
			ch := Entry{id, v, p.Target(id)}
			j.Entries = append(j.Entries, ch)
			if e = SaveJSON(pending, j); e != nil {
				j.Entries = j.Entries[:len(j.Entries)-1]
				return e
			}
			if e = r.Backend.Set(ctx, id, ch.After); e != nil {
				return e
			}
			v, e = r.Backend.Get(ctx, id)
			if e != nil {
				return e
			}
			if v != ch.After {
				return errors.New("настройка не вступила в силу")
			}
			rep.Journal = append(rep.Journal, fmt.Sprintf("%s · %d → %d", Label(id), ch.Before, ch.After))
		}
		if e := measure(name, group); e != nil {
			return e
		}
		return restore()
	}
	if r.JobKey != "" {
		if len(ids) > 0 {
			return rep, path, errors.New("выбери работу SQL или серию экспериментов")
		}
		err = r.job(ctx, r.JobKey, &rep, path, measure)
		return
	}
	if err = measure("Исходный замер", nil); err != nil {
		return
	}
	for _, id := range active {
		if err = trial(ByID(id).Title, []int{id}); err != nil {
			return
		}
	}
	if len(active) > 1 {
		if err = trial("Все выбранные вместе", active); err != nil {
			return
		}
	}
	if len(active) > 0 {
		err = measure("Контроль после отката", nil)
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
