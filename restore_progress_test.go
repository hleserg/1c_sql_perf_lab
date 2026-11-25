package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/hleserg/1c_sql_perf_lab/lab"
)

func oldVerifyTestReport() lab.Report {
	start := time.Date(2026, 1, 2, 12, 30, 0, 0, time.UTC)
	return lab.Report{
		Job: lab.RestoreReportTitle, Demo: true, Started: start, Finished: start.Add(12 * time.Second),
		Inventory: map[string]string{"База": "demo_copy", "SQL": "DEMO-SQL", "Файл бэкапа": `D:\Demo\daily_example.bak`},
		Journal:   []string{"Проверка носителя RESTORE VERIFYONLY · daily_example.bak", "Проверка копии · 11%", "Проверка копии · 32%"},
		Error:     "проверка копии до отключения сеансов: Read: Pipe IO timed out waiting for server",
	}
}

func TestHistoricalRestoreFailureAcrossViewports(t *testing.T) {
	m := newUI(t.TempDir())
	m.report, m.tab, m.file = oldVerifyTestReport(), 2, "report.json"
	m.delivery = "Журнал отправлен"
	for _, size := range [][2]int{{44, 14}, {80, 25}, {123, 32}, {190, 48}} {
		m.w, m.h = size[0], size[1]
		screen := ansi.Strip(m.View())
		for _, v := range []string{"Проверка копии прервана", "32%", "Запись базы", "Сеансы не отключались", "00:12", "Обновить программу"} {
			if !strings.Contains(screen, v) {
				t.Fatalf("%v missing %q:\n%s", size, v, screen)
			}
		}
		if strings.Contains(screen, "Серий замеров") || strings.Contains(screen, "100%") {
			t.Fatal(screen)
		}
		for _, line := range strings.Split(screen, "\n") {
			if ansi.StringWidth(line) > m.w {
				t.Fatalf("overflow at %v: %s", size, line)
			}
		}
	}
}

func TestProgressEventBeforeFirstSampleAndAfterFailure(t *testing.T) {
	m := newUI(t.TempDir())
	m.w, m.h, m.running, m.restoring, m.tab = 120, 30, true, true, 1
	m.report = lab.Report{Job: lab.RestoreReportTitle, Started: time.Now(), Restore: &lab.RestoreProgress{Stage: lab.RestoreVerify}}
	screen := ansi.Strip(m.View())
	if !strings.Contains(screen, "ждём SQL") || strings.Contains(screen, "0%") {
		t.Fatal(screen)
	}
	zero := 0
	progress := lab.RestoreProgress{Stage: lab.RestoreVerify, Verify: lab.RestoreMeter{Percent: &zero}}
	changed, _ := m.Update(streamMsg{lab.Event{Restore: &progress}})
	m = changed.(ui)
	if !strings.Contains(ansi.Strip(m.View()), "0%") {
		t.Fatal(m.View())
	}
	progress.Stage = lab.RestoreWrite
	progress.Verify.Complete = true
	r := lab.Report{Job: lab.RestoreReportTitle, Restore: &progress, Error: "Read: Pipe IO timed out"}
	changed, _ = m.Update(streamMsg{done{r: r, err: errors.New(r.Error)}})
	m = changed.(ui)
	screen = ansi.Strip(m.View())
	for _, v := range []string{"Восстановление прервано", "Состояние базы не подтверждено", "активные RESTORE"} {
		if !strings.Contains(screen, v) {
			t.Fatalf("missing %q: %s", v, screen)
		}
	}
	if strings.Contains(screen, "Сеансы не отключались") || m.msg != "" {
		t.Fatal(screen)
	}
}

func TestRestoreAdviceByCauseAndStage(t *testing.T) {
	for _, c := range []struct {
		stage            lab.RestoreStage
		state, err, next string
	}{
		{lab.RestoreVerify, "", "Operating system error 5(Access is denied.)", "права службы SQL Server"},
		{lab.RestoreVerify, "", "Operating system error 2(The system cannot find the file specified.)", "пути"},
		{lab.RestorePrepare, "", "permission denied", "учётную запись"},
		{lab.RestoreRecheck, "", "not enough disk space", "свободное место"},
		{lab.RestoreWrite, "RESTORING", "Read: Pipe IO timed out", "продолжается ли RESTORE"},
		{lab.RestoreExclusive, "", "не удалось получить монопольный доступ", "активные RESTORE"},
	} {
		advice := failureAdvice(lab.RestoreProgress{Stage: c.stage, State: c.state}, c.err, false)
		if !strings.Contains(advice.next, c.next) {
			t.Errorf("%q: %q", c.err, advice.next)
		}
	}
}

func TestRestorePreflightFailureKeepsJob(t *testing.T) {
	m := newUI(t.TempDir())
	m.report = lab.Report{Job: lab.RestoreReportTitle, Restore: &lab.RestoreProgress{Stage: lab.RestorePrepare}}
	m.running, m.restoring = true, true
	changed, _ := m.Update(streamMsg{done{err: errors.New("cannot load profile")}})
	m = changed.(ui)
	if m.report.Job != lab.RestoreReportTitle || m.report.Error == "" || m.tab != 2 {
		t.Fatal(m.report)
	}
}
