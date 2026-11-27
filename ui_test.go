package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/hleserg/1c_sql_perf_lab/lab"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnreadableStandProfileExplainsDemoMode(t *testing.T) {
	root := t.TempDir()
	path := profilePath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	m := newUI(root)
	if !m.demo || !strings.Contains(m.msg, "Не удалось прочитать профиль") {
		t.Fatal(m.msg)
	}
}

func press(m ui, k tea.KeyMsg) ui { changed, _ := m.Update(k); return changed.(ui) }
func TestKeyboardWorkflow(t *testing.T) {
	m := newUI(t.TempDir())
	m = press(m, tea.KeyMsg{Type: tea.KeyDown})
	m = press(m, tea.KeyMsg{Type: tea.KeySpace})
	if !m.selected[24] {
		t.Fatal("selection")
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if m.form == nil {
		t.Fatal("form")
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = press(m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m = press(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Test Stand")})
	m = press(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	p, e := lab.Load(profilePath(m.root))
	if e != nil || p.Name != "Test Stand" {
		t.Fatal(p, e)
	}
	p.ExpectedServer = "TEST-SQL"
	if err := lab.SaveJSON(profilePath(m.root), p); err != nil {
		t.Fatal(err)
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	m = press(m, tea.KeyMsg{Type: tea.KeyTab})
	for i := 0; i < len(m.controls()) && m.controls()[m.action].key != "run"; i++ {
		m = press(m, tea.KeyMsg{Type: tea.KeyRight})
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.confirm || m.running {
		t.Fatal("live stand preview missing")
	}
}
func TestScreenSize(t *testing.T) {
	for _, w := range []int{44, 64, 80, 108, 140} {
		m := newUI(t.TempDir())
		m.w = w
		for tab := 0; tab < 5; tab++ {
			m.tab = tab
			for _, line := range strings.Split(m.View(), "\n") {
				if ansi.StringWidth(line) > w {
					t.Errorf("width %d, tab %d: %s", w, tab, line)
				}
			}
		}
	}
}

func TestPortfolioProfileRoundTrip(t *testing.T) {
	m := newUI(t.TempDir())
	p := lab.Example()
	p.ERPDocuments = []string{"ДокументА", "ДокументБ"}
	p.ERPPostDocuments = []string{"ДокументА"}
	if err := lab.SaveJSON(profilePath(m.root), p); err != nil {
		t.Fatal(err)
	}
	form, err := formFor(profilePath(m.root))
	if err != nil {
		t.Fatal(err)
	}
	if form.fields[29].value != "ДокументА, ДокументБ" {
		t.Fatal(form.fields[29])
	}
	form.fields[29].value = "ДокументА, ДокументВ, "
	form.fields[30].value = ""
	p, err = form.profile()
	if err != nil || strings.Join(p.ERPDocuments, ",") != "ДокументА,ДокументВ" || len(p.ERPPostDocuments) != 0 {
		t.Fatal(p, err)
	}
}
