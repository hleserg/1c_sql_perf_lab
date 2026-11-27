package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/hleserg/1c_sql_perf_lab/lab"
)

func TestOpeningPlanLoadsLastReportWithoutStartingWork(t *testing.T) {
	m := newUI(t.TempDir())
	r := lab.Report{Started: time.Now(), Demo: true, Error: "прогон прерван"}
	file := filepath.Join(m.root, "runs", "example", "report.json")
	if err := lab.SaveJSON(file, r); err != nil {
		t.Fatal(err)
	}
	loaded, _ := m.Update(m.Init()())
	m = loaded.(ui)
	if m.running || m.tab != 0 || m.file != file || !strings.Contains(ansi.Strip(m.View()), "ошибка · демо") {
		t.Fatal(m.View())
	}
	m, _ = pointer(t, m, "3 Итог ·", tea.MouseButtonLeft)
	if m.tab != 2 || m.report.Error != r.Error {
		t.Fatal("saved report not reachable from plan")
	}
}

func TestResizingKeepsModeAndCommandsInView(t *testing.T) {
	m := newUI(t.TempDir())
	for _, size := range [][2]int{{186, 52}, {80, 25}, {44, 14}, {136, 36}} {
		width, height := size[0], size[1]
		changed, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		m = changed.(ui)
		for tab := range names {
			m.tab = tab
			lines := strings.Split(ansi.Strip(m.View()), "\n")
			if len(lines) != height {
				t.Fatalf("%dx%d tab %d rendered %d lines", width, height, tab, len(lines))
			}
			if !strings.HasSuffix(strings.TrimRight(lines[0], " "), "ДЕМО") || ansi.StringWidth(lines[0]) != width {
				t.Fatalf("mode does not follow viewport at %dx%d", width, height)
			}
			if strings.TrimSpace(lines[height-1]) == "" || !strings.Contains(lines[2], "Команды") {
				t.Fatalf("no commands at %dx%d tab %d", width, height, tab)
			}
		}
	}
}

func TestCompactRestoreConfirmationScrollsLongPaths(t *testing.T) {
	m := newUI(t.TempDir())
	m.w, m.h = 44, 14
	plan := &lab.RestorePlan{
		Path:     "D:\\" + strings.Repeat("verylongfoldername\\", 20) + "complete.bak",
		Server:   "LAB",
		Target:   "test_copy",
		TargetID: 17,
		Source:   "source",
		Position: 1,
	}
	m.restorePicker = &restorePicker{plan: plan}
	first := ansi.Strip(m.View())
	if !strings.Contains(first, "LAB / test_copy") || !strings.Contains(first, "Будет заменена") {
		t.Fatal("restore target is not visible before confirmation")
	}
	for i := 0; i < 20; i++ {
		m = press(m, tea.KeyMsg{Type: tea.KeyPgDown})
	}
	last := ansi.Strip(m.View())
	if !strings.Contains(last, "набор 1") || m.running {
		t.Fatal("scrolling must reveal backup details without starting the restore")
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyPgUp})
	if m.restorePicker.scroll == 0 {
		return
	}
	if m.restorePicker.scroll >= len(restoreDetails(plan, m.w-4))-m.restorePageSize() {
		t.Fatal("cannot scroll back")
	}
}

func TestResultBadgeShowsTheCurrentExecutionMode(t *testing.T) {
	for _, demo := range []bool{true, false} {
		m := newUI(t.TempDir())
		m.w, m.h, m.tab, m.demo = 44, 14, 2, demo
		m.liveProfile = lab.Example()
		m.report = lab.Report{Started: time.Now(), Job: lab.RestoreReportTitle, Demo: !demo}
		mode := "СТЕНД"
		provenance := "Демонстрационный протокол"
		if demo {
			mode = "ДЕМО"
			provenance = "Протокол стенда"
		}
		view := ansi.Strip(m.View())
		if !strings.HasSuffix(strings.TrimSpace(strings.Split(view, "\n")[0]), mode) || !strings.Contains(view, provenance) {
			t.Fatal(view)
		}
	}
}

func TestRepeatedRunKeyNeverConfirmsOrStops(t *testing.T) {
	m := newUI(t.TempDir())
	m.demo = false
	m = press(m, tea.KeyMsg{Type: tea.KeyF5})
	m = press(m, tea.KeyMsg{Type: tea.KeyF5})
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.confirm || m.running {
		t.Fatal("repeated F5 confirmed live work")
	}
	m.confirm = false
	m.restorePicker = &restorePicker{plan: &lab.RestorePlan{}}
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.running || m.restorePicker == nil || m.restorePicker.plan == nil {
		t.Fatal("repeat Enter started restore")
	}
	m.restorePicker = nil
	m.confirm, m.running = false, true
	stopped := false
	m.cancel = func() { stopped = true }
	m = press(m, tea.KeyMsg{Type: tea.KeyF5})
	if stopped {
		t.Fatal("repeated F5 canceled a newly started run")
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyEsc})
	if !stopped {
		t.Fatal("explicit cancellation unavailable")
	}
}

func TestRunProfileKeepsTheDisplayedDatabase(t *testing.T) {
	root := t.TempDir()
	shown := lab.Example()
	shown.Database = "shown"
	disk := shown
	disk.Database = "newer"
	if err := lab.SaveJSON(profilePath(root), disk); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		demo     bool
		snapshot *lab.Profile
		want     string
	}{
		{false, &shown, shown.Database}, {false, nil, disk.Database}, {true, &shown, demoProfile().Database},
	} {
		profile, err := runProfile(root, tc.demo, tc.snapshot)
		if err != nil || profile.Database != tc.want {
			t.Fatal(profile, err)
		}
	}
}
