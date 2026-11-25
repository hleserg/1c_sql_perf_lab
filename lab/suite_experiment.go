package lab

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

type experiment struct {
	snapshot         func(context.Context) Counters
	run              Run
	rep              *Report
	path, pending    string
	journal          Journal
	reference        map[string]float64
	load             func(context.Context) (map[string]float64, error)
	source, baseline string
	sessions         int
}

type stopSuite struct{ error }

func (e stopSuite) Unwrap() error { return e.error }
func (x *experiment) save() error {
	if err := SaveJSON(x.path, x.rep); err != nil {
		return stopSuite{fmt.Errorf("сохранение протокола: %w", err)}
	}
	return nil
}
func (r Run) verify(ctx context.Context, identity string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if guard, ok := r.Backend.(interface {
		VerifyIdentity(context.Context, string) error
	}); ok {
		if err := guard.VerifyIdentity(ctx, identity); err != nil {
			return stopSuite{fmt.Errorf("проверка базы и сеансовой блокировки: %w", err)}
		}
	}
	return nil
}
func (x *experiment) verify(ctx context.Context) error { return x.run.verify(ctx, x.journal.Identity) }

func (x *experiment) capture(ctx context.Context, id int) error {
	p, rep := x.run.Profile, x.rep
	if id == 24 && !x.run.Demo && (p.OptimizerBuild == "" || p.OptimizerBuild != rep.Inventory["Сборка SQL"]) {
		return skippedStep{"для №24 укажи в OptimizerBuild проверенную сборку из пункта 2"}
	}
	if id == 25 && !x.run.Demo && rep.Inventory["Автостатистика"] != "1" {
		return skippedStep{"№25 требует включённой AUTO_UPDATE_STATISTICS"}
	}
	if id == ClusterID && (p.Cluster.Connections < 1 || p.Cluster.Connections > 100000) {
		return skippedStep{"задай целевой лимит соединений рабочего сервера"}
	}

	before, err := x.run.Backend.Get(ctx, id)
	if err != nil {
		return err
	}
	rep.Before[id], rep.Targets[id] = before, p.Target(id)
	return nil
}

func (x *experiment) measure(ctx context.Context, name string, group []int) (err error) {
	if err = x.verify(ctx); err != nil {
		return err
	}
	p := x.run.Profile
	start := time.Now()
	x.rep.Phases = append(x.rep.Phases, Phase{Started: start, Name: name, IDs: append([]int(nil), group...), Source: x.source, Baseline: x.baseline, Sessions: x.sessions})
	phase := &x.rep.Phases[len(x.rep.Phases)-1]
	defer func() { phase.ElapsedMS = time.Since(start).Milliseconds(); err = errors.Join(err, x.save()) }()
	for i := 1 - p.Warmup; i <= p.Samples; i++ {
		msg := "Замер"
		if i <= 0 {
			msg = "Прогрев"
		}
		x.run.say(Event{Phase: name, Detail: msg, Step: max(i, 0), Total: p.Samples})
		if i == 1 && x.snapshot != nil {
			v := x.snapshot(ctx)
			phase.TelemetryBefore = &v
		}
		one, cancel := context.WithTimeout(ctx, p.Timeout())
		values, e := x.load(one)
		if e == nil {
			e = one.Err()
		}
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
		if x.reference == nil {
			x.reference = values
		}
		if len(values) != len(x.reference) {
			return errors.New("изменился набор операций")
		}
		for k := range x.reference {
			if _, ok := values[k]; !ok {
				return errors.New("изменился набор операций")
			}
		}
		if i > 0 {
			phase.Raw = append(phase.Raw, values)
			x.run.say(Event{Phase: name, Detail: "Образец записан", Step: i, Total: p.Samples, Values: values})
		}
		if e = x.save(); e != nil {
			return e
		}
	}
	if x.snapshot != nil {
		v := x.snapshot(ctx)
		phase.TelemetryAfter = &v
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	phase.Complete = true
	return nil
}

// Each trial owns its journal entries until they have been restored and verified.
// The caller can continue after an isolated error only when rollback succeeded.
func (x *experiment) trial(ctx context.Context, name string, group []int) (err error) {
	defer func() {
		if len(x.journal.Entries) == 0 {
			return
		}
		x.run.say(Event{Phase: "Откат", Detail: "Возвращаем настройки и сверяем фактические значения"})
		clean, stop := context.WithTimeout(context.Background(), 2*time.Minute)
		defer stop()
		changes := append([]Entry(nil), x.journal.Entries...)
		if e := x.run.restore(clean, &x.journal, x.pending); e != nil {
			err = errors.Join(err, stopSuite{fmt.Errorf("откат не подтверждён: %w", e)})
			return
		}
		for i := len(changes) - 1; i >= 0; i-- {
			ch := changes[i]
			x.rep.Journal = append(x.rep.Journal, fmt.Sprintf("%s · %s · возврат проверен, фактическое значение %d", time.Now().UTC().Format(time.RFC3339), Label(ch.ID), ch.Before))
		}
		err = errors.Join(err, x.save())
	}()
	for _, id := range group {
		if err := x.verify(ctx); err != nil {
			return err
		}
		v, e := x.run.Backend.Get(ctx, id)
		if e != nil {
			return e
		}
		if v != x.rep.Before[id] {
			return stopSuite{fmt.Errorf("%s: исходная настройка изменилась между сериями", Label(id))}
		}
		ch := Entry{id, v, x.rep.Targets[id]}
		x.journal.Entries = append(x.journal.Entries, ch)
		if e = SaveJSON(x.pending, x.journal); e != nil {
			x.journal.Entries = x.journal.Entries[:len(x.journal.Entries)-1]
			return stopSuite{e}
		}
		if e = x.run.Backend.Set(ctx, id, ch.After); e != nil {
			return e
		}
		v, e = x.run.Backend.Get(ctx, id)
		if e != nil {
			return e
		}
		if v != ch.After {
			return fmt.Errorf("%s: настройка не вступила в силу", Label(id))
		}
		x.rep.Journal = append(x.rep.Journal, fmt.Sprintf("%s · %s · %d → %d, значение проверено", time.Now().UTC().Format(time.RFC3339), Label(id), ch.Before, ch.After))
	}
	return x.measure(ctx, name, group)
}
