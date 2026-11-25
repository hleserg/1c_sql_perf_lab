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
