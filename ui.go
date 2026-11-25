package main

import (
	"context"
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
	bodyRows int
	update   *updateTask
	hitCanvas
	focus                                    focusArea
	action                                   int
	sized                                    bool
	restorePicker                            *restorePicker
	restorePlan                              *lab.RestorePlan
	restoring                                bool
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
	m := ui{root: root, w: 100, h: 30, demo: true, focus: contentArea, selected: map[int]bool{19: true}}
	if p, auto, err := connectionProfile(root); err == nil && !auto {
		m.demo = false
		m.liveProfile = p
	}
	return m
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
	plan := m.restorePlan
	m.restorePlan = nil
	m.restoring = plan != nil
	job := m.chosenJob
	m.chosenJob = ""
	if job != "" {
		picked = nil
	}
	ctx, stop := context.WithCancel(context.Background())
	m.cancel = stop
	m.running = true
	m.focus = contentArea
	m.confirm = false
	m.tab = 1
	m.phasePos = 0
	m.report = lab.Report{}
	m.file = ""
	m.msg = ""
	m.log = nil
	m.points = nil
	m.delivery = ""
	m.series = ""
	m.current = lab.Event{}
	m.started = time.Now()
	if plan != nil {
		m.report = lab.Report{Job: lab.RestoreReportTitle, Started: m.started, Demo: m.demo,
			Inventory: map[string]string{"База": plan.Target, "SQL": plan.Server, "Файл бэкапа": plan.Path}, Restore: &lab.RestoreProgress{Stage: lab.RestorePrepare}}
	}
	ch := make(chan any, 32)
	m.events = ch
	root, demo, rec := m.root, m.demo, m.recovery
	go func() {
		defer stop()
		r, file, e := start(root, demo, picked, job, plan, rec, func(v lab.Event) { ch <- v }, ctx)
		ch <- done{r, file, e, rec}
	}()
	return tea.Batch(listen(ch), pulse())
}
func (m ui) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case updateTick:
		if m.update == v.task && !v.task.installing {
			return m, updatePulse(v.task)
		}
		return m, nil
	case updateFetched:
		if m.update != v.task {
			v.stage.cleanup()
			return m, nil
		}
		m.update.cancel()
		if v.err != nil {
			m.update = nil
			m.msg = "Обновление не установлено. Проверь доступ к GitHub и нажми «Обновить программу» ещё раз. " + v.err.Error()
			return m, nil
		}
		if v.stage.dir == "" {
			m.update = nil
			m.msg = "Установлена последняя версия · " + v.stage.tag
			return m, nil
		}
		m.update.installing = true
		m.msg = "Перезапуск · " + v.stage.tag
		return m, func() tea.Msg {
			err := handoffUpdate(v.stage, v.target, m.root)
			if err != nil {
				v.stage.cleanup()
			}
			return updateHandoff{err}
		}
	case updateHandoff:
		if v.err != nil {
			m.update = nil
			m.msg = "Перезапуск не выполнен. Закрой другие окна Perf Lab и повтори обновление. " + v.err.Error()
			return m, nil
		}
		return m, tea.Quit
	case restoreLoaded:
		if m.restorePicker == v.picker {
			x := m.restorePicker
			x.loading = false
			if v.err != nil {
				x.err = v.err.Error()
				if x.login != "" {
					x.err = "SQL-вход " + x.login + " · " + x.err
				}
				if x.file == "" && !x.standOnly {
					x.files = nil
					x.pos = 0
				}
			} else if len(v.targets) > 0 {
				x.targets = v.targets
				x.targetPos = 0
				if len(v.targets) == 1 {
					cmd := m.chooseRestoreTarget()
					return m, cmd
				}
			} else if v.plan != nil {
				x.plan = v.plan
				x.manual = false
			} else {
				x.files = v.files
				x.pos = 0
			}
		}
	case tea.WindowSizeMsg:
		m.w = v.Width
		m.h = v.Height
		if !m.sized {
			if rail, _ := m.workspaceWidth(); rail > 0 && !m.modal() {
				m.focus = menuArea
			}
			m.sized = true
		}
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
			if t.Restore != nil {
				m.report.Restore = t.Restore
			}
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
			if m.restoring && t.r.Job == "" && t.err != nil {
				// Opening the run directory or loading the profile can fail before Execute starts.
				delivery := t.r.Delivery
				t.r = m.report
				t.r.Delivery = delivery
				t.r.Finished = time.Now()
				t.r.Error = t.err.Error()
			}
			m.running = false
			m.restoring = false
			m.recovery = false
			m.delivery = t.r.Delivery
			m.report = t.r
			m.file = t.file
			m.phasePos = min(1, max(0, len(m.report.Phases)-1))
			if t.err != nil {
				if t.r.Job == lab.RestoreReportTitle && t.r.Error == t.err.Error() {
					m.msg = ""
				} else {
					if m.report.Error == "" {
						m.report.Error = t.err.Error()
					}
					m.msg = runNextStep(t.err.Error(), t.file != "") + " " + t.err.Error()
				}
			} else if t.recovery {
				m.msg = "Исходные значения восстановлены"
			} else {
				m.msg = "Протокол сохранён"
			}
			m.tab = 2
			m.focus = contentArea
			m.action = 0
		}
	case tea.MouseMsg:
		if m.update != nil && (v.Action != tea.MouseActionPress || v.Button != tea.MouseButtonLeft || v.Y != m.h-1) {
			return m, nil
		}
		return m.mouse(v)
	case tea.KeyMsg:
		key := v.String()
		if m.update != nil {
			if !m.update.installing && (key == "esc" || key == "ctrl+c") {
				m.update.cancel()
				m.update = nil
				m.msg = "Обновление отменено"
			}
			return m, nil
		}
		switch key {
		case "tab", "shift+tab":
			m.changeFocus(key == "shift+tab")
			return m, nil
		}
		if m.focus == actionsArea {
			count := len(m.controls())
			switch key {
			case "enter", " ":
				return m.activate(m.action)
			case "right", "down":
				m.action = (m.action + 1) % count
				return m, nil
			case "left", "up":
				m.action = (m.action + count - 1) % count
				return m, nil
			case "home":
				m.action = 0
				return m, nil
			case "end":
				m.action = count - 1
				return m, nil
			case "esc":
				m.focus = contentArea
				return m, nil
			}
		}
		if m.restorePicker != nil {
			cmd := m.restoreKey(v)
			return m, cmd
		}
		if m.form != nil {
			cmd := m.formKey(v)
			return m, cmd
		}
		if m.running {
			if key == "ctrl+c" || key == "esc" {
				m.cancel()
				if m.restoring {
					m.msg = "Отмена проверки запрошена. Начатое восстановление завершится."
				} else {
					m.msg = "Остановка · проверяем возврат настроек"
				}
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
		rail, _ := m.workspaceWidth()
		if m.focus == menuArea {
			switch {
			case key == "enter" || (rail > 0 && key == "right") || (rail == 0 && key == "down"):
				m.focus = contentArea
				return m, nil
			case (rail > 0 && (key == "up" || key == "k")) || (rail == 0 && key == "left"):
				m.tab = (m.tab + len(names) - 1) % len(names)
				return m, nil
			case (rail > 0 && (key == "down" || key == "j")) || (rail == 0 && key == "right"):
				m.tab = (m.tab + 1) % len(names)
				return m, nil
			case key == "home":
				m.tab = 0
				return m, nil
			case key == "end":
				m.tab = len(names) - 1
				return m, nil
			case key == "up" || key == "left" || key == " " || key == "pgup" || key == "pgdown":
				return m, nil
			}
		} else if key == "esc" || (rail > 0 && key == "left") {
			m.focus = menuArea
			return m, nil
		}
		switch key {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "1", "2", "3", "4", "5":
			m.tab = int(key[0] - '1')
			m.focus = contentArea
		case "b":
			if m.tab == 4 {
				m.focus = contentArea
				cmd := m.openRestore()
				return m, cmd
			}
		case "right":
			if rail > 0 {
				return m, nil
			}
			m.tab = (m.tab + 1) % len(names)
		case "left":
			m.tab = (m.tab + len(names) - 1) % len(names)
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
			m.focus = contentArea
			m.form = f
			return m, nil
		case "d":
			m.demo = true
			m.msg = "Деморежим · подключение к серверу отключено"
		case "l":
			cmd := m.openStandPicker(true)
			return m, cmd
		case "r":
			if !m.demo {
				m.chosenJob = ""
				m.focus = contentArea
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
				m.focus = contentArea
				m.msg = "Последний сохранённый протокол"
			}
		case "o":
			if m.file != "" {
				return m, openFile(filepath.Join(filepath.Dir(m.file), "report.html"), false)
			}
		case "enter":
			if m.tab == 0 {
				id := ids[m.planPos]
				m.selected[id] = !m.selected[id]
				return m, nil
			}
			return m.openSelection()
		}
	}
	return m, nil
}
func (m ui) openSelection() (tea.Model, tea.Cmd) {
	m.chosenJob = ""
	if m.tab == 4 {
		m.chosenJob = availableSQLJobs()[m.jobPos].Key
		if m.chosenJob == "restore" {
			cmd := m.openRestore()
			return m, cmd
		}
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
	return m, nil
}
func (m *ui) move(d int) {
	if !m.running && (m.tab == 1 || m.tab == 2) && m.report.Job == lab.RestoreReportTitle {
		m.scrollRestoreReport(d)
		return
	}
	switch m.tab {
	case 4:
		m.jobPos = max(0, min(len(availableSQLJobs())-1, m.jobPos+d))
	case 0:
		m.planPos = max(0, min(len(ids)-1, m.planPos+d))
	case 3:
		m.catalogPos = max(0, min(len(lab.All())-1, m.catalogPos+d))
	case 2:
		m.phasePos = max(0, min(len(m.report.Phases)+len(m.report.Sections)-1, m.phasePos+d))
	}
}
