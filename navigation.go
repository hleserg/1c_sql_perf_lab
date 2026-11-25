package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/hleserg/1c_sql_perf_lab/lab"
	"strings"
)

type focusArea int

const (
	menuArea focusArea = iota
	contentArea
	actionsArea
)

type hitTarget struct {
	kind  string
	index int
}
type hitPoint struct {
	x, y   int
	target hitTarget
}
type hitCanvas struct {
	probe            *hitPoint
	originX, originY int
}

func (m ui) offset(x, y int) ui {
	m.originX += x
	m.originY += y
	return m
}
func (m ui) mark(x, y, width int, kind string, index int) {
	p := m.probe
	if p != nil && p.y == m.originY+y && p.x >= m.originX+x && p.x < m.originX+x+width {
		p.target = hitTarget{kind, index}
	}
}
func (m ui) modal() bool {
	return m.update != nil || m.form != nil || m.restorePicker != nil || m.confirm || m.running
}

type control struct{ label, key string }

func (m ui) controls() []control {
	back := control{"Назад", "esc"}
	selectFile := control{"Выбрать копию", "b"}
	switch {
	case m.update != nil:
		if m.update.installing {
			return []control{{"Перезапуск…", "wait"}}
		}
		return []control{{"Отменить обновление", "esc"}}
	case m.restorePicker != nil:
		x := m.restorePicker
		switch {
		case x.loading:
			return []control{{"Отменить", "esc"}}
		case len(x.targets) > 0:
			return []control{{"Выбрать базу", "enter"}, back}
		case x.standOnly:
			return []control{{"Повторить поиск", "enter"}, {"Профиль", "e"}, back}
		case x.plan != nil:
			return []control{back, {"Восстановить базу", "enter"}}
		case x.manual:
			return []control{{"Проверить файл", "enter"}, {"Очистить", "ctrl+u"}, back}
		default:
			return []control{{"Проверить копию", "enter"}, {"Указать путь", "p"}, {"Обновить список", "r"}, back}
		}
	case m.form != nil:
		if m.form.editing {
			return []control{{"Принять", "enter"}, {"Очистить", "ctrl+u"}, {"Сохранить профиль", "ctrl+s"}, back}
		}
		return []control{{"Изменить", "enter"}, {"Сохранить профиль", "ctrl+s"}, back}
	case m.confirm:
		return []control{back, {"Подтвердить", "enter"}}
	case m.running:
		return []control{{"Запросить остановку", "esc"}}
	}
	all := control{"Полный прогон", "full"}
	if m.demo {
		all.label = "Демо-прогон"
	}
	var a []control
	switch m.tab {
	case 0:
		a = []control{all, {"Запустить", "run"}, selectFile}
	case 1:
		a = []control{{"Запустить", "enter"}}
	case 2:
		a = []control{all, {"Последний отчёт", "h"}}
		if len(m.report.Coverage) > 0 {
			toggle := control{"Данные замеров", "suite-results"}
			if m.suiteDetail {
				toggle.label = "Итог по пунктам"
			}
			a = []control{toggle, {"Открыть HTML", "o"}, all}
			if !m.demo && m.report.Workload == "1С · исходный замер не получен" {
				a = append([]control{{"Исправить подключение 1С", "erp-repair"}}, a...)
			}
			if m.suiteDetail {
				a = append(a, control{"Предыдущий", "prev"}, control{"Следующий", "next"})
			}
			if !m.demo {
				a = append(a, control{"Прогон через 1С", "erp-run"})
			}
			return append(a, control{"Выход", "q"})
		}
		if m.report.Job == lab.RestoreReportTitle && m.report.Error != "" {
			if m.h <= 20 {
				a = []control{selectFile, {"Обновить программу", "update"}}
				if m.file != "" {
					a = append(a, control{"Открыть HTML", "o"})
				}
				return a
			}
			a = append([]control{selectFile}, a...)
		}
		if len(m.report.Phases)+len(m.report.Sections) > 1 {
			a = append(a, control{"Предыдущий", "prev"}, control{"Следующий", "next"})
		}
		if m.file != "" {
			a = append(a, control{"Открыть HTML", "o"})
		}
	case 3:
		item := lab.All()[m.catalogPos].ID
		if lab.Runnable(item) || sqlJobIndex(item) >= 0 {
			a = []control{{"Открыть пункт", "enter"}}
		}
	case 4:
		a = []control{all, {"Выполнить", "enter"}, selectFile}
	}
	if !m.demo && (m.tab == 0 || m.tab == 4 || m.tab == 2) {
		a = append(a, control{"Прогон через 1С", "erp-run"})
	}
	if !m.demo && (m.tab == 0 || m.tab == 4) {
		a = append(a, control{"Исправить подключение 1С", "erp-repair"})
	}
	if !m.demo && (m.tab == 4 || m.tab == 2) {
		a = append(a, control{"Доступ к базе", "access-status"})
	}
	a = append(a, control{"Профиль", "e"})
	if m.demo {
		a = append(a, control{"На стенд", "l"})
	} else {
		a = append(a, control{"Деморежим", "d"}, control{"Вернуть настройки", "r"})
	}
	return append(a, control{"Обновить программу", "update"}, control{"Выход", "q"})
}

// Buttons wrap within the visible window. The same spans drive drawing and clicks.
func (m ui) actionRows(w int) []string {
	var rows []string
	line, x := "", 0
	type span struct{ x, y, width int }
	var spans []span
	for i, a := range m.controls() {
		label := "[ " + a.label + " ]"
		n := lipgloss.Width(label)
		if x > 0 && x+n > w {
			rows = append(rows, line)
			line = ""
			x = 0
		}
		if i == 1 && (m.confirm || m.restorePicker != nil && m.restorePicker.plan != nil) {
			gap := max(0, w-n-x)
			line += strings.Repeat(" ", gap)
			x += gap
		}
		spans = append(spans, span{x, len(rows), n})
		fg, bg := ice, titleBG
		if m.focus == actionsArea && m.action == i {
			fg, bg = canvas, mint
		}
		line += strip(n, label, fg, bg, true) + " "
		x += n + 1
	}
	rows = append(rows, strings.TrimRight(line, " "))
	for i, b := range spans {
		m.mark(1+b.x, m.h-len(rows)+b.y, b.width, "control", i)
	}
	return rows
}
func (m ui) hint(w int) string {
	var s string
	switch {
	case m.update != nil:
		if m.update.installing {
			return "Открываю новую версию…"
		}
		return "Esc отменить"
	case m.focus == actionsArea:
		s = "Кнопки: влево / вправо выбрать, Enter нажать"
	case m.modal():
		s = "Enter выбрать   Esc назад   Tab к кнопкам"
	case m.focus == contentArea && (m.tab == 1 || m.tab == 2) && m.report.Job == lab.RestoreReportTitle && !m.running:
		s = "Вверх/вниз или колесо — прокрутить   Tab к кнопкам   Esc в меню"
	case m.focus == menuArea:
		if rail, _ := m.workspaceWidth(); rail > 0 {
			s = "Вверх/вниз по меню   Enter открыть   Tab далее"
		} else {
			s = "Влево/вправо по меню   Enter открыть   Tab далее"
		}
	default:
		s = "Вверх/вниз по списку   Esc в меню   Tab к кнопкам"
		if m.tab == 0 {
			s = "Enter / Пробел отметить   Tab к кнопкам   Esc в меню"
		}
	}
	return fit(s, w)
}
func (m *ui) changeFocus(back bool) {
	if m.modal() {
		if m.focus == actionsArea {
			m.focus = contentArea
		} else {
			m.focus = actionsArea
		}
	} else {
		delta := 1
		if back {
			delta = 2
		}
		m.focus = (m.focus + focusArea(delta)) % 3
	}
	m.action = 0
}
func commandKey(key string) tea.KeyMsg {
	switch key {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
}
func (m ui) activate(index int) (tea.Model, tea.Cmd) {
	buttons := m.controls()
	if index < 0 || index >= len(buttons) {
		return m, nil
	}
	key := buttons[index].key
	m.focus, m.action = contentArea, 0
	switch key {
	case "access-status":
		if m.modal() || m.demo {
			return m, nil
		}
		m.chosenJob = "access-status"
		m.recovery = false
		cmd := m.begin()
		return m, cmd
	case "erp-repair":
		if m.demo || m.modal() {
			return m, nil
		}
		m.chosenJob, m.confirm, m.recovery = "erp-repair", true, false
		return m, nil
	case "full", "erp-run":
		if m.modal() {
			return m, nil
		}
		m.chosenJob = lab.FullJob.Key
		if key == "erp-run" {
			m.chosenJob = "erp"
		}
		m.recovery = false
		cmd := m.begin()
		return m, cmd
	case "suite-results":
		m.suiteDetail = !m.suiteDetail
		m.phasePos = 0
		return m, nil
	case "wait":
		return m, nil
	case "update":
		if m.modal() {
			return m, nil
		}
		cmd := m.startUpdate()
		return m, cmd
	case "run":
		return m.openSelection()
	case "prev":
		m.move(-1)
		return m, nil
	case "next":
		m.move(1)
		return m, nil
	}
	if key == "b" && !m.modal() {
		m.tab = 4
	}
	return m.Update(commandKey(key))
}
func (m ui) mouse(ev tea.MouseMsg) (tea.Model, tea.Cmd) {
	if ev.Action != tea.MouseActionPress || ev.X < 0 || ev.Y < 0 || ev.X >= m.w || ev.Y >= m.h || m.w < 44 || m.h < 14 {
		return m, nil
	}
	probe := hitPoint{x: ev.X, y: ev.Y}
	frame := m
	frame.probe = &probe
	frame.View()
	t := probe.target
	wheel := 0
	switch ev.Button {
	case tea.MouseButtonWheelUp:
		wheel = -1
	case tea.MouseButtonWheelDown:
		wheel = 1
	case tea.MouseButtonLeft:
	default:
		return m, nil
	}
	if wheel != 0 {
		switch t.kind {
		case "nav", "navarea":
			if !m.modal() {
				m.focus = menuArea
				m.tab = (m.tab + wheel + len(names)) % len(names)
			}
		case "control", "footer":
			m.focus = actionsArea
			count := len(m.controls())
			m.action = (m.action + wheel + count) % count
		case "":
			return m, nil
		default:
			m.focus = contentArea
			arrow := tea.KeyDown
			if wheel < 0 {
				arrow = tea.KeyUp
			}
			if m.form != nil && m.form.editing {
				return m, nil
			}
			return m.Update(tea.KeyMsg{Type: arrow})
		}
		return m, nil
	}
	switch t.kind {
	case "control":
		return m.activate(t.index)
	case "nav":
		if !m.modal() {
			m.tab = t.index
			m.focus = contentArea
		}
	case "experiment", "check":
		m.focus = contentArea
		m.planPos = t.index
		if t.kind == "check" {
			id := ids[t.index]
			m.selected[id] = !m.selected[id]
		}
	case "catalog":
		m.focus = contentArea
		m.catalogPos = t.index
	case "job":
		m.focus = contentArea
		m.jobPos = t.index
	case "coverage":
		m.suitePos = t.index
		m.focus = contentArea
		return m, nil
	case "series":
		m.focus = contentArea
		m.phasePos = t.index
	case "target":
		m.focus = contentArea
		m.restorePicker.targetPos = t.index
	case "file":
		m.focus = contentArea
		m.restorePicker.pos = t.index
	case "field":
		m.focus = contentArea
		f := m.form
		if f.editing && f.pos != t.index {
			f.fields[f.pos].value = f.buffer
			f.editing = false
		}
		if !f.editing {
			f.pos = t.index
			return m.Update(commandKey("enter"))
		}
	case "content":
		m.focus = contentArea
	case "navarea":
		if !m.modal() {
			m.focus = menuArea
		}
	}
	return m, nil
}
