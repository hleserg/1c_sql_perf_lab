package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/hleserg/1c_sql_perf_lab/lab"
	"strings"
	"testing"
)

// Locate controls on the visible screen so tests catch layout/hit-region drift.
func pointAt(t *testing.T, m ui, label string) (int, int) {
	t.Helper()
	for y, row := range strings.Split(ansi.Strip(m.View()), "\n") {
		if n := strings.Index(row, label); n >= 0 {
			return ansi.StringWidth(row[:n]) + 1, y
		}
	}
	t.Fatalf("missing %q in %dx%d\n%s", label, m.w, m.h, ansi.Strip(m.View()))
	return 0, 0
}
func pointer(t *testing.T, m ui, label string, button tea.MouseButton) (ui, tea.Cmd) {
	t.Helper()
	x, y := pointAt(t, m, label)
	changed, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Button: button, Action: tea.MouseActionPress})
	return changed.(ui), cmd
}
func TestArrowNavigationAndActions(t *testing.T) {
	m := newUI(t.TempDir())
	changed, _ := m.Update(tea.WindowSizeMsg{Width: 144, Height: 35})
	m = changed.(ui)
	key := func(k tea.KeyType) { m = press(m, tea.KeyMsg{Type: k}) }
	key(tea.KeyDown)
	if m.tab != 1 || m.focus != menuArea {
		t.Fatal("vertical menu")
	}
	key(tea.KeyEnd)
	key(tea.KeyRight)
	key(tea.KeyDown)
	if m.tab != 4 || m.jobPos != 1 || m.focus != contentArea {
		t.Fatal("SQL content")
	}
	key(tea.KeyLeft)
	key(tea.KeyHome)
	key(tea.KeyEnter)
	key(tea.KeyDown)
	key(tea.KeyEnter)
	if !m.selected[24] || m.running {
		t.Fatal("selecting an experiment started the run")
	}
	key(tea.KeyTab)
	for m.controls()[m.action].key != "e" {
		key(tea.KeyRight)
	}
	key(tea.KeyEnter)
	if m.form == nil {
		t.Fatal("open profile without letters")
	}
	key(tea.KeyTab)
	key(tea.KeyRight)
	key(tea.KeyEnter)
	if m.form != nil {
		t.Fatal("save profile without letters")
	}
	changed, _ = m.Update(tea.WindowSizeMsg{Width: 60, Height: 22})
	m = changed.(ui)
	key(tea.KeyEsc)
	key(tea.KeyRight)
	key(tea.KeyDown)
	if m.focus != contentArea || m.tab != 1 {
		t.Fatal("horizontal menu")
	}
}
func TestMouseUsesVisibleControls(t *testing.T) {
	for _, s := range [][2]int{{44, 14}, {80, 25}, {144, 35}} {
		m := newUI(t.TempDir())
		m.w, m.h = s[0], s[1]
		m, _ = pointer(t, m, "[x]", tea.MouseButtonLeft)
		if m.selected[19] {
			t.Fatal("checkbox")
		}
		m, _ = pointer(t, m, "Исправления", tea.MouseButtonLeft)
		if m.planPos != 1 || m.selected[24] {
			t.Fatal("row should only select")
		}
		m, _ = pointer(t, m, "Исправления", tea.MouseButtonWheelDown)
		if m.planPos != 2 {
			t.Fatal("wheel")
		}
		m, _ = pointer(t, m, "[ Профиль ]", tea.MouseButtonLeft)
		m, _ = pointer(t, m, "Название", tea.MouseButtonLeft)
		if m.form == nil || !m.form.editing || m.form.pos != 0 {
			t.Fatal("profile field")
		}
		m, _ = pointer(t, m, "[ Сохранить профиль ]", tea.MouseButtonLeft)
		if m.form != nil {
			t.Fatal("save")
		}
		if s[0] > 110 {
			m, _ = pointer(t, m, "Работы SQL", tea.MouseButtonLeft)
		} else {
			row := strings.Split(ansi.Strip(m.View()), "\n")[1]
			i := strings.LastIndex(row, "SQL")
			if i < 0 {
				t.Fatal(row)
			}
			next, _ := m.Update(tea.MouseMsg{X: ansi.StringWidth(row[:i]), Y: 1, Button: tea.MouseButtonLeft})
			m = next.(ui)
		}
		if m.tab != 4 {
			t.Fatal("section click")
		}
		m, _ = pointer(t, m, "Обновить", tea.MouseButtonLeft)
		if m.jobPos == 0 || m.running || m.confirm {
			t.Fatal("SQL selection")
		}
		backupFixtures(t, m.root)
		m, load := pointer(t, m, "[ Выбрать копию ]", tea.MouseButtonLeft)
		if load == nil {
			t.Fatal("picker")
		}
		loaded, _ := m.Update(load())
		m = loaded.(ui)
		m, _ = pointer(t, m, "prev", tea.MouseButtonLeft)
		if m.restorePicker.pos != 1 || m.running {
			t.Fatal("backup click")
		}
		m, load = pointer(t, m, "[ Проверить копию ]", tea.MouseButtonLeft)
		m = simulatedSQLPreview(t, m)
		if m.restorePicker.plan == nil || m.running {
			t.Fatal("preview")
		}
		m, _ = pointer(t, m, "[ Назад ]", tea.MouseButtonLeft)
		if m.restorePicker.plan != nil || m.running {
			t.Fatal("back without restore")
		}
	}
}
func TestConfirmationIsSeparateFromSelection(t *testing.T) {
	m := newUI(t.TempDir())
	for _, width := range []int{44, 58, 80, 144} {
		m.w = width
		m.restorePicker = &restorePicker{}
		x, y := pointAt(t, m, "[ Проверить копию ]")
		m.restorePicker.plan = &lab.RestorePlan{}
		confirmX, confirmY := pointAt(t, m, "[ Восстановить базу ]")
		if y == confirmY && x+ansi.StringWidth("Проверить копию ]") >= confirmX {
			t.Fatal("overlapping buttons", width)
		}
		changed, _ := m.Update(tea.MouseMsg{X: confirmX, Y: confirmY, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
		if changed.(ui).running {
			t.Fatal("mouse release started restore")
		}
	}
}
