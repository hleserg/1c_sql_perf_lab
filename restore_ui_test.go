package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hleserg/1c_sql_perf_lab/lab"
)

// Use real directory entries for the UI and drive SQL replies independently.
func backupFixtures(t *testing.T, root string) {
	t.Helper()
	dir := t.TempDir()
	for i, name := range []string{"DB_previous.bak", "DB_latest.BAK", "notes.txt"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(1600000000+int64(i)*60, 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	p := lab.Example()
	p.RestoreDir = dir
	if err := lab.SaveJSON(profilePath(root), p); err != nil {
		t.Fatal(err)
	}
}
func simulatedSQLPreview(t *testing.T, m ui) ui {
	t.Helper()
	x := m.restorePicker
	if x == nil || !x.loading || x.file == "" {
		t.Fatal("missing SQL metadata request")
	}
	x.cancel()
	p := x.profile
	p.ExpectedServer = "local-instance"
	p.Database = "selected-copy"
	changed, cmd := m.Update(restoreLoaded{picker: x, targets: []lab.LocalTarget{{Profile: p, Login: "sql-user"}}})
	m = changed.(ui)
	if cmd == nil || x.plan != nil || m.running {
		t.Fatal("missing preflight")
	}
	x.cancel()
	changed, _ = m.Update(restoreLoaded{picker: x, plan: &lab.RestorePlan{Path: x.file, Target: p.Database, Server: p.ExpectedServer}})
	return changed.(ui)
}
func TestRestorePickerRequiresPreviewAndSelectsNewest(t *testing.T) {
	m := newUI(t.TempDir())
	backupFixtures(t, m.root)
	m.tab = 4
	key := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
	a, load := m.Update(key("b"))
	m = a.(ui)
	if m.restorePicker == nil || load == nil {
		t.Fatal("missing backup picker")
	}
	a, _ = m.Update(load())
	m = a.(ui)
	if !strings.HasSuffix(m.restorePicker.files[0].Path, "DB_latest.BAK") || m.restorePicker.pos != 0 || len(m.restorePicker.files) != 2 || m.restorePicker.files[0].Modified.Before(m.restorePicker.files[1].Modified) {
		t.Fatal("default backup")
	}
	a, load = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = a.(ui)
	if m.running || load == nil {
		t.Fatal("restore started ahead of confirmation")
	}
	m = simulatedSQLPreview(t, m)
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

func TestMissingProfileDoesNotShowDemoFiles(t *testing.T) {
	m := newUI(t.TempDir())
	cmd := m.openRestore()
	if cmd == nil || !m.restorePicker.local || !m.restorePicker.auto || m.restorePicker.profile.RestoreFolder() != lab.DefaultRestoreDir {
		t.Fatal("first launch")
	}
	if strings.Contains(m.View(), "erp_demo_") || strings.Contains(m.View(), "Синтетические данные") {
		t.Fatal("fake entries")
	}
	m.restorePicker.cancel()
}
func TestSelectAndRememberDetectedStand(t *testing.T) {
	m := newUI(t.TempDir())
	cmd := m.openStandPicker(true)
	x := m.restorePicker
	if cmd == nil || x == nil {
		t.Fatal("manual setup on first launch")
	}
	x.cancel()
	p := lab.Example()
	p.ExpectedServer = "LOCAL-SQL"
	p.Database = "First"
	p.SQLProtocol = "lpc"
	q := p
	q.Database = "Second"
	changed, cmd := m.Update(restoreLoaded{picker: x, targets: []lab.LocalTarget{{Profile: p}, {Profile: q}}})
	m = changed.(ui)
	if cmd != nil || m.restorePicker == nil || !m.demo {
		t.Fatal("unconfirmed ambiguous target")
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyDown})
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	saved, auto, err := connectionProfile(m.root)
	if err != nil || auto || m.demo || saved.Database != "Second" || saved.ExpectedServer != q.ExpectedServer || newUI(m.root).demo {
		t.Fatal(saved, auto, err)
	}
}
