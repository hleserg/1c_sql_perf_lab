package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/hleserg/1c_sql_perf_lab/lab"
)

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
			if strings.TrimSpace(lines[height-1]) == "" {
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
