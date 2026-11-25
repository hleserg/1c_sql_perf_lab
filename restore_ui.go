package main

import (
	"context"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/hleserg/1c_sql_perf_lab/lab"
	"strings"
	"time"
)

func availableSQLJobs() []lab.Job { return append(append([]lab.Job(nil), lab.Jobs...), lab.RestoreJob) }

type restorePicker struct {
	profile               lab.Profile
	demo, loading, manual bool
	files                 []lab.BackupFile
	pos, scroll           int
	path, err             string
	plan                  *lab.RestorePlan
	cancel                context.CancelFunc
}
type restoreLoaded struct {
	picker *restorePicker
	files  []lab.BackupFile
	plan   *lab.RestorePlan
	err    error
}

func (m *ui) openRestore() tea.Cmd {
	p := demoProfile()
	if !m.demo {
		var e error
		p, e = lab.Load(profilePath(m.root))
		if e != nil {
			m.msg = e.Error()
			return nil
		}
	}
	m.confirm = false
	m.recovery = false
	m.msg = ""
	m.restorePicker = &restorePicker{profile: p, demo: m.demo}
	return m.restorePicker.request("")
}
func (x *restorePicker) request(path string) tea.Cmd {
	x.loading = true
	x.scroll = 0
	x.err = ""
	p, demo := x.profile, x.demo
	ctx, stop := context.WithTimeout(context.Background(), 60*time.Second)
	x.cancel = stop
	return func() tea.Msg {
		defer stop()
		if path == "" {
			files, e := lab.ListBackups(ctx, p, demo)
			return restoreLoaded{picker: x, files: files, err: e}
		}
		plan, e := lab.PrepareRestore(ctx, p, path, demo)
		return restoreLoaded{picker: x, plan: &plan, err: e}
	}
}
func (m *ui) restoreKey(k tea.KeyMsg) tea.Cmd {
	x := m.restorePicker
	switch k.String() {
	case "esc", "ctrl+c":
		if x.cancel != nil {
			x.cancel()
		}
		if x.loading || !x.manual && x.plan == nil {
			m.restorePicker = nil
		} else {
			x.plan = nil
			x.manual = false
			x.err = ""
		}
		return nil
	}
	if x.loading {
		return nil
	}
	if x.plan != nil {
		_, inner := m.workspaceWidth()
		all := restoreDetails(x.plan, inner)
		maxScroll := max(0, len(all)-m.restorePageSize())
		switch k.String() {
		case "up", "k":
			x.scroll = max(0, x.scroll-1)
		case "down", "j":
			x.scroll = min(x.scroll+1, maxScroll)
		case "pgup":
			x.scroll = max(0, x.scroll-max(1, m.h-7))
		case "pgdown":
			x.scroll = min(maxScroll, x.scroll+max(1, m.h-7))
		case "enter":
			m.restorePlan = x.plan
			m.chosenJob = "restore"
			m.restorePicker = nil
			return m.begin()
		}
		return nil
	}
	if x.manual {
		switch k.String() {
		case "enter":
			p := strings.Trim(strings.TrimSpace(x.path), "\"")
			if p != "" {
				return x.request(p)
			}
		case "ctrl+u":
			x.path = ""
		case "backspace", "ctrl+h":
			r := []rune(x.path)
			if len(r) > 0 {
				x.path = string(r[:len(r)-1])
			}
		default:
			if k.Type == tea.KeyRunes {
				x.path += string(k.Runes)
			} else if k.Type == tea.KeySpace {
				x.path += " "
			}
		}
		return nil
	}
	switch k.String() {
	case "up", "k":
		x.pos = max(0, x.pos-1)
	case "down", "j":
		x.pos = max(0, min(len(x.files)-1, x.pos+1))
	case "pgdown":
		x.pos = max(0, min(len(x.files)-1, x.pos+8))
	case "pgup":
		x.pos = max(0, x.pos-8)
	case "p", "P":
		x.manual = true
		x.path = ""
		x.err = ""
	case "r":
		return x.request("")
	case "enter":
		if len(x.files) > 0 {
			return x.request(x.files[x.pos].Path)
		} else {
			x.manual = true
		}
	}
	return nil
}
func restoreDetails(p *lab.RestorePlan, w int) []string {
	action := "Будет создана база"
	if p.TargetID != 0 {
		action = "Будет заменена база"
	}
	rows := wrap(action+" "+p.Server+" / "+p.Target, w-5)
	if p.TargetID != 0 {
		rows = append(rows, wrap("Сеансы этой базы будут отключены. Текущие данные будут заменены.", w-5)...)
	}
	rows = append(rows, "", ink("Файл резервной копии", blue))
	rows = append(rows, wrap(p.Path, w-5)...)
	rows = append(rows, wrap(fmt.Sprintf("Полная копия %s, %s, набор %d", p.Source, p.Finished, p.Position), w-5)...)
	return rows
}
func (m ui) restorePageSize() int {
	// Leave room for the caption, navigation, and actions, including after a resize.
	return max(3, m.h-7)
}
func (m ui) restoreView(w int) string {
	x := m.restorePicker
	title := "Выбор резервной копии"
	var rows []string
	switch {
	case x.loading:
		rows = []string{"SQL читает сведения о копиях..."}
	case x.plan != nil:
		title = "Подтвердить восстановление"
		rows = restoreDetails(x.plan, w)
		size := m.restorePageSize()
		start := min(x.scroll, max(0, len(rows)-size))
		if len(rows) > size {
			title += fmt.Sprintf("    %d-%d / %d", start+1, start+size, len(rows))
			rows = rows[start : start+size]
		}
		return panel(title, rows, w, gold)
	case x.manual:
		title = "Полный путь к резервной копии"
		rows = wrap("Введите путь на сервере SQL, включая имя .bak.", w-5)
		path := wrap(x.path+"|", w-5)
		n := max(2, m.h-10)
		rows = append(rows, path[max(0, len(path)-n):]...)
	default:
		rows = append(rows, bold(x.profile.ExpectedServer+" / "+x.profile.Database, ice), ink(x.profile.RestoreFolder()+"   свежие сверху", dim), "")
		n := max(2, m.h-11)
		start := max(0, min(len(x.files)-n, x.pos-n/2))
		for i := start; i < min(len(x.files), start+n); i++ {
			f := x.files[i]
			pos := strings.LastIndexAny(f.Path, "/\\")
			label := f.Modified.Format("02.01.2006 15:04") + "  " + f.Path[pos+1:]
			m.mark(0, len(rows)+1, w, "file", i)
			rows = append(rows, activeRow(w-3, label, i == x.pos))
		}
		if len(x.files) == 0 && x.err == "" {
			rows = append(rows, "Копий в папке нет. Нажми «Указать путь».")
		}
		if len(x.files) > 0 && m.h >= 22 {
			rows = append(rows, "", ink("Полный путь выбранного файла", blue))
			rows = append(rows, wrap(x.files[x.pos].Path, w-5)...)
		}
	}
	return panel(title, rows, w, blue)
}
