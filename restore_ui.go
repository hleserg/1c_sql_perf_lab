package main

import (
	"context"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/hleserg/1c_sql_perf_lab/lab"
	"net"
	"os"
	"strings"
	"time"
)

func availableSQLJobs() []lab.Job { return append(append([]lab.Job(nil), lab.Jobs...), lab.RestoreJob) }

type restorePicker struct {
	profile                                 lab.Profile
	auto, local, loading, manual, standOnly bool
	targets                                 []lab.LocalTarget
	targetPos                               int
	file, login, busy                       string
	files                                   []lab.BackupFile
	pos, scroll                             int
	path, err                               string
	plan                                    *lab.RestorePlan
	cancel                                  context.CancelFunc
}
type restoreLoaded struct {
	picker  *restorePicker
	targets []lab.LocalTarget
	files   []lab.BackupFile
	plan    *lab.RestorePlan
	err     error
}

// A generated example is a template, not an identified SQL Server.
func connectionProfile(root string) (lab.Profile, bool, error) {
	p, err := lab.Load(profilePath(root))
	if os.IsNotExist(err) {
		p = lab.Example()
		p.ERPDatabase = ""
		return p, true, nil
	}
	if err != nil {
		return p, false, err
	}
	ex := lab.Example()
	template := p.SQLProtocol == "" && p.SQLHost == ex.SQLHost && p.Instance == "" && strings.EqualFold(p.ExpectedServer, ex.ExpectedServer)
	if template && p.ERPDatabase == ex.ERPDatabase {
		p.ERPDatabase = ""
	}
	return p, template, nil
}
func isDiscoveredLocal(p lab.Profile) bool {
	host := p.SQLHost
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	return (p.SQLProtocol == "lpc" || p.SQLProtocol == "tcp") && (strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback()))
}
func (m *ui) openRestore() tea.Cmd { return m.openStandPicker(false) }
func (m *ui) openStandPicker(standOnly bool) tea.Cmd {
	p, auto, err := connectionProfile(m.root)
	if err != nil {
		m.msg = "Профиль подключения: " + err.Error()
		return nil
	}
	m.focus = contentArea
	if standOnly && !auto {
		m.liveProfile = p
		m.demo = false
		m.msg = p.ExpectedServer + " / " + p.Database
		return nil
	}
	m.confirm = false
	m.recovery = false
	m.chosenJob = ""
	m.msg = ""
	x := &restorePicker{profile: p, auto: auto, local: auto || isDiscoveredLocal(p), standOnly: standOnly}
	m.restorePicker = x
	if standOnly {
		return x.discover("")
	}
	return x.request("")
}
func (x *restorePicker) load(label string, read func(context.Context) restoreLoaded) tea.Cmd {
	x.busy = label
	x.loading = true
	x.scroll = 0
	x.err = ""
	ctx, stop := context.WithTimeout(context.Background(), 60*time.Second)
	x.cancel = stop
	return func() tea.Msg { defer stop(); r := read(ctx); r.picker = x; return r }
}
func (x *restorePicker) request(path string) tea.Cmd {
	if path != "" {
		x.file = path
		if x.auto {
			return x.discover(path)
		}
		return x.preview()
	}
	x.file = ""
	p, local := x.profile, x.local
	return x.load("Чтение папки с резервными копиями…", func(ctx context.Context) restoreLoaded {
		var files []lab.BackupFile
		var err error
		if local {
			files, err = lab.LocalBackups(p.RestoreFolder())
		} else {
			files, err = lab.ListBackups(ctx, p, false)
		}
		return restoreLoaded{files: files, err: err}
	})
}
func (x *restorePicker) discover(path string) tea.Cmd {
	p := x.profile
	return x.load("Поиск локального SQL Server и доступных баз…", func(ctx context.Context) restoreLoaded {
		targets, err := lab.DiscoverLocalSQL(ctx, p, path)
		return restoreLoaded{targets: targets, err: err}
	})
}
func (x *restorePicker) preview() tea.Cmd {
	p, path := x.profile, x.file
	return x.load("SQL проверяет выбранную копию…", func(ctx context.Context) restoreLoaded {
		plan, err := lab.PrepareRestore(ctx, p, path, false)
		return restoreLoaded{plan: &plan, err: err}
	})
}
func (m *ui) useStand(x *restorePicker) error {
	if x.auto {
		saved, auto, err := connectionProfile(m.root)
		if err != nil {
			return err
		}
		if !auto && (saved.Key() != x.profile.Key() || saved.SQLHost != x.profile.SQLHost || saved.Instance != x.profile.Instance) {
			return fmt.Errorf("Профиль стенда изменился. Открой выбор заново.")
		}
		if auto {
			saved.SQLHost, saved.Instance, saved.ExpectedServer, saved.Database = x.profile.SQLHost, x.profile.Instance, x.profile.ExpectedServer, x.profile.Database
			saved.TrustServerCertificate, saved.SQLProtocol = x.profile.TrustServerCertificate, x.profile.SQLProtocol
			if err = lab.SaveJSON(profilePath(m.root), saved); err != nil {
				return fmt.Errorf("сохранение подключения: %w", err)
			}
			x.profile = saved
		}
	}
	m.liveProfile = x.profile
	m.demo = false
	return nil
}
func (m *ui) chooseRestoreTarget() tea.Cmd {
	x := m.restorePicker
	t := x.targets[x.targetPos]
	x.profile = t.Profile
	x.login = t.Login
	x.manual = false
	if x.standOnly {
		if err := m.useStand(x); err != nil {
			x.err = err.Error()
			return nil
		}
		m.restorePicker = nil
		m.msg = "Стенд: " + t.Profile.ExpectedServer + " / " + t.Profile.Database + " · " + t.Login
		return nil
	}
	x.targets = nil
	return x.preview()
}
func (m *ui) restoreKey(k tea.KeyMsg) tea.Cmd {
	x := m.restorePicker
	switch k.String() {
	case "esc", "ctrl+c":
		if x.cancel != nil {
			x.cancel()
		}
		if x.loading || x.standOnly || !x.manual && x.plan == nil && len(x.targets) == 0 {
			m.restorePicker = nil
		} else {
			x.plan = nil
			x.targets = nil
			x.manual = false
			x.err = ""
		}
		return nil
	}
	if x.loading {
		return nil
	}
	if len(x.targets) > 0 {
		switch k.String() {
		case "down", "j":
			x.targetPos = min(len(x.targets)-1, x.targetPos+1)
		case "up", "k":
			x.targetPos = max(0, x.targetPos-1)
		case "enter":
			return m.chooseRestoreTarget()
		}
		return nil
	}
	if x.standOnly {
		switch k.String() {
		case "enter", "r":
			return x.discover("")
		case "e":
			m.restorePicker = nil
			f, err := formFor(profilePath(m.root))
			if err != nil {
				m.msg = err.Error()
			} else {
				m.form = f
			}
		}
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
			if err := m.useStand(x); err != nil {
				x.err = err.Error()
				return nil
			}
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
		rows = wrap(x.busy, w-5)
	case len(x.targets) > 0:
		title = "Выбери базу стенда"
		rows = wrap("Найдены подключения на этом компьютере. Выбери, с какой базой работать.", w-5)
		size := max(2, m.h-13)
		start := max(0, min(len(x.targets)-size, x.targetPos-size/2))
		for i := start; i < min(len(x.targets), start+size); i++ {
			t := x.targets[i]
			label := t.Profile.ExpectedServer + " / " + t.Profile.Database
			if t.New {
				label += " · создать"
			} else if t.SourceMatch {
				label += " · имя из копии"
			}
			m.mark(0, len(rows)+1, w, "target", i)
			rows = append(rows, activeRow(w-3, label, i == x.targetPos))
		}
	case x.standOnly:
		title = "Подключение к стенду"
		rows = wrap("Повтори поиск или укажи другой SQL Server в профиле.", w-5)
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
		where := "Сервер SQL: " + x.profile.ExpectedServer + " / " + x.profile.Database
		if x.local {
			where = "Файлы на этом компьютере"
		}
		rows = append(rows, bold(where, ice), ink(x.profile.RestoreFolder()+"   свежие сверху", dim), "")
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
