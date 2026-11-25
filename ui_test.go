package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/hleserg/1c_sql_perf_lab/lab"
	"strings"
	"testing"
)

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
