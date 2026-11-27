package main

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
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

func (m ui) confirmation() bool {
	return m.confirm || m.restorePicker != nil && m.restorePicker.plan != nil
}
func (m ui) busy() bool {
	return m.running || m.update != nil || m.restorePicker != nil && m.restorePicker.loading
}

type control struct{ label, key string }

func (m ui) controls() []control {
	back := control{"Назад", "esc"}
	selectFile := control{"Выбрать копию", "b"}
	if m.demo {
		selectFile.label = "Копии на стенде"
	}
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
			return []control{{"Восстановить базу", "confirm-action"}, back}
		case x.manual:
			return []control{{"Проверить файл", "enter"}, {"Очистить", "ctrl+u"}, back}
		default:
			return []control{{"Проверить копию", "enter"}, {"Указать путь", "p"}, {"Обновить список", "r"}, back}
		}
	case m.form != nil:
		if m.form.editing {
			return []control{{"Сохранить профиль", "ctrl+s"}, {"Принять", "enter"}, {"Очистить", "ctrl+u"}, back}
		}
		return []control{{"Сохранить профиль", "ctrl+s"}, {"Изменить", "enter"}, back}
	case m.confirm:
		return []control{{"Подтвердить", "confirm-action"}, back}
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
		label := "Замерить выбранные"
		if len(m.selectedIDs()) == 0 {
			label = "Исходный замер"
		}
		a = []control{{label, "run"}, all, selectFile}
	case 1:
		a = []control{{"Запустить", "enter"}}
	case 2:
		last := control{"Последний отчёт", "h"}
		html := control{"Открыть HTML", "o"}
		switch {
		case len(m.report.Coverage) > 0:
			toggle := control{"Данные замеров", "suite-results"}
			if m.suiteDetail {
				toggle.label = "Итог по пунктам"
			}
			if !m.demo && failedERP(m.report) {
				a = append(a, control{"Исправить подключение 1С", "erp-repair"})
			}
			a = append(a, toggle)
			if m.file != "" {
				a = append(a, html)
			}
			a = append(a, last, all)
		case m.report.Job == lab.RestoreReportTitle && m.report.Demo == m.demo &&
			(m.report.Inventory["База"] == "" || strings.EqualFold(m.report.Inventory["База"], m.profile().Database)) &&
			(m.report.Inventory["SQL"] == "" || strings.EqualFold(m.report.Inventory["SQL"], m.profile().ExpectedServer)):
			if m.report.Error != "" {
				a = append(a, selectFile)
			}
			a = append(a, all)
			if m.file != "" {
				a = append(a, html)
			}
			a = append(a, last)
		default:
			if m.file != "" {
				a = append(a, html)
			}
			a = append(a, last, all)
		}
		if (m.suiteDetail || len(m.report.Coverage) == 0) && len(m.report.Phases)+len(m.report.Sections) > 1 {
			a = append(a, control{"Предыдущий", "prev"}, control{"Следующий", "next"})
		}

	case 3:
		item := lab.All()[m.catalogPos].ID
		if lab.Runnable(item) || sqlJobIndex(item) >= 0 {
			a = []control{{"Открыть пункт", "enter"}}
		}
	case 4:
		run := control{"Выполнить", "enter"}
		if m.demo && availableSQLJobs()[m.jobPos].Key == "restore" {
			run.label = selectFile.label
		}
		a = []control{run, all}
		if availableSQLJobs()[m.jobPos].Key != "restore" {
			a = append(a, selectFile)
		}
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

// The command row and overlay share the same controls as keyboard and mouse dispatch.
func (m ui) commandRow(w int) string {
	actions := m.controls()
	primary := actions[0].label
	more := " F2 Команды "
	if m.focus == actionsArea {
		more = " F2 Закрыть "
		start := w - ansi.StringWidth(more)
		m.mark(start, 0, ansi.StringWidth(more), "palette", 0)
		return between(w, ink("  Команды", mint), strip(ansi.StringWidth(more), more, ice, railBG, false))
	}
	if m.busy() {
		status := "Идёт прогон"
		switch {
		case m.update != nil:
			status = "Обновление"
		case m.restorePicker != nil:
			status = m.restorePicker.busy
		case m.restoring:
			status = "Восстановление"
		}
		m.mark(w-ansi.StringWidth(more), 0, ansi.StringWidth(more), "palette", 0)
		return between(w, ink(status, mint), strip(ansi.StringWidth(more), more, ice, railBG, false))
	}
	available := w - ansi.StringWidth(more) - 2
	if available < 30 && actions[0].key == "erp-repair" {
		primary = "Исправить вход 1С"
	}
	shortcut := "F5"
	if m.confirmation() {
		shortcut = "F8"
	}
	label := fit(" "+shortcut+"  "+primary+" ", available)
	first, second := strip(ansi.StringWidth(label), label, canvas, mint, true), strip(ansi.StringWidth(more), more, ice, railBG, false)
	if m.confirmation() {
		m.mark(0, 0, ansi.StringWidth(more), "palette", 0)
		m.mark(w-ansi.StringWidth(label), 0, ansi.StringWidth(label), "control", 0)
		return between(w, second, first)
	}
	m.mark(0, 0, ansi.StringWidth(label), "control", 0)
	m.mark(w-ansi.StringWidth(more), 0, ansi.StringWidth(more), "palette", 0)
	return between(w, first, second)
}
func (m ui) commandList(w int) string {
	actions := m.controls()
	count := min(len(actions), max(1, m.bodyRows-1))
	current := min(m.action, len(actions)-1)
	start := min(max(0, current-count+1), len(actions)-count)
	title := between(w-4, "Действия", fmt.Sprintf("%d / %d", current+1, len(actions)))
	var rows []string
	for i := start; i < start+count; i++ {
		a := actions[i]
		label := a.label
		switch a.key {
		case "enter", "esc", "ctrl+s", "ctrl+u", "e", "d", "l", "r", "q", "h", "o":
			label = between(w-8, label, strings.ToUpper(a.key))
		}
		m.mark(0, len(rows)+1, w, "control", i)
		rows = append(rows, activeRow(w-3, label, i == current))
	}
	return panel(title, rows, w, blue)
}
func (m ui) hint(w int) string {
	var hint string
	switch {
	case m.focus == actionsArea:
		hint = "↑↓ выбрать   Enter выполнить   Esc назад"
	case m.update != nil:
		if m.update.installing {
			hint = "Перезапуск программы…"
		} else {
			hint = "Esc отменить обновление"
		}
	case m.restoring:
		hint = "Esc запросить остановку проверки"
	case m.running:
		hint = "Esc запросить остановку"
	case m.confirmation():
		hint = "F8 подтвердить   Esc назад"
	case m.modal():
		hint = "Enter выбрать   Esc назад   F2 команды"
	case m.focus == menuArea:
		hint = "←→ раздел   Enter открыть   F2 команды"
	case m.tab == 0:
		hint = "↑↓ выбор   Пробел отметить   1–5 раздел"
	case m.tab == 2:
		switch {
		case m.suiteDetail:
			hint = "↑↓ серии   Esc к пунктам   1–5 раздел"
		case len(m.report.Coverage) > 0:
			hint = "↑↓ пункт   Enter данные   1–5 раздел"
		case m.report.Job == lab.RestoreReportTitle:
			hint = "↑↓ прокрутить   1–5 раздел"
		default:
			hint = "↑↓ серии   1–5 раздел"
		}
	default:
		hint = "↑↓ выбор   Enter открыть   1–5 раздел"
	}
	return fit(hint, w)
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
	case "confirm-action":
		if m.restorePicker != nil && m.restorePicker.plan != nil {
			cmd := m.restoreKey(tea.KeyMsg{Type: tea.KeyF8})
			return m, cmd
		}
		if m.confirm {
			cmd := m.begin()
			return m, cmd
		}
		return m, nil
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
		m.phasePos, m.operationOffset = m.selectedSuitePhase(), 0
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
		case "control", "palette":
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
	case "palette":
		return m.Update(tea.KeyMsg{Type: tea.KeyF2})
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
	case "result-page":
		m.focus, m.operationOffset = contentArea, t.index
		return m, nil
	case "coverage":
		m.operationOffset = 0
		m.suitePos = t.index
		m.focus = contentArea
		return m, nil
	case "series":
		m.focus = contentArea
		m.phasePos, m.operationOffset = t.index, 0
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
