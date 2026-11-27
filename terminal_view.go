package main

import (
	"fmt"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/hleserg/1c_sql_perf_lab/lab"
	"strings"
)

var (
	canvas   = lipgloss.Color("#101923")
	railBG   = lipgloss.Color("#172836")
	accentBG = lipgloss.Color("#173B43")
	titleBG  = lipgloss.Color("#22394B")
)
var names = []string{"План", "Прогон", "Итог", "Каталог", "SQL"}
var longNames = []string{"План замеров", "Ход прогона", "Результаты", "Мероприятия", "Работы SQL"}

func strip(w int, s string, fg, bg lipgloss.Color, strong bool) string {
	return lipgloss.NewStyle().Foreground(fg).Background(bg).Bold(strong).Render(pad(s, w))
}
func between(w int, left, right string) string {
	rw := lipgloss.Width(right)
	if rw+3 >= w {
		return fit(left, w)
	}
	return pad(left, w-rw) + right
}
func wrap(s string, w int) []string {
	if w < 1 {
		return nil
	}
	return strings.Split(ansi.Wrap(s, w, ""), "\n")
}

// Keep one consistent, text-only caption for dynamically sized detail panes and dialogs.
func panel(title string, body []string, w int, c lipgloss.Color) string {
	rows := []string{strip(w, "  "+title, c, titleBG, true)}
	for _, v := range body {
		rows = append(rows, "  "+pad(v, max(1, w-3)))
	}
	return strings.Join(rows, "\n")
}
func activeRow(w int, text string, active bool) string {
	if active {
		return strip(w, "> "+text, ice, accentBG, true)
	}
	return "  " + pad(text, max(1, w-2))
}
func columns(w, leftWidth int, left, right string) string {
	const gap = 2
	rightWidth := w - gap - leftWidth
	l, r := strings.Split(left, "\n"), strings.Split(right, "\n")
	rows := make([]string, max(len(l), len(r)))
	for i := range rows {
		var a, b string
		if i < len(l) {
			a = l[i]
		}
		if i < len(r) {
			b = r[i]
		}
		rows[i] = pad(a, leftWidth) + "  " + pad(b, rightWidth)
	}
	return strings.Join(rows, "\n")
}
func (m ui) profile() lab.Profile {
	if m.demo {
		return demoProfile()
	}
	return m.liveProfile
}
func (m ui) selectedIDs() []int {
	var out []int
	for _, id := range ids {
		if m.selected[id] {
			out = append(out, id)
		}
	}
	return out
}
func (m ui) studyList(w int) string {
	count := min(len(ids), max(1, m.bodyRows-1))
	start := min(max(0, m.planPos-count+1), len(ids)-count)
	rows := make([]string, 0, count)
	for i := start; i < start+count; i++ {
		id := ids[i]
		mark := "[ ]"
		if m.selected[id] {
			mark = "[x]"
		}
		label := mark + "  " + lab.ByID(id).Title
		if w >= 45 {
			label = between(w-6, label, lab.Label(id))
		}
		m.mark(0, len(rows)+1, w, "experiment", i)
		m.mark(4, len(rows)+1, 3, "check", i)
		rows = append(rows, activeRow(w-3, label, i == m.planPos))
	}
	title := between(w-4, "Мероприятия", fmt.Sprintf("выбрано %d", len(m.selectedIDs())))
	return panel(title, rows, w, mint)
}
func (m ui) plan(w int) string {
	p := m.profile()
	source := m.loadSource()
	target := p.Database
	if w >= 65 && !m.demo && p.ExpectedServer != "" {
		target = p.ExpectedServer + " / " + p.Database
	}
	if m.demo {
		target = "Демонстрация"
	}
	heading := bold(target, ice) + "  " + ink("·  "+source, dim)
	if w < 65 && !m.demo {
		// Keep the workload visible even if the SQL database has a long name.
		heading = bold(fit(target, w/2), ice) + "  " + ink("·  "+source, dim)
	}
	density := fmt.Sprintf("Сеансы %d   Повторы %d   Прогрев %d", p.Sessions, p.Samples, p.Warmup)
	intro := []string{fit(heading, w), ink(fit(density, w), blue)}
	if !m.demo && len(p.Workload) == 0 && len(p.ERPPostDocuments) > 0 {
		intro = append(intro, ink(fit(fmt.Sprintf("Перепроведение с откатом · видов %d", len(p.ERPPostDocuments)), w), gold))
	}
	if m.file != "" && !m.report.Started.IsZero() {
		status := "завершён"
		if m.report.Error != "" {
			status = "ошибка"
		} else if m.report.Finished.IsZero() {
			status = "нет времени завершения"
		}
		origin := m.report.Profile
		if m.report.Demo {
			origin = "демо"
		}
		recent := fmt.Sprintf("3 Итог · %s · %s", m.report.Started.Local().Format("02.01 15:04"), status)
		if origin != "" {
			recent += " · " + origin
		}
		intro = append(intro, ink(fit(recent, w), dim))
		m.mark(0, len(intro)-1, min(w, lipgloss.Width(recent)), "nav", 2)
	}
	if m.bodyRows >= 15 {
		intro = append(intro, "")
	}
	list := m.offset(0, len(intro))
	list.bodyRows -= len(intro)
	it := lab.ByID(ids[m.planPos])
	if w < 78 {
		body := list.studyList(w)
		if list.bodyRows > len(ids)+2 {
			body += "\n\n" + strings.Join(wrap(it.Check, w), "\n")
		}
		return strings.Join(intro, "\n") + "\n" + body
	}
	leftW := w * 54 / 100
	detailW := w - leftW - 2
	details := []string{bold(it.Title, ice), ink(m.targetLabel(it.ID), mint), ""}
	details = append(details, wrap(it.Check, detailW-5)...)
	picked := len(m.selectedIDs())
	stages := []string{"Исходный замер"}
	if picked > 0 {
		stages = append(stages, fmt.Sprintf("По отдельности · %d", picked))
		if picked > 1 {
			stages = append(stages, "Все выбранные вместе")
		}
		stages = append(stages, "Контроль после отката")
	}
	right := panel("Что сравниваем", details, detailW, blue) + "\n\n" + panel("Порядок серий", stages, detailW, blue)
	return strings.Join(intro, "\n") + "\n" + columns(w, leftW, list.studyList(leftW), right)
}
func (m ui) targetLabel(id int) string {
	p := m.profile()
	switch id {
	case 19:
		return fmt.Sprintf("MAXDOP → %d", p.MaxDOP)
	case 24:
		return "Исправления оптимизатора → вкл."
	case 25:
		if p.AsyncStats {
			return "Асинхронное обновление → вкл."
		}
		return "Асинхронное обновление → выкл."
	case 18:
		return "Query Store → READ_WRITE"
	case 23:
		return "CHECKSUM · AUTO_CLOSE OFF · AUTO_SHRINK OFF"
	case 28:
		if p.MaxServerMemoryMB == 0 {
			return "Лимит памяти в профиле не задан"
		}
		return fmt.Sprintf("Лимит → %d МБ", p.MaxServerMemoryMB)
	case 34:
		return "Optimize for ad hoc workloads → вкл."
	case lab.ClusterID:
		return fmt.Sprintf("Соединений на процесс → %d", p.Cluster.Connections)
	}
	return ""
}
func (m ui) loadSource() string {
	p := m.profile()
	switch {
	case m.demo:
		return "Синтетическая нагрузка"
	case len(p.Workload) > 0:
		return "Внешний сценарий"
	case p.DocumentUUID != "":
		return "Чтение документа · " + p.Document
	default:
		return "Операции 1С"
	}
}
func (m ui) catalog(w int) string {
	all := lab.All()
	side := w >= 90
	listW := w
	if side {
		listW = w * 62 / 100
	}
	size := max(3, min(len(all), m.h-13))
	if m.h <= 18 {
		size = min(4, len(all))
	}
	start := max(0, min(len(all)-size, m.catalogPos-size/2))
	var rows []string
	kind := func(id int) string {
		if lab.Runnable(id) {
			return "эксперимент"
		}
		if sqlJobIndex(id) >= 0 {
			return "SQL"
		}
		return "карточка"
	}
	for i := start; i < min(len(all), start+size); i++ {
		it := all[i]
		s := lab.Label(it.ID) + "  " + it.Title
		if listW >= 56 {
			s = between(listW-6, s, kind(it.ID))
		}
		m.mark(0, len(rows)+1, listW, "catalog", i)
		rows = append(rows, activeRow(listW-3, s, i == m.catalogPos))
	}
	list := panel(fmt.Sprintf("Мероприятия   %d / %d", m.catalogPos+1, len(all)), rows, listW, mint)
	it := all[m.catalogPos]
	if !side {
		return list + "\n" + fit(it.Check, w)
	}
	rw := w - listW - 2
	desc := []string{bold(it.Title, ice), ink(it.Group, dim), ""}
	desc = append(desc, wrap(it.Check, rw-5)...)
	if k := kind(it.ID); k != "карточка" {
		desc = append(desc, "", ink("Enter  открыть "+k, blue))
	}
	return columns(w, listW, list, panel("Критерий проверки", desc, rw, blue))
}
func progressBar(w, n, total int) string {
	if total < 1 {
		return ""
	}
	w = max(4, w)
	done := max(0, min(w, n*w/total))
	return strip(done, strings.Repeat("=", done), mint, accentBG, true) + ink(strings.Repeat("-", w-done), line)
}
func runNextStep(raw string, hasReport bool) string {
	msg := strings.ToLower(raw)
	switch {
	case strings.Contains(msg, "изменено извне"):
		return "Сверь текущее значение с журналом отката на стенде перед новым прогоном."
	case strings.Contains(msg, "журнал изменения"), strings.Contains(msg, "проверка отката"):
		return "Нажми «Вернуть настройки» на том же стенде и дождись проверки исходных значений."
	case strings.Contains(msg, "уже идёт эксперимент"):
		return "Дождись завершения прогона на этой базе и снова нажми «Запустить»."
	case strings.Contains(msg, "отличается от профиля"), strings.Contains(msg, "отличаются от журнала"):
		return "Открой «Профиль» и сверь экземпляр SQL, имя базы и выбранный стенд."
	case strings.Contains(msg, "optimizerbuild"):
		return "Сверь сборку SQL в отчёте инвентаризации и укажи её в профиле для пункта 24."
	case strings.Contains(msg, "auto_update_statistics"):
		return "Сними пункт 25 в плане либо проверь настройку автостатистики стенда."
	case strings.HasPrefix(msg, "подготовка подключения 1с:"):
		return strings.TrimSpace(strings.SplitN(raw, ":", 2)[1])
	case strings.Contains(msg, "8002801d"), strings.Contains(msg, "80040154"):
		return "Открой Perf Lab от имени администратора и нажми «Исправить подключение 1С»."
	case strings.Contains(msg, "com · "):
		return "Причина и следующий шаг указаны в строке этапа COM."
	case strings.Contains(msg, "rac"), strings.Contains(msg, "лимит соединений"):
		return "Открой «Профиль» и проверь путь к rac, адрес RAS и параметры рабочего сервера."
	case strings.Contains(msg, "нагрузк"), strings.Contains(msg, "erpdatabase"), strings.Contains(msg, "comconnector"):
		return "Открой «Профиль» и проверь подключение 1С и выбранный сценарий нагрузки."
	case strings.Contains(msg, "login failed"), strings.Contains(msg, "permission"), strings.Contains(msg, "denied"), strings.Contains(msg, "отказано"):
		return "Проверь учётную запись подключения и её права на операцию, указанную в ошибке."
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "timed out"), strings.Contains(msg, "deadline exceeded"):
		return "Проверь доступность стенда и лимит времени операции в профиле."
	case strings.Contains(msg, "context canceled"):
		return "Для следующего прогона открой «План замеров» и выбери нужные пункты."
	case hasReport:
		return "Нажми «Открыть HTML» и посмотри первый незавершённый этап в протоколе."
	default:
		return "Проверь путь профиля и доступ к папке программы, затем повтори запуск."
	}
}

func (m ui) modeLabel() (string, lipgloss.Color) {
	if x := m.restorePicker; x != nil {
		if x.local && x.plan == nil {
			return "ЭТОТ ПК", mint
		}
		return "СТЕНД", mint
	}
	if m.demo {
		return "ДЕМО", plum
	}
	return "СТЕНД", mint
}
func (m ui) horizontal(w int) string {
	labels := names
	if w >= 80 {
		labels = longNames
	}
	if w < 55 {
		labels = []string{"План", "Ход", "Итог", "Каталог", "SQL"}
	}
	var b strings.Builder
	x := 0
	for i, name := range labels {
		label := fmt.Sprintf(" %d %s", i+1, name)
		n := lipgloss.Width(label)
		m.mark(x, 0, n, "nav", i)
		if i == m.tab {
			label = "›" + strings.TrimPrefix(label, " ")
			fg, bg := ice, accentBG
			if m.focus == menuArea {
				fg, bg = canvas, mint
			}
			b.WriteString(strip(n, label, fg, bg, true))
		} else {
			b.WriteString(ink(label, dim))
		}
		b.WriteByte(' ')
		x += n + 1
	}
	return b.String()
}
func (m ui) confirmView(w int) string {
	title := "Запустить эксперимент?"
	p := m.profile()
	rows := []string{p.ExpectedServer + " / " + p.Database, ""}
	if m.chosenJob == "" && !m.recovery {
		rows = append(rows, m.loadSource())
		if len(p.Workload) == 0 && len(p.ERPPostDocuments) > 0 {
			rows = append(rows, wrap("Перепроведение с откатом · "+strings.Join(p.ERPPostDocuments, ", "), w-5)...)
		}
	}
	if j, ok := lab.FindJob(m.chosenJob); ok {
		title = j.Name
		rows = append(rows, wrap(j.Detail, w-5)...)
	}
	if m.chosenJob == "erp-repair" {
		title = "Исправить подключение 1С и запустить замеры?"
		rows = append(rows, wrap("Будет зарегистрирован COM-коннектор x64 из сборки работающего агента. Нужны права администратора Windows.", w-5)...)
		rows = append(rows, wrap("Если RAS выключен, на время прогона откроется его порт администрирования. Доступ по сети зависит от правил брандмауэра. Затем начнётся полный прогон через 1С.", w-5)...)
	}
	if m.recovery {
		title = "Восстановить настройки из журнала?"
		rows = append(rows, wrap("Для возврата параметра кластера может потребоваться временный RAS на порту из журнала.", w-5)...)
	}
	return panel(title, rows, w, gold)
}
func (m ui) contentWidth() int { return m.w - 4 }
func (m ui) footer(w int) []string {
	var foot []string
	if m.tab == 2 && !m.modal() && (!m.report.Started.IsZero() || m.report.Job != "") && m.report.Demo != m.demo {
		source := "Протокол стенда · " + m.report.Profile
		if m.report.Demo {
			source = "Демонстрационный протокол"
		}
		foot = append(foot, ink(" "+fit(strings.TrimSuffix(source, " · "), w-2), plum))
	}
	if m.delivery != "" {
		foot = append(foot, ink(fit(" GitHub   "+m.delivery, w), blue))
	}
	message := m.msg
	if m.form != nil {
		message = m.form.err
	}
	if m.restorePicker != nil {
		x := m.restorePicker
		message = x.err
		if message != "" {
			next := "Проверь путь к папке и нажми «Обновить список»."
			switch {
			case x.standOnly:
				next = "Проверь, что служба SQL Server запущена, и повтори поиск."
			case x.plan != nil:
				next = "Вернись к выбору и проверь подключение и доступ к папке профиля."
			case x.file != "":
				next = failureAdvice(lab.RestoreProgress{Stage: lab.RestorePrepare}, message, false).next
				if strings.Contains(next, "Открой протокол") || strings.Contains(next, "Выбрать копию") {
					next = "Проверь путь к файлу и выбери доступную полную копию."
				}
			}
			message = next + " " + message
		}
	}
	if message != "" {
		msgs := wrap(message, w-3)
		for _, s := range msgs[:min(2, len(msgs))] {
			foot = append(foot, ink(" "+fit(s, w-2), gold))
		}
	}
	foot = append(foot, strip(w, "  "+m.hint(w-3), dim, railBG, false))
	return foot
}

func (m ui) contentHeight(footerRows int) (height, lead int) {
	height = m.h - 3 - footerRows
	if m.h >= 23 {
		lead = 1
	}
	return height, lead
}
func (m ui) View() string {
	w, h := m.w, m.h
	if w < 44 || h < 14 {
		return fit("SQL PERF LAB | Минимум 44 x 14", w)
	}
	mode, tint := m.modeLabel()
	right := " " + mode + " "
	brand := " PERF LAB"
	if w >= 68 {
		brand += "   /   Производительность 1С и SQL"
	}
	titleWidth := w - lipgloss.Width(right)
	if !m.demo && m.liveProfile.Database != "" && m.restorePicker == nil {
		base := fit(m.liveProfile.Database, titleWidth/2)
		brand = between(titleWidth, brand, base+"  ")
	}
	header := strip(titleWidth, brand, ice, railBG, true) + strip(lipgloss.Width(right), right, canvas, tint, true)
	inner := m.contentWidth()
	top := []string{header}
	if m.modal() {
		context := "Стенд"
		switch {
		case m.update != nil:
			context = "Обновление программы"
		case m.restorePicker != nil:
			context = "База и резервные копии"
		case m.form != nil:
			context = "Профиль стенда"
		case m.running:
			context = "Ход прогона"
		case m.confirm:
			context = "Подтверждение"
		}
		top = append(top, "  "+ink(context, blue))
	} else {
		top = append(top, " "+m.offset(1, 1).horizontal(w-2))
	}
	foot := m.footer(w)
	bodyH, lead := m.contentHeight(len(foot))
	if m.confirmation() {
		// Keep repeated clicks on the previous action off the confirmation target.
		top = append(top, "  "+m.offset(2, 1).commandRow(inner))
		top[1], top[2] = top[2], top[1]
	} else {
		top = append(top, "  "+m.offset(2, 2).commandRow(inner))
	}
	bodyModel := m.offset(2, len(top)+lead)
	bodyModel.bodyRows = bodyH - lead
	if p := m.probe; p != nil {
		if p.y < len(top) || p.y >= len(top)+bodyH || p.x < 2 || p.x >= inner+2 {
			bodyModel.probe = nil
		} else {
			p.target = hitTarget{kind: "content"}
		}
	}
	var content string
	switch {
	case m.focus == actionsArea:
		content = bodyModel.commandList(inner)
	case m.update != nil:
		content = bodyModel.updateView(inner)
	case m.restorePicker != nil:
		content = bodyModel.restoreView(inner)
	case m.form != nil:
		content = bodyModel.formView(inner)
	case m.confirm:
		content = bodyModel.confirmView(inner)
	default:
		switch m.tab {
		case 0:
			content = bodyModel.plan(inner)
		case 1:
			content = bodyModel.bench(inner)
		case 2:
			content = bodyModel.results(inner)
		case 3:
			content = bodyModel.catalog(inner)
		case 4:
			content = bodyModel.jobsView(inner)
		}
	}
	body := strings.Split(content, "\n")
	col := make([]string, bodyH)
	for i := range col {
		idx := i - lead
		var s string
		if idx >= 0 && idx < len(body) {
			s = body[idx]
		}
		col[i] = "  " + pad(s, inner) + "  "
	}
	rows := append(top, col...)
	rows = append(rows, foot...)
	return strings.Join(rows, "\n")
}
