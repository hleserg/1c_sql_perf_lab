package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hleserg/1c_sql_perf_lab/feed"
	"github.com/hleserg/1c_sql_perf_lab/lab"
)

func TestMonitorRestartsAndQueuesOnlyCompletedDaysForConfiguredRepo(t *testing.T) {
	root := t.TempDir()
	p := lab.Example()
	dir := filepath.Join(root, "monitor", p.Key())
	cfg := feed.Config{ID: 41, Repo: "example/private"}
	first := time.Date(2026, 11, 24, 23, 46, 0, 0, time.UTC)
	today := first.Add(15 * time.Minute)
	store := func(at time.Time, reading *lab.MonitorReading, failure string) {
		t.Helper()
		if err := appendMonitorSample(dir, p, cfg, lab.MonitorSample{UTC: at, Reading: reading, Error: failure}); err != nil {
			t.Fatal(err)
		}
	}
	store(first.Add(-time.Minute), nil, "SQL недоступен")
	store(first, &lab.MonitorReading{DBReads: 11, SQLMemoryMiB: 22}, "")
	store(today, &lab.MonitorReading{DBReads: 15}, "")
	dayfile := filepath.Join(dir, first.Format(time.DateOnly)+".json")
	html, err := os.ReadFile(filepath.Join(dir, first.Format(time.DateOnly)+".html"))
	if err != nil || !strings.Contains(string(html), "с данными: 1 из 2") || !strings.Contains(string(html), "SQL недоступен") {
		t.Fatal("missing daily HTML readings", err)
	}
	d, err := readMonitorDay(dayfile)
	if err != nil || len(d.Samples) != 2 || d.Samples[0].Reading != nil || d.Samples[0].Error == "" || d.Samples[1].Reading.DBReads != 11 {
		t.Fatal(d, err)
	}
	if err := queueCompletedMonitorDays(context.Background(), root, dir, p, feed.Config{ID: 42}, today); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dayfile + ".queued"); !os.IsNotExist(err) {
		t.Fatal("previous destination leaked", err)
	}
	for i := 0; i < 2; i++ {
		if err := queueCompletedMonitorDays(context.Background(), root, dir, p, cfg, today); err != nil {
			t.Fatal(err)
		}
	}
	reports, err := filepath.Glob(filepath.Join(root, "local", "feed-queue", "41", "*", "report.json"))
	if err != nil || len(reports) != 1 {
		t.Fatal(reports, err)
	}
	raw, err := os.ReadFile(reports[0])
	if err != nil || !strings.Contains(string(raw), `"DBReads": 11`) || strings.Contains(string(raw), `"DBReads": 15`) {
		t.Fatal("unexpected daily feed", err)
	}
	if _, err := os.Stat(filepath.Join(dir, today.Format(time.DateOnly)+".json.queued")); !os.IsNotExist(err) {
		t.Fatal("unfinished day was queued")
	}
	if err := appendMonitorSample(dir, p, cfg, lab.MonitorSample{UTC: first}); err == nil {
		t.Fatal("rewrote immutable day after clock change")
	}
}

func TestMonitorPreservesUnreadableDay(t *testing.T) {
	p := lab.Example()
	dir := t.TempDir()
	s := lab.MonitorSample{UTC: time.Now().UTC()}
	path := filepath.Join(dir, s.UTC.Format(time.DateOnly)+".json")
	if err := os.WriteFile(path, []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := appendMonitorSample(dir, p, feed.Config{}, s); err == nil {
		t.Fatal("discarded existing report")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "incomplete" {
		t.Fatal(string(b), err)
	}
}

func TestMonitorCollectsImmediatelyAndStops(t *testing.T) {
	root := t.TempDir()
	p := lab.Example()
	// An ephemeral local listener closes the login. This produces a real failed
	// sample without connecting to any installed SQL Server.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	p.SQLHost = listener.Addr().String()
	p.SQLProtocol = "tcp"
	if err := lab.SaveJSON(profilePath(root), p); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runMonitor(ctx, root) }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		files, err := filepath.Glob(filepath.Join(root, "monitor", p.Key(), "*.html"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 1 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("monitor stopped before the first report: %v", err)
		case <-ctx.Done():
			t.Fatal("missing immediate sample", ctx.Err())
		case <-ticker.C:
		}
	}
	if err := runMonitor(ctx, root); err == nil || !strings.Contains(err.Error(), "уже запущен") {
		t.Fatal("concurrent monitor was allowed", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not honor cancellation")
	}
}

func TestMonitorCPUUsesCounterDeltasWithinSameSQLUptime(t *testing.T) {
	now := time.Now().UTC()
	d := monitorDay{Samples: []lab.MonitorSample{{UTC: now, Reading: &lab.MonitorReading{SQLStartedServerTime: "same", SQLCPUTimeMS: 100}}, {UTC: now.Add(time.Second), Reading: &lab.MonitorReading{SQLStartedServerTime: "same", SQLCPUTimeMS: 1600}}}}
	if d.AverageCores(0) != "—" || d.AverageCores(1) != "1.50" {
		t.Fatal("wrong CPU interval", d.AverageCores(1))
	}
	d.Samples[1].Reading.SQLStartedServerTime = "restarted"
	if d.AverageCores(1) != "—" {
		t.Fatal("CPU compared across a restart")
	}
}

func TestMonitorQueuesHealthyDayAfterCorruptReport(t *testing.T) {
	root := t.TempDir()
	p := lab.Example()
	dir := filepath.Join(root, "monitor", p.Key())
	cfg := feed.Config{ID: 41, Repo: "example/private"}
	healthy := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	if err := appendMonitorSample(dir, p, cfg, lab.MonitorSample{UTC: healthy, Error: "offline"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-11-23.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := queueCompletedMonitorDays(context.Background(), root, dir, p, cfg, healthy.Add(24*time.Hour)); err == nil {
		t.Fatal("missing error for corrupt report")
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-11-24.json.queued")); err != nil {
		t.Fatal("later report was blocked", err)
	}
}
