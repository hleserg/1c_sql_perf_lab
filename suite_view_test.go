package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/hleserg/1c_sql_perf_lab/lab"
	"strings"
	"testing"
)

func TestFullRunVisibleOnPlanAndRestoredResult(t *testing.T) {
	for _, size := range [][2]int{{44, 14}, {100, 22}, {170, 38}} {
		for _, tab := range []int{0, 2} {
			m := newUI(t.TempDir())
			m.w, m.h, m.tab, m.demo = size[0], size[1], tab, false
			m.report.Job = lab.RestoreReportTitle
			m.report.Restore = &lab.RestoreProgress{Stage: lab.RestoreReady, State: "ONLINE / MULTI_USER"}
			if !strings.Contains(ansi.Strip(m.View()), "[ Полный прогон ]") {
				t.Fatal(size, tab, m.View())
			}
			out, cmd := pointer(t, m, "[ Полный прогон ]", tea.MouseButtonLeft)
			if cmd == nil || !out.running || out.confirm || out.tab != 1 {
				t.Fatal("one click must start on saved stand", size, tab)
			}
			out.cancel()
		}
	}
}

func TestProfileInstanceTrialsCanBeToggledWithoutTyping(t *testing.T) {
	m := newUI(t.TempDir())
	f, err := formFor(profilePath(m.root))
	if err != nil {
		t.Fatal(err)
	}
	m.form = f
	f.pos = 26
	m.formKey(tea.KeyMsg{Type: tea.KeyEnter})
	f.pos = 28
	m.formKey(tea.KeyMsg{Type: tea.KeySpace})
	if f.editing {
		t.Fatal("switch opened text entry")
	}
	m.formKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	p, err := lab.Load(profilePath(m.root))
	if err != nil || !p.IsolatedInstanceTrials || !p.TestAdHocWorkloads || !m.liveProfile.TestAdHocWorkloads {
		t.Fatal(p, err)
	}
}

func TestERPRepairFromPlanAndOldFailedReport(t *testing.T) {
	for _, tab := range []int{0, 2} {
		for _, size := range [][2]int{{44, 14}, {148, 42}} {
			m := newUI(t.TempDir())
			m.demo, m.tab, m.w, m.h = false, tab, size[0], size[1]
			m.report = lab.Report{Coverage: []lab.ItemResult{{ID: 1, Title: "Мониторинг"}}, Workload: "1С · исходный замер не получен", Error: "RAS: refused. COM: 0x8002801d"}
			if tab == 2 && size[0] > 80 && !strings.Contains(ansi.Strip(m.View()), "библиотека типов COM") {
				t.Fatal("cause missing", m.View())
			}
			next, _ := pointer(t, m, "[ Исправить подключение 1С ]", tea.MouseButtonLeft)
			if !next.confirm || next.running || next.chosenJob != "erp-repair" {
				t.Fatal("incorrect repair action", size, tab)
			}
			out, _ := next.Update(tea.KeyMsg{Type: tea.KeyEsc})
			if out.(ui).confirm || out.(ui).running {
				t.Fatal("cancel started work")
			}
		}
	}
}
