package main

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hleserg/1c_sql_perf_lab/lab"
)

func TestRestorePickerRequiresPreviewAndSelectsNewest(t *testing.T) {
	m := newUI(t.TempDir())
	m.tab = 4
	key := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
	a, load := m.Update(key("b"))
	m = a.(ui)
	if m.restorePicker == nil || load == nil {
		t.Fatal("missing backup picker")
	}
	a, _ = m.Update(load())
	m = a.(ui)
	if m.restorePicker.pos != 0 || len(m.restorePicker.files) != 2 || m.restorePicker.files[0].Modified.Before(m.restorePicker.files[1].Modified) {
		t.Fatal("default backup")
	}
	a, load = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = a.(ui)
	if m.running || load == nil {
		t.Fatal("restore started ahead of confirmation")
	}
	a, _ = m.Update(load())
	m = a.(ui)
	if m.restorePicker.plan == nil || m.restorePicker.plan.Path != m.restorePicker.files[0].Path {
		t.Fatal("wrong plan")
	}
	a, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = a.(ui)
	if m.running || m.restorePicker == nil || m.restorePicker.plan != nil {
		t.Fatal("cancel must return to file selection")
	}
	// A cancelled asynchronous reply must not revive an old picker.
	stale := m.restorePicker
	a, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = a.(ui)
	a, _ = m.Update(restoreLoaded{picker: stale, files: []lab.BackupFile{{Path: "other", Modified: time.Now()}}})
	if a.(ui).restorePicker != nil {
		t.Fatal("stale reply")
	}
}
