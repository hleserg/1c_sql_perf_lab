package main

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/hleserg/1c_sql_perf_lab/lab"
)

var (
	ice  = lipgloss.Color("#EDF7F6")
	mint = lipgloss.Color("#64E5C1")
	blue = lipgloss.Color("#82B8FF")
	plum = lipgloss.Color("#C5A3FF")
	gold = lipgloss.Color("#EDC082")
	red  = lipgloss.Color("#FF9D98")
	dim  = lipgloss.Color("#91A5AE")
	line = lipgloss.Color("#395461")
	deep = lipgloss.Color("#162A34")
)

func ink(s string, c lipgloss.Color) string { return lipgloss.NewStyle().Foreground(c).Render(s) }
func bold(s string, c lipgloss.Color) string {
	return lipgloss.NewStyle().Bold(true).Foreground(c).Render(s)
}
func fit(s string, n int) string {
	if n < 1 {
		return ""
	}
	return ansi.Truncate(s, n, "…")
}
func pad(s string, n int) string {
	s = fit(s, n)
	return s + strings.Repeat(" ", max(0, n-lipgloss.Width(s)))
}
func badge(s string, c lipgloss.Color) string {
	return lipgloss.NewStyle().Background(c).Foreground(lipgloss.Color("#10212A")).Bold(true).Render(" " + s + " ")
}

type note string
type done struct {
	r        lab.Report
	file     string
	err      error
	recovery bool
}
type streamMsg struct{ value any }
type tick time.Time
type ui struct {
	delivery                                 string
	jobPos                                   int
	chosenJob                                string
	liveProfile                              lab.Profile
	form                                     *profileForm
	root                                     string
	w, h, tab, planPos, catalogPos, phasePos int
	selected                                 map[int]bool
	demo, running, confirm, recovery         bool
	cancel                                   context.CancelFunc
	events                                   chan any
	current                                  lab.Event
	series                                   string
	points                                   []float64
	log                                      []string
	msg, file                                string
	report                                   lab.Report
	frame                                    int
	started                                  time.Time
}

var ids = []int{19, 24, 25, lab.ClusterID}

func newUI(root string) ui {
	return ui{root: root, w: 100, h: 30, demo: true, selected: map[int]bool{19: true}}
}
func (m ui) Init() tea.Cmd      { return nil }
func listen(c chan any) tea.Cmd { return func() tea.Msg { return streamMsg{<-c} } }
func pulse() tea.Cmd {
	return tea.Tick(140*time.Millisecond, func(t time.Time) tea.Msg { return tick(t) })
}
func (m *ui) begin() tea.Cmd {
	var picked []int
	for _, id := range ids {
		if m.selected[id] {
			picked = append(picked, id)
		}
	}
	job := m.chosenJob
	m.chosenJob = ""
	if job != "" {
		picked = nil
	}
	ctx, stop := context.WithCancel(context.Background())
	m.cancel = stop
	m.running = true
	m.confirm = false
	m.tab = 1
	m.report = lab.Report{}
	m.file = ""
	m.msg = ""
	m.log = nil
	m.points = nil
	m.delivery = ""
	m.series = ""
	m.current = lab.Event{}
	m.started = time.Now()
	ch := make(chan any, 32)
	m.events = ch
	root, demo, rec := m.root, m.demo, m.recovery
	go func() {
		defer stop()
		r, file, e := start(root, demo, picked, job, rec, func(v lab.Event) { ch <- v }, ctx)
		ch <- done{r, file, e, rec}
	}()
	return tea.Batch(listen(ch), pulse())
}
func (m ui) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.w = v.Width
		m.h = v.Height
	case tick:
		if m.running {
			m.frame++
			return m, pulse()
		}
	case note:
		m.msg = string(v)
	case streamMsg:
		switch t := v.value.(type) {
		case lab.Event:
			if t.Delivery {
				m.delivery = t.Detail
				return m, listen(m.events)
			}
			m.current = t
			if len(t.Values) > 0 {
				k := lab.SortedKeys(t.Values)[0]
				m.series = k
				m.points = append(m.points, t.Values[k])
				if len(m.points) > 32 {
					m.points = m.points[1:]
				}
			}
			if len(m.log) == 0 || m.log[len(m.log)-1] != t.Phase {
				m.log = append(m.log, t.Phase)
			}
			return m, listen(m.events)
		case done:
			m.running = false
			m.recovery = false
			m.delivery = t.r.Delivery
			m.report = t.r
			m.file = t.file
			m.phasePos = min(1, max(0, len(m.report.Phases)-1))
			if t.err != nil {
				m.msg = t.err.Error()
			} else if t.recovery {
				m.msg = "Исходные значения восстановлены"
			} else {
				m.msg = "Протокол сохранён"
			}
			m.tab = 2
		}
	case tea.KeyMsg:
		if m.form != nil {
			cmd := m.formKey(v)
			return m, cmd
		}
		key := v.String()
		if m.running {
			if key == "ctrl+c" || key == "esc" {
				m.cancel()
				m.msg = "Остановка · проверяем возврат настроек"
			}
			return m, nil
		}
		if m.confirm {
			switch key {
			case "enter":
				cmd := m.begin()
				return m, cmd
			case "esc", "q", "ctrl+c":
				m.confirm = false
				m.chosenJob = ""
				m.recovery = false
			}
			return m, nil
		}
		switch key {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "1":
			m.tab = 0
		case "2":
			m.tab = 1
		case "3":
			m.tab = 2
		case "4":
			m.tab = 3
		case "5":
			m.tab = 4
		case "tab", "right":
			m.tab = (m.tab + 1) % 5
		case "shift+tab", "left":
			m.tab = (m.tab + 4) % 5
		case "up", "k":
			m.move(-1)
		case "down", "j":
			m.move(1)
		case "pgup":
			m.move(-6)
		case "pgdown":
			m.move(6)
		case " ":
			if m.tab == 0 {
				id := ids[m.planPos]
				m.selected[id] = !m.selected[id]
			}
		case "e":
			f, e := formFor(profilePath(m.root))
			if e != nil {
				m.msg = e.Error()
				return m, openFile(profilePath(m.root), true)
			}
			m.form = f
			return m, nil
		case "d":
			m.demo = true
			m.msg = "Деморежим · подключение к серверу отключено"
		case "l":
			p, e := lab.Load(profilePath(m.root))
			if e != nil {
				m.msg = "Профиль стенда · " + e.Error() + "   [e] настроить"
			} else {
				m.demo = false
				m.liveProfile = p
				m.msg = p.Name + " · " + p.ExpectedServer + " / " + p.Database
			}
		case "r":
			if !m.demo {
				m.chosenJob = ""
				m.recovery = true
				m.confirm = true
			}
		case "h":
			r, f, e := lastReport(m.root)
			if e != nil {
				m.msg = e.Error()
			} else {
				m.report = r
				m.file = f
				m.phasePos = min(1, max(0, len(m.report.Phases)-1))
				m.tab = 2
				m.msg = "Последний сохранённый протокол"
			}
		case "o":
			if m.file != "" {
				return m, openFile(filepath.Join(filepath.Dir(m.file), "report.html"), false)
			}
		case "enter":
			m.chosenJob = ""
			if m.tab == 4 {
				m.chosenJob = lab.Jobs[m.jobPos].Key
			}
			if m.tab == 0 || m.tab == 1 || m.tab == 4 {
				if m.demo {
					cmd := m.begin()
					return m, cmd
				}
				m.confirm = true
			} else if m.tab == 3 {
				it := lab.All()[m.catalogPos]
				if ix := sqlJobIndex(it.ID); ix >= 0 && !lab.Runnable(it.ID) {
					m.jobPos = ix
					m.tab = 4
				} else if lab.Runnable(it.ID) {
					for i, id := range ids {
						if id == it.ID {
							m.planPos = i
						}
					}
					m.tab = 0
				}
			}
		}
	}
	return m, nil
}
func (m *ui) move(d int) {
	switch m.tab {
	case 4:
		m.jobPos = max(0, min(len(lab.Jobs)-1, m.jobPos+d))
	case 0:
		m.planPos = max(0, min(len(ids)-1, m.planPos+d))
	case 3:
		m.catalogPos = max(0, min(len(lab.All())-1, m.catalogPos+d))
	case 2:
		m.phasePos = max(0, min(len(m.report.Phases)+len(m.report.Sections)-1, m.phasePos+d))
	}
}
func spark(v []float64) string {
	if len(v) == 0 {
		return "· · · · · · · · · · · ·"
	}
	lo, hi := v[0], v[0]
	for _, p := range v {
		lo = math.Min(p, lo)
		hi = math.Max(p, hi)
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for _, p := range v {
		n := 3
		if hi > lo {
			n = int(math.Round((p - lo) * 7 / (hi - lo)))
		}
		b.WriteRune(blocks[n])
	}
	return b.String()
}
func panel(title string, body []string, w int, c lipgloss.Color) string {
	var s []string
	inside := max(8, w-4)
	head := " " + title + " "
	s = append(s, ink("╭─", line)+bold(head, c)+ink(strings.Repeat("─", max(0, w-3-lipgloss.Width(head)))+"╮", line))
	for _, row := range body {
		s = append(s, ink("│ ", line)+pad(row, inside)+ink(" │", line))
	}
	s = append(s, ink("╰"+strings.Repeat("─", max(0, w-2))+"╯", line))
	return strings.Join(s, "\n")
}
func wrap(s string, w int) []string {
	var rows []string
	var row string
	for _, word := range strings.Fields(s) {
		if row != "" && lipgloss.Width(row+" "+word) > w {
			rows = append(rows, row)
			row = ""
		}
		if row != "" {
			row += " "
		}
		row += word
	}
	if row != "" {
		rows = append(rows, row)
	}
	return rows
}
func (m ui) plan(w int) string {
	var rows []string
	for i, id := range ids {
		it := lab.ByID(id)
		mark := ink("○", dim)
		if m.selected[id] {
			mark = bold("●", mint)
		}
		cursor := "  "
		if m.planPos == i {
			cursor = bold("› ", mint)
		}
		rows = append(rows, cursor+mark+"  "+ink(lab.Label(id), dim)+"   "+bold(it.Title, ice), "")
	}
	rows = append(rows, ink("Пробел  выбрать     Enter  запустить", dim))
	leftW := w
	if w >= 76 {
		leftW = w * 53 / 100
	}
	left := panel("Эксперименты", rows, leftW, mint)
	id := ids[m.planPos]
	it := lab.ByID(id)
	rw := w
	if w >= 76 {
		rw = w - leftW - 1
	}
	n := 0
	for _, on := range m.selected {
		if on {
			n++
		}
	}
	rr := []string{ink(it.Group, plum), bold(it.Title, ice), ""}
	rr = append(rr, wrap(it.Check, rw-5)...)
	rr = append(rr, "", ink("Серии замеров", dim))
	switch n {
	case 0:
		rr = append(rr, "Только исходная серия")
	case 1:
		rr = append(rr, "Исходная → эксперимент", "→ контроль после отката")
	default:
		rr = append(rr, "Исходная → по одному → вместе", "→ контроль после отката")
	}
	for len(rr) < len(rows) {
		rr = append(rr, "")
	}
	right := panel("Протокол", rr, rw, blue)
	if w >= 76 {
		layout := lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
		if m.h >= 23 {
			p := m.liveProfile
			if m.demo {
				p = demoProfile()
			}
			foot := []string{bold(fmt.Sprintf("%d", n), mint) + ink("  выбрано", dim), ink("Повторы  ", dim) + bold(fmt.Sprintf("%d", p.Samples), blue), ink("Сеансы 1С  ", dim) + bold(fmt.Sprintf("%d", p.Sessions), plum)}
			summary := ""
			for i, k := range foot {
				if i > 0 {
					summary += ink("  │  ", line)
				}
				summary += k
			}
			layout += "\n\n  " + summary
		}
		return layout
	}
	return left + "\n" + ink(fit(it.Check, w), dim)
}
func (m ui) bench(w int) string {
	if !m.running && len(m.log) == 0 {
		return panel("Ход прогона", []string{"Выбери эксперименты на вкладке «План».", "", "Enter  начать с выбранными настройками"}, w, blue)
	}
	v := m.current
	spin := []string{"◐", "◓", "◑", "◒"}[m.frame%4]
	if !m.running {
		spin = "●"
	}
	barW := min(38, w-21)
	fill := 0
	if v.Total > 0 {
		fill = barW * v.Step / v.Total
	}
	rows := []string{bold(spin+"  "+v.Phase, mint), ink(fmt.Sprintf("%s    %d / %d", v.Detail, v.Step, v.Total), dim), ink(strings.Repeat("━", fill), mint) + ink(strings.Repeat("─", barW-fill), line), "", ink(m.series+" · мс", dim), bold(spark(m.points), blue), ""}
	if len(m.points) > 0 {
		rows = append(rows, bold(fmt.Sprintf("%.1f мс", m.points[len(m.points)-1]), ice)+"   "+ink(time.Since(m.started).Truncate(time.Second).String()+" с начала", dim))
	}
	for _, s := range m.log[max(0, len(m.log)-3):] {
		rows = append(rows, ink("· ", line)+s)
	}
	return panel("Телеметрия", rows, w, mint)
}
func (m ui) results(w int) string {
	if m.phasePos >= len(m.report.Phases) && len(m.report.Sections) > 0 {
		return m.sectionView(w)
	}
	if len(m.report.Phases) == 0 {
		return panel("Результаты", []string{"Здесь появится сравнение серий одного прогона.", "", "h  открыть последний протокол"}, w, blue)
	}
	idx := min(m.phasePos, len(m.report.Phases)-1)
	p := m.report.Phases[idx]
	rows := []string{ink(fmt.Sprintf("↑ ↓  Серия %d / %d", idx+1, len(m.report.Phases)), dim), bold(p.Name, ice), "", pad(ink("Операция", dim), w-32) + pad(ink("Медиана", dim), 13) + ink("Δ времени", dim)}
	for _, v := range lab.Rows(m.report) {
		if v.Phase != p.Name {
			continue
		}
		d := "—"
		c := dim
		if v.Compared {
			d = lab.Delta(v.Delta)
			if v.Delta < 0 {
				c = mint
			} else {
				c = gold
			}
		}
		rows = append(rows, pad(v.Name, w-32)+pad(fmt.Sprintf("%.1f мс", v.Median), 13)+bold(d, c))
	}
	rows = append(rows, "", ink("o  HTML-протокол и исходные измерения", dim))
	if m.report.Demo {
		rows = append(rows, ink("Демо · синтетические данные", plum))
	}
	return panel("Сравнение с исходной серией", rows, w, blue)
}
func (m ui) catalog(w int) string {
	all := lab.All()
	size := max(3, min(10, m.h-16))
	start := max(0, min(len(all)-size, m.catalogPos-size/2))
	var rows []string
	for i := start; i < min(start+size, len(all)); i++ {
		it := all[i]
		cur := "  "
		if i == m.catalogPos {
			cur = bold("› ", mint)
		}
		status := ink("карточка", dim)
		if sqlJobIndex(it.ID) >= 0 {
			status = ink("работа SQL", blue)
		}
		if lab.Runnable(it.ID) {
			status = ink("эксперимент", mint)
		}
		rows = append(rows, cur+ink(lab.Label(it.ID), dim)+"   "+pad(it.Title, w-26)+status)
	}
	rows = append(rows, "")
	rows = append(rows, wrap(all[m.catalogPos].Check, w-5)...)
	return panel("План мероприятий · 38 + кластер", rows, w, plum)
}
func (m ui) View() string {
	if m.w < 44 || m.h < 14 {
		return "\n " + bold("SQL PERF LAB", mint) + "\n Расширь терминал минимум до 44 × 14.\n q  выход"
	}
	w := min(m.w-4, 114)
	mode := badge("ДЕМО", plum)
	showLive := !m.demo
	if m.tab == 2 && (len(m.report.Phases) > 0 || m.report.Job != "") {
		showLive = !m.report.Demo
	}
	if showLive {
		mode = badge("СТЕНД", mint)
	}
	logo := bold("▌▌", mint) + " " + bold("SQL PERF LAB", ice) + ink("  /  1С", dim)
	signature := ink("▁▂▁▄▆▃▂▅▇▅▃▁", mint)
	header := pad(logo, w-lipgloss.Width(mode)-lipgloss.Width(signature)-3) + signature + "   " + mode
	nav := ""
	for i, t := range []string{"План", "Прогон", "Итог", "Каталог", "SQL"} {
		s := fmt.Sprintf(" %d %s ", i+1, t)
		if i == m.tab {
			nav += lipgloss.NewStyle().Background(deep).Foreground(mint).Bold(true).Render(s)
		} else {
			nav += ink(s, dim)
		}
	}
	var view string
	if m.form != nil {
		view = m.formView(w)
	} else if m.confirm {
		label := "Запустить эксперимент на стенде?"
		if j, ok := lab.FindJob(m.chosenJob); ok {
			label = j.Name
		}
		if m.recovery {
			label = "Восстановить настройки из журнала?"
		}
		target := ""
		if p, e := lab.Load(profilePath(m.root)); e == nil {
			target = p.ExpectedServer + " / " + p.Database
		}
		body := []string{bold(target, mint)}
		if j, ok := lab.FindJob(m.chosenJob); ok {
			body = append(body, wrap(j.Detail, w-5)...)
		}
		view = panel(fit(label, w-8), append(body, "", "Enter  выполнить      Esc  назад"), w, gold)
	} else {
		switch m.tab {
		case 0:
			view = m.plan(w)
		case 1:
			view = m.bench(w)
		case 2:
			view = m.results(w)
		case 3:
			view = m.catalog(w)
		case 4:
			view = m.jobsView(w)
		}
	}
	hints := "↑↓ выбор   Tab вкладка   e профиль   l стенд   d демо   q выход"
	if m.tab == 2 {
		hints = "↑↓ серия   o HTML   h история   Tab вкладка   q выход"
	}
	if !m.demo && m.tab == 0 {
		hints += "   r откат"
	}
	if m.running {
		hints = "Esc остановить с возвратом настроек"
	}
	if m.form != nil {
		hints = "↑↓ поле   Enter изменить   Ctrl+S сохранить   Esc назад"
		if m.form.editing {
			hints = "Enter принять   Ctrl+U очистить   Esc отменить ввод"
		}
	}
	if w < 50 {
		nav = fmt.Sprintf("%d / 5   %s    Tab →", m.tab+1, []string{"План", "Прогон", "Итог", "Каталог", "SQL"}[m.tab])
	}
	content := []string{header, "", nav, ink(strings.Repeat("─", w), line), view}
	if m.delivery != "" {
		content = append(content, ink(fit("GitHub · "+m.delivery, w), blue))
	}
	if m.msg != "" {
		content = append(content, wrap(m.msg, w)...)
	}
	content = append(content, ink(fit(hints, w), dim))
	raw := strings.Split(strings.Join(content, "\n"), "\n")
	if len(raw) > m.h-1 {
		raw = append(raw[:m.h-2], raw[len(raw)-1])
	}
	for i := range raw {
		raw[i] = "  " + raw[i]
	}
	return "\n" + strings.Join(raw, "\n")
}
