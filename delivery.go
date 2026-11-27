package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hleserg/1c_sql_perf_lab/feed"
	"github.com/hleserg/1c_sql_perf_lab/lab"
	"os"
	"time"
)

func start(root string, demo bool, snapshot *lab.Profile, ids []int, job string, plan *lab.RestorePlan, recoverOnly bool, emit func(lab.Event), ctx context.Context) (lab.Report, string, error) {
	meta := map[string]any{"demo": demo, "items": ids, "job": job, "recovery": recoverOnly}
	return deliverRun(root, meta, emit, func(progress func(lab.Event)) (lab.Report, string, error) {
		return runLab(root, demo, snapshot, ids, job, plan, recoverOnly, progress, ctx)
	})
}

// Standalone trials write the same JSON report format as the interactive run.
func deliverCommand(root, job string, run func(func(string)) (string, error)) error {
	show := func(e lab.Event) { fmt.Println(e.Detail) }
	_, path, err := deliverRun(root, map[string]any{"job": job}, show, func(progress func(lab.Event)) (lab.Report, string, error) {
		dest, runErr := run(func(detail string) { progress(lab.Event{Phase: job, Detail: detail}) })
		var report lab.Report
		if dest != "" {
			data, readErr := os.ReadFile(dest)
			if readErr == nil {
				readErr = json.Unmarshal(data, &report)
			}
			if readErr != nil {
				return report, "", errors.Join(runErr, readErr)
			}
		}
		return report, dest, runErr
	})
	if path != "" {
		fmt.Println(path)
	}
	return err
}

func deliverRun(root string, meta map[string]any, emit func(lab.Event), run func(func(lab.Event)) (lab.Report, string, error)) (lab.Report, string, error) {
	cfg, e := feed.Load(root)
	if e != nil {
		return lab.Report{}, "", e
	}
	if cfg.Repo == "" {
		return run(emit)
	}
	s, e := feed.Begin(root, cfg)
	if e != nil {
		return lab.Report{}, "", e
	}
	link := "https://github.com/" + cfg.Repo + "/tree/HEAD/runs/" + s.ID
	status := func(msg string) {
		if emit != nil {
			emit(lab.Event{Phase: "GitHub", Detail: msg, Delivery: true})
		}
	}
	var recordErr error
	record := func(k string, v any) {
		if err := s.Event(k, v); err != nil {
			recordErr = err
		}
	}
	record("start", meta)
	status("Очередь · " + link)
	worker, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			bounded, stop := context.WithTimeout(worker, 13*time.Second)
			syncErr := feed.Sync(bounded, root, cfg)
			stop()
			if worker.Err() != nil {
				return
			}
			if syncErr != nil {
				status("Журнал сохранён в очереди. Следующая попытка через 15 с · " + syncErr.Error())
			} else {
				status("Отправлено · " + s.ID)
			}
			select {
			case <-worker.Done():
				return
			case <-ticker.C:
				if err := s.Event("heartbeat", map[string]string{"status": "running"}); err != nil {
					status(err.Error())
				}
			}
		}
	}()
	result, path, runErr := run(func(v lab.Event) {
		record("progress", v)
		if emit != nil {
			emit(v)
		}
	})
	cancel()
	<-finished
	end := map[string]any{"success": runErr == nil}
	if runErr != nil {
		end["error"] = runErr.Error()
	}
	record("finish", end)
	if path != "" {
		if err := s.Report(result); err != nil {
			recordErr = err
		}
	}
	final, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	syncErr := feed.Sync(final, root, cfg)
	result.Delivery = link
	if syncErr != nil {
		result.Delivery = "Журнал ожидает отправки · " + syncErr.Error() + ". Повтор: perf-lab.exe -sync"
	}
	if recordErr != nil {
		result.Delivery += " · Ошибка локальной очереди: " + recordErr.Error()
	}
	status(result.Delivery)
	return result, path, runErr
}
func deliveryCLI(root, repo string, syncOnly bool) error {
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Minute)
	defer stop()
	if repo != "" {
		if e := feed.Configure(ctx, root, repo); e != nil {
			return e
		}
		fmt.Println("Настройка сохранена ·", repo)
	}
	if syncOnly {
		cfg, e := feed.Load(root)
		if e != nil {
			return e
		}
		if cfg.Repo == "" {
			return errors.New("сначала выполни -github owner/repo")
		}
		if e = feed.Sync(ctx, root, cfg); e != nil {
			return e
		}
		fmt.Fprintln(os.Stdout, "Очередь отправлена · https://github.com/"+cfg.Repo)
	}
	return nil
}
