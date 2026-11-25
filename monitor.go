package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/hleserg/1c_sql_perf_lab/feed"
	"github.com/hleserg/1c_sql_perf_lab/lab"
)

const monitorInterval = 15 * time.Minute

type monitorDay struct {
	DateUTC                           string
	Profile, ExpectedServer, Database string
	IntervalMinutes                   int
	DeliveryRepoID                    int64 `json:",omitempty"`
	Samples                           []lab.MonitorSample
}

func runMonitor(ctx context.Context, root string) error {
	p, err := lab.Load(profilePath(root))
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "monitor", p.Key())
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(dir, "monitor.lock"))
	ok, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("мониторинг этого SQL и базы уже запущен")
	}
	defer lock.Unlock()
	fmt.Printf("Мониторинг SQL · первый замер сейчас, далее каждые 15 минут до Ctrl+C. Суточные отчёты UTC: %s\n", dir)
	ticker := time.NewTicker(monitorInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		sample := lab.ReadMonitorSample(ctx, p)
		if ctx.Err() != nil {
			break
		}
		cfg, cfgErr := feed.Load(root)
		if cfgErr != nil {
			fmt.Fprintln(os.Stderr, "Настройки доставки:", cfgErr)
			cfg = feed.Config{}
		}
		if err = appendMonitorSample(dir, p, cfg, sample); err != nil {
			return err
		}
		fmt.Printf("%s · замер записан\n", sample.UTC.Format(time.RFC3339))
		if sample.Error != "" {
			fmt.Fprintln(os.Stderr, sample.Error)
		}
		if cfg.Repo != "" {
			if err = queueCompletedMonitorDays(ctx, root, dir, p, cfg, sample.UTC); err != nil {
				fmt.Fprintln(os.Stderr, "Очередь суточных отчётов:", err)
			}
			syncCtx, stop := context.WithTimeout(ctx, 30*time.Second)
			err = feed.Sync(syncCtx, root, cfg)
			stop()
			if err != nil && ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, "Доставка в GitHub:", err)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
	return nil
}

func readMonitorDay(path string) (d monitorDay, err error) {
	b, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(b, &d)
	}
	return
}

func appendMonitorSample(dir string, p lab.Profile, cfg feed.Config, s lab.MonitorSample) error {
	date := s.UTC.UTC().Format(time.DateOnly)
	path := filepath.Join(dir, date+".json")
	if _, err := os.Stat(path + ".queued"); err == nil {
		return fmt.Errorf("отчёт за %s уже закрыт, проверь часы стенда", date)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	d, err := readMonitorDay(path)
	if errors.Is(err, os.ErrNotExist) {
		d = monitorDay{DateUTC: date, Profile: p.Name, ExpectedServer: p.ExpectedServer, Database: p.Database,
			IntervalMinutes: int(monitorInterval / time.Minute), DeliveryRepoID: cfg.ID}
	} else if err != nil {
		return err
	} else if !strings.EqualFold(d.ExpectedServer, p.ExpectedServer) || !strings.EqualFold(d.Database, p.Database) || d.DateUTC != date {
		return errors.New("в суточном отчёте другой SQL, база или дата")
	}
	// A mixed-destination day stays local, including when delivery was
	// disabled for part of the observation period.
	if d.DeliveryRepoID != cfg.ID {
		d.DeliveryRepoID = 0
	}
	d.Samples = append(d.Samples, s)
	if err := lab.SaveJSON(path, d); err != nil {
		return err
	}
	return writeMonitorHTML(dir, d)
}

func queueCompletedMonitorDays(ctx context.Context, root, dir string, p lab.Profile, cfg feed.Config, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	today := now.UTC().Format(time.DateOnly)
	var failures []error
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		day := strings.TrimSuffix(entry.Name(), ".json")
		if _, err = time.Parse(time.DateOnly, day); err != nil || day >= today {
			continue
		}
		if err := queueMonitorDayFile(root, dir, p, cfg, day); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", day, err))
		}
	}
	return errors.Join(failures...)
}

func queueMonitorDayFile(root, dir string, p lab.Profile, cfg feed.Config, day string) error {
	path := filepath.Join(dir, day+".json")
	if _, err := os.Stat(path + ".queued"); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	report, err := readMonitorDay(path)
	if err != nil {
		return err
	}
	if report.DeliveryRepoID != cfg.ID || cfg.ID == 0 {
		return nil
	}
	if report.DateUTC != day || !strings.EqualFold(report.ExpectedServer, p.ExpectedServer) || !strings.EqualFold(report.Database, p.Database) {
		return errors.New("в суточном отчёте другая дата, экземпляр или база")
	}
	if err = feed.QueueMonitorDay(root, cfg, p.Key(), day, report); err != nil {
		return err
	}
	return lab.SaveJSON(path+".queued", cfg.ID)
}
