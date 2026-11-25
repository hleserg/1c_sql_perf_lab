package main

import (
	"fmt"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/hleserg/1c_sql_perf_lab/lab"
	"strings"
	"time"
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
func (m ui) studyList(w, h int) string {
	p := m.profile()
	rowHeight := 1
	if h >= 23 && m.bodyRows >= 18 {
		rowHeight = 2
	}
	available := m.bodyRows
	if available < 1 {
		available = h
	}
	count := min(len(ids), max(2, (available-3)/rowHeight))
	start := min(max(0, m.planPos-count+1), len(ids)-count)
	rows := []string{}
	for i := start; i < start+count; i++ {
		id := ids[i]
		it := lab.ByID(id)
		mark := "[ ]"
		if m.selected[id] {
			mark = "[x]"
		}
		m.mark(0, len(rows)+1, w, "experiment", i)
		m.mark(4, len(rows)+1, 3, "check", i)
		if rowHeight == 2 {
			m.mark(0, len(rows)+2, w, "experiment", i)
		}
		rows = append(rows, activeRow(w-3, mark+"  "+it.Title, i == m.planPos))
		if rowHeight == 2 {
			extra := fmt.Sprintf("Целевой MAXDOP: %d", p.Target(id))
			switch id {
			case 18:
				extra = "Запись запросов: READ_WRITE"
			case 23:
				extra = "CHECKSUM; AUTO_CLOSE и AUTO_SHRINK выключены"
			case 28:
				extra = fmt.Sprintf("Целевой лимит памяти: %d МБ", p.MaxServerMemoryMB)
			case 34:
				extra = "Optimize for ad hoc workloads: включить"
			case 24:
				extra = "Исправления планов: включить"
			case 25:
				if p.AsyncStats {
					extra = "Асинхронное обновление: включить"
				} else {
					extra = "Асинхронное обновление: выключить"
				}
			case lab.ClusterID:
				extra = fmt.Sprintf("Соединений на процесс: %d", p.Cluster.Connections)
			}
			rows = append(rows, ink("       "+extra, dim))
		}
	}
	return panel("Эксперименты", rows, w, mint)
}
func (m ui) seriesList(w, h int) string {
	picked := m.selectedIDs()
	rows := []string{}
	add := func(title, detail string) {
		rows = append(rows, ink(title, ice))
		if h >= 24 {
			rows = append(rows, ink(detail, dim))
		}
	}
	add("Исходное состояние", "Перед первым изменением")
	for _, id := range picked {
		add(lab.Label(id)+"  "+lab.ByID(id).Title, "Возврат параметра после серии")
	}
	if len(picked) > 1 {
		add("Все выбранные вместе", "Взаимодействие параметров")
	}
	if len(picked) > 0 {
		add("Контроль после отката", "Повтор исходного замера")
	}
	return panel("План серий", rows, w, blue)
}

// The top row uses the same columns as the workload controls, so the plan
// shows exactly which profile values will govern the selected experiments.
func (m ui) workloadMetrics(w int) string {
	p := m.profile()
	counts := []int{len(m.selectedIDs()), p.Warmup, p.Samples, p.Sessions}
	labels := []string{"Выбрано", "Прогревов", "Повторов", "Сеансов 1С"}
	if w < 55 {
		return ink(fmt.Sprintf("Выбрано %d    Повторов %d    Сеансов %d", counts[0], counts[2], counts[3]), mint)
	}
	var value, caption strings.Builder
	for i, n := range counts {
		cell := (i+1)*(w+1)/len(counts) - i*(w+1)/len(counts) - 1
		if i > 0 {
			value.WriteByte(' ')
			caption.WriteByte(' ')
		}
		value.WriteString(strip(cell, fmt.Sprintf("  %02d", n), mint, railBG, true))
		caption.WriteString(strip(cell, "  "+labels[i], ice, railBG, false))
	}
	return value.String() + "\n" + caption.String()
}
func (m ui) plan(w int) string {
	short := m.h < 20
	it := lab.ByID(ids[m.planPos])
	if short || w < 65 {
		var rows []string
		rows = append(rows, m.studyList(w, 18), m.workloadMetrics(w))
		if m.h >= 16 {
			rows = append(rows, strings.Join(wrap(it.Check, w), "\n"))
		}
		return strings.Join(rows, "\n")
	}
	leftW := w * 53 / 100
	rw := w - leftW - 2
	prefix := m.workloadMetrics(w) + "\n\n"
	if m.h >= 32 {
		prefix = bold("План замеров", ice) + "\n" + ink(m.loadSource(), dim) + "\n\n" + prefix
	}
	list := m.offset(0, strings.Count(prefix, "\n"))
	list.bodyRows -= strings.Count(prefix, "\n")
	left := list.studyList(leftW, m.h)
	desc := wrap(it.Check, rw-4)
	right := panel("Критерий проверки", desc, rw, blue) + "\n\n" + m.seriesList(rw, m.h)
	body := prefix + columns(w, leftW, left, right)
	return body
}
func (m ui) loadSource() string {
	p := m.profile()
	if m.demo {
		return "Синтетические измерения демонстрационного стенда"
	}
	if len(p.Workload) > 0 {
		return "Внешний сценарий из профиля стенда"
	}
	return "Чтение документа: " + p.Document
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
func trend(values []float64, w, h int) []string {
	if len(values) == 0 || w < 10 || h < 3 {
		return nil
	}
	data := values[max(0, len(values)-w):]
	lo, hi := data[0], data[0]
	for _, v := range data {
		lo = min(lo, v)
		hi = max(hi, v)
	}
	chart := make([][]rune, h)
	for i := range chart {
		chart[i] = []rune(strings.Repeat(" ", w))
	}
	for i, v := range data {
		x := i
		if len(data) > 1 {
			x = i * (w - 1) / (len(data) - 1)
		}
		y := h / 2
		if hi != lo {
			y = h - 1 - int((v-lo)/(hi-lo)*float64(h-1)+0.5)
		}
		chart[y][x] = '*'
	}
	rows := make([]string, h)
	for y := range chart {
		label := ""
		if y == 0 {
			label = fmt.Sprintf("%.1f", hi)
		} else if y == h-1 {
			label = fmt.Sprintf("%.1f", lo)
		}
		rows[y] = ink(pad(label, 8)+"| ", dim) + ink(string(chart[y]), mint)
	}
	return rows
}
func (m ui) bench(w int) string {
	if m.restoring || (!m.running && m.report.Job == lab.RestoreReportTitle) {
		return m.restoreDashboard(w)
	}
	if !m.running && len(m.log) == 0 {
		return panel("Ход прогона", []string{"Измерения ещё не запускались.", "Выбери эксперименты в разделе «План»."}, w, blue)
	}
	v := m.current
	listW := w
	side := w >= 83 && m.h >= 19
	if side {
		listW = w * 63 / 100
	}
	rows := []string{}
	if v.SuiteTotal > 0 {
		rows = append(rows, fmt.Sprintf("Этап %d из %d", v.SuiteStage, v.SuiteTotal), progressBar(listW-6, v.SuiteStage-1, v.SuiteTotal))
		rows = append(rows, wrap(v.SuiteName, listW-5)...)
	}
	rows = append(rows, wrap(v.Phase, listW-5)...)
	rows = append(rows, wrap(v.Detail, listW-5)...)
	if v.Total > 0 {
		rows = append(rows, progressBar(listW-18, v.Step, v.Total)+fmt.Sprintf("   %d / %d", v.Step, v.Total))
	}
	if len(m.points) > 0 {
		rows = append(rows, "", bold(fmt.Sprintf("%.1f мс", m.points[len(m.points)-1]), mint)+"  "+ink(m.series, dim))
		if m.h >= 21 {
			rows = append(rows, trend(m.points, listW-15, min(9, max(3, m.h/7)))...)
		}
	}
	if !m.started.IsZero() {
		rows = append(rows, "", ink("От начала  "+time.Since(m.started).Truncate(time.Second).String(), dim))
	}
	left := panel("Измерения", rows, listW, mint)
	if !side {
		return left
	}
	n := max(2, m.h-15)
	var logs []string
	for _, s := range m.log[max(0, len(m.log)-n):] {
		logs = append(logs, s)
	}
	return columns(w, listW, left, panel("Этапы прогона", logs, w-listW-2, blue))
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

func (m ui) results(w int) string {
	if len(m.report.Coverage) > 0 && !m.suiteDetail {
		return m.fullSummary(w)
	}
	if m.report.Job == lab.RestoreReportTitle {
		return m.restoreDashboard(w)
	}
	if m.phasePos >= len(m.report.Phases) && len(m.report.Sections) > 0 {
		return m.sectionView(w)
	}
	if len(m.report.Phases) == 0 {
		if m.report.Error != "" {
			rows := wrap("Дальше  "+runNextStep(m.report.Error, m.file != ""), w-5)
			rows = append(rows, "")
			rows = append(rows, wrap(m.report.Error, w-5)...)
			return panel("Прогон остановлен", rows, w, red)
		}
		return panel("Результаты", []string{"Серий замеров пока нет.", "Последний протокол можно открыть кнопкой внизу."}, w, blue)
	}
	idx := min(m.phasePos, len(m.report.Phases)-1)
	phase := m.report.Phases[idx]
	wide := w >= 85
	cols := 27
	if wide {
		cols = 57
	}
	operation := max(9, w-cols)
	header := pad("Операция", operation) + pad("Медиана, мс", 14)
	if wide {
		header += pad("Диапазон, мс", 23) + pad("Замеров", 9)
	}
	var rows = []string{bold(phase.Name, ice), ink(fmt.Sprintf("Серия %d из %d", idx+1, len(m.report.Phases)), dim), "", ink(header+"Изменение", blue)}
	for _, v := range lab.Rows(m.report) {
		if v.Phase != phase.Name {
			continue
		}
		d := "-"
		c := dim
		if v.Compared {
			d = lab.Delta(v.Delta)
			if v.Delta < 0 {
				c = mint
			} else {
				c = gold
			}
		}
		row := pad(v.Name, operation) + pad(fmt.Sprintf("%.2f", v.Median), 14)
		if wide {
			row += pad(fmt.Sprintf("%.2f ... %.2f", v.Min, v.Max), 23) + pad(fmt.Sprint(v.N), 9)
		}
		rows = append(rows, row+bold(d, c))
	}
	out := panel("Время операций", rows, w, mint)
	if m.h >= 24 && len(m.report.Phases) > 1 {
		var series []string
		for i, p := range m.report.Phases {
			title := p.Name
			if w > 63 {
				title = between(w-9, title, fmt.Sprintf("%d замеров", len(p.Raw)))
			}
			m.mark(0, strings.Count(out, "\n")+3+len(series), w, "series", i)
			series = append(series, activeRow(w-3, title, i == idx))
		}
		out += "\n\n" + panel("Серии прогона", series, w, blue)
	}
	return out
}
func (m ui) modeLabel() (string, lipgloss.Color) {
	if x := m.restorePicker; x != nil {
		if x.local && x.plan == nil {
			return "ЭТОТ ПК", mint
		}
		return "СТЕНД", mint
	}
	demo := m.demo
	if m.tab == 2 && (m.report.Job != "" || len(m.report.Phases) > 0) {
		demo = m.report.Demo
	}
	if demo {
		return "ДЕМО", plum
	}
	return "СТЕНД", mint
}
func (m ui) rail(w, h int) string {
	rows := make([]string, h)
	for i := range rows {
		rows[i] = strip(w, "", ice, railBG, false)
	}
	for i, name := range longNames {
		y := i + 1
		if h >= 25 {
			y = i*2 + 2
		}
		if y >= h {
			break
		}
		m.mark(0, y, w, "nav", i)
		s := "  " + name
		fg, bg := dim, railBG
		if i == m.tab {
			fg, bg = ice, accentBG
			if m.focus == menuArea && !m.modal() {
				fg, bg = canvas, mint
				s = "> " + s[2:]
			}
		}
		rows[y] = strip(w, s, fg, bg, i == m.tab)
	}
	if h >= 19 {
		p := m.profile()
		details := []string{"ПОДКЛЮЧЕНИЕ", p.Name, "SQL  " + p.ExpectedServer, "База " + p.Database}
		if m.demo {
			details = []string{"ДЕМОНСТРАЦИЯ", "Синтетические данные"}
		}
		if x := m.restorePicker; x != nil {
			if x.auto && x.plan == nil {
				details = []string{"ЭТОТ КОМПЬЮТЕР", x.profile.RestoreFolder()}
			} else {
				details = []string{"ВОССТАНОВЛЕНИЕ", x.profile.ExpectedServer, x.profile.Database, x.login}
			}
		}
		for i, s := range details {
			c := dim
			if i == 0 {
				c = blue
			}
			rows[h-len(details)-1+i] = strip(w, "  "+s, c, railBG, i == 0)
		}
	}
	return strings.Join(rows, "\n")
}
func (m ui) horizontal(w int) string {
	var b strings.Builder
	labels := names
	if w < 52 {
		labels = []string{"План", "Ход", "Итог", "Каталог", "SQL"}
	}
	x := 0
	for i, name := range labels {
		label := " " + name + " "
		width := lipgloss.Width(label)
		m.mark(x, 0, width, "nav", i)
		if i == m.tab {
			fg, bg := ice, accentBG
			if m.focus == menuArea && !m.modal() {
				fg, bg = canvas, mint
			}
			b.WriteString(strip(width, label, fg, bg, true))
		} else {
			b.WriteString(ink(label, dim))
		}
		b.WriteString(" ")
		x += width + 1
	}
	return b.String()
}
func (m ui) confirmView(w int) string {
	title := "Запустить эксперимент?"
	p := m.profile()
	rows := []string{p.ExpectedServer + " / " + p.Database, ""}
	if j, ok := lab.FindJob(m.chosenJob); ok {
		title = j.Name
		rows = append(rows, wrap(j.Detail, w-5)...)
	}
	if m.recovery {
		title = "Восстановить настройки из журнала?"
	}
	return panel(title, rows, w, gold)
}
func (m ui) workspaceWidth() (int, int) {
	w, h := m.w, m.h
	railW := 0
	if w >= 112 && h >= 21 {
		railW = 25
		if w >= 158 {
			railW = 29
		}
	}
	inner := w - 4
	if railW > 0 {
		inner -= railW + 1
	}
	return railW, inner
}
func (m ui) footer(w int) (foot, buttons []string) {
	// Pin status and actions to the visible last rows. Reserve room for diagnostics even on an error.

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
	buttons = m.actionRows(w - 2)
	foot = append(foot, ink(" "+m.hint(w-2), dim))
	for _, row := range buttons {
		foot = append(foot, " "+row)
	}
	return foot, buttons
}

func (m ui) contentHeight(rail, footerRows int) (height, lead int) {
	height = m.h - 1 - footerRows
	if rail == 0 {
		height--
	}
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
	header := strip(5, " 1С  ", canvas, mint, true)
	title := "  SQL PERF LAB"
	if w >= 78 {
		title += "     Производительность ERP"
	}
	right := mode + "  "
	if w >= 78 && m.running {
		if m.restoring {
			right = "ВОССТАНОВЛЕНИЕ  /  " + right
		} else {
			right = "ИДЁТ ПРОГОН  /  " + right
		}
	}
	header += strip(w-5-lipgloss.Width(right)-1, title, ice, titleBG, true) + strip(lipgloss.Width(right)+1, " "+right, canvas, tint, true)
	railW, inner := m.workspaceWidth()
	var top = []string{header}
	if railW == 0 {
		top = append(top, " "+m.offset(1, 1).horizontal(w-2))
	}
	foot, buttons := m.footer(w)
	bodyH, lead := m.contentHeight(railW, len(foot))
	bodyX := 2
	if railW > 0 {
		bodyX += railW + 1
	}
	bodyModel := m.offset(bodyX, len(top)+lead)
	bodyModel.bodyRows = bodyH - lead
	if p := m.probe; p != nil {
		switch {
		case p.y >= h-len(buttons):
			if p.target.kind == "" {
				p.target = hitTarget{kind: "footer"}
			}
			bodyModel.probe = nil
		case p.y < len(top) || p.y >= len(top)+bodyH:
			bodyModel.probe = nil
		case railW > 0 && p.x < railW:
			p.target = hitTarget{kind: "navarea"}
			bodyModel.probe = nil
		case p.x >= bodyX && p.x < bodyX+inner:
			p.target = hitTarget{kind: "content"}
		}
	}
	var content string
	switch {
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
	if railW > 0 {
		rail := strings.Split(m.offset(0, len(top)).rail(railW, bodyH), "\n")
		for i := range col {
			col[i] = rail[i] + " " + col[i]
		}
	}
	rows := append(top, col...)
	rows = append(rows, foot...)
	return strings.Join(rows, "\n")
}
