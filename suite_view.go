package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/hleserg/1c_sql_perf_lab/lab"
)

func failedERP(r lab.Report) bool {
	return r.Workload == "1С · исходный замер не получен" && r.Error != ""
}

// A combined or control phase cannot establish an individual item's effect.
func individualComparisons(r lab.Report, results []lab.Row) map[int]int {
	paired := make(map[string]bool)
	for _, row := range results {
		if row.Compared {
			paired[row.Phase] = true
		}
	}
	found := make(map[int]int)
	for i, phase := range r.Phases {
		if len(phase.IDs) == 1 && phase.Complete && paired[phase.Name] {
			if _, exists := found[phase.IDs[0]]; !exists {
				found[phase.IDs[0]] = i
			}
		}
	}
	return found
}

func initialSuitePosition(r lab.Report) int {
	paired := individualComparisons(r, lab.Rows(r))
	for i, item := range r.Coverage {
		if failedERP(r) {
			if item.ID == 11 {
				return i
			}
		} else if _, ok := paired[item.ID]; ok {
			return i
		}
	}
	return 0
}

func (m ui) selectedSuitePhase() int {
	return max(0, m.suiteDataIndex())
}

func (m ui) suiteDataIndex() int {
	if m.suitePos >= 0 && m.suitePos < len(m.report.Coverage) {
		id := m.report.Coverage[m.suitePos].ID
		if phase, ok := individualComparisons(m.report, lab.Rows(m.report))[id]; ok {
			return phase
		}
		for i, section := range m.report.Sections {
			if section.ID == id {
				return len(m.report.Phases) + i
			}
		}
	}
	return -1
}

func deltaText(row lab.Row) string {
	if !row.Compared {
		return ink("—", dim)
	}
	value := lab.Delta(row.Delta)
	color := dim
	if value != "0.0%" {
		if row.Delta < 0 {
			color = mint
		} else {
			color = gold
		}
	}
	return bold(value, color)
}

func timePair(row lab.Row) string {
	if !row.Compared {
		return fmt.Sprintf("%.2f мс   Δ —   n=%d", row.Median, row.N)
	}
	return fmt.Sprintf("%.2f → %.2f мс   %s   n=%d", row.BaselineMedian, row.Median, deltaText(row), row.N)
}

func (m ui) fullSummary(w int) string {
	if len(m.report.Coverage) == 0 {
		return m.results(w)
	}
	view, _, _ := m.summaryPage(w)
	return view
}

func (m ui) pageLabel(w, y int, unit string, first, last, total, previous, next int) string {
	m.mark(0, y, 3, "result-page", previous)
	m.mark(w-3, y, 3, "result-page", next)
	label := fmt.Sprintf("%s %d–%d/%d  ·  PgUp/PgDn", unit, first+1, last, total)
	return ink("‹  "+pad(label, w-6)+"  ›", blue)
}

func (m ui) summaryPage(w int) (string, int, int) {
	r := m.report
	pos := max(0, min(m.suitePos, len(r.Coverage)-1))
	item := r.Coverage[pos]
	results := lab.Rows(r)
	compared := individualComparisons(r, results)
	count := 0
	for _, entry := range r.Coverage {
		if _, ok := compared[entry.ID]; ok {
			count++
		}
	}
	title := fmt.Sprintf("Парный замер у %d из %d пунктов", count, len(r.Coverage))
	if failedERP(r) {
		title = "Остановлено · нет исходного замера 1С"
	} else if r.Error != "" {
		title = "Ошибки · " + title
	}
	rows := []string{strip(w, " "+title, ice, titleBG, true)}
	if failedERP(r) {
		cause := strings.TrimPrefix(r.Error, "Прогон через 1С остановлен до изменения настроек: ")
		if strings.HasPrefix(cause, "Подготовка подключения 1С: ") {
			cause = strings.TrimPrefix(cause, "Подготовка подключения 1С: ")
		} else if strings.Contains(strings.ToLower(cause), "8002801d") {
			cause = "Windows: библиотека типов COM 1С не зарегистрирована (0x8002801D)."
		}
		rows = append(rows, wrap(ink(cause, gold), w)...)
		if !strings.HasPrefix(r.Error, "Подготовка подключения 1С:") {
			rows = append(rows, wrap(runNextStep(r.Error, true), w)...)
		}
	} else if r.Workload != "" {
		rows = append(rows, fit(r.Workload, w))
	}
	rows = append(rows, fit(bold(lab.Label(item.ID)+"  "+item.Title, ice), w))
	if ix, ok := compared[item.ID]; ok {
		phase := r.Phases[ix]
		for _, v := range results {
			if v.Phase == phase.Name && v.Compared {
				rows = append(rows, wrap("Медиана · "+v.Name, w)...)
				rows = append(rows, timePair(v))
				break
			}
		}
		rows = append(rows, fit(ink(item.Status+" · Enter все операции", dim), w))
	} else if !failedERP(r) || item.ID != 11 {
		rows = append(rows, wrap(item.Status+" · "+item.Reason, w)...)
		if item.NextAction != "" && m.bodyRows >= 12 {
			rows = append(rows, wrap(ink(item.NextAction, dim), w)...)
		}
	}
	h := max(2, m.bodyRows)
	// Pin the run status when a diagnostic or explanation spans several screens.
	if len(rows) > h {
		body := rows[1:]
		capacity := max(1, h-2)
		first := min(max(0, m.operationOffset), len(body)-1)
		last := min(first+capacity, len(body))
		previous, next := max(0, first-capacity), first
		if last < len(body) {
			next = last
		}
		page := []string{rows[0], m.pageLabel(w, 1, "Строки", first, last, len(body), previous, next)}
		return strings.Join(append(page, body[first:last]...), "\n"), previous, next
	}
	visible := min(len(r.Coverage), h-len(rows))
	start := max(0, min(len(r.Coverage)-visible, pos-visible/2))
	for i := start; i < start+visible; i++ {
		entry := r.Coverage[i]
		m.mark(0, len(rows), w, "coverage", i)
		label := lab.Label(entry.ID) + "  " + entry.Title
		if w >= 58 {
			label = between(w-3, label, entry.Status)
		}
		rows = append(rows, activeRow(w, label, i == pos))
	}
	return strings.Join(rows, "\n"), -1, -1
}

func (m ui) bench(w int) string {
	if m.restoring || (!m.running && m.report.Job == lab.RestoreReportTitle) {
		return m.restoreDashboard(w)
	}
	if !m.running {
		if len(m.log) == 0 && m.report.Finished.IsZero() {
			return panel("Ход прогона", []string{"Серий пока нет.", "Выбери нагрузку в плане замеров."}, w, blue)
		}
		label := "Прогон завершён"
		if m.report.Error != "" {
			label = "Прогон завершился с ошибкой"
		}
		return panel(label, []string{"Измерения и протокол — в разделе «Итог»."}, w, blue)
	}
	v := m.current
	title := "Подготовка стенда"
	if v.SuiteTotal > 0 {
		title = fmt.Sprintf("%d/%d  %s", v.SuiteStage, v.SuiteTotal, v.SuiteName)
	} else if v.Phase != "" {
		title = v.Phase
	}
	rows := []string{strip(w, " "+title, ice, titleBG, true)}
	if v.Phase != "" && v.Phase != title && v.SuiteTotal > 0 {
		rows = append(rows, bold(fit(v.Phase, w), ice))
	}
	if v.Detail != "" {
		rows = append(rows, wrap(v.Detail, w)...)
	}
	if v.Total > 0 {
		completed := m.lastSample.Step
		rows = append(rows, fmt.Sprintf("Записано повторов  %d / %d", completed, v.Total), progressBar(min(w, 64), completed, v.Total))
	}
	if !m.started.IsZero() {
		rows = append(rows, ink("От начала  "+time.Since(m.started).Truncate(time.Second).String(), dim))
	}
	if sample := m.lastSample; len(sample.Values) > 0 {
		rows = append(rows, "", bold(fmt.Sprintf("Повтор %d · время операций", sample.Step), blue))
		keys := lab.SortedKeys(sample.Values)
		limit := min(len(keys), max(0, m.bodyRows-len(rows)-1))
		for _, name := range keys[:limit] {
			rows = append(rows, between(w, name, fmt.Sprintf(" %.2f мс", sample.Values[name])))
		}
		if limit < len(keys) {
			rows = append(rows, ink(fmt.Sprintf("Показано %d из %d операций", limit, len(keys)), dim))
		}
	} else if len(m.log) > 1 && m.bodyRows-len(rows) > 3 {
		rows = append(rows, "", ink("Предыдущие этапы", blue))
		end := len(m.log) - 1
		for _, entry := range m.log[max(0, end-(m.bodyRows-len(rows))):end] {
			rows = append(rows, fit(entry, w))
		}
	}
	return strings.Join(rows, "\n")
}

func (m ui) results(w int) string {
	r := m.report
	if len(r.Coverage) > 0 && !m.suiteDetail {
		return m.fullSummary(w)
	}
	if r.Job == lab.RestoreReportTitle {
		return m.restoreDashboard(w)
	}
	if m.phasePos >= len(r.Phases) && len(r.Sections) > 0 {
		return m.sectionView(w)
	}
	if len(r.Phases) == 0 {
		if r.Error != "" {
			rows := wrap("Дальше  "+runNextStep(r.Error, m.file != ""), w-4)
			rows = append(rows, "")
			rows = append(rows, wrap(r.Error, w-4)...)
			return panel("Прогон остановлен", rows, w, red)
		}
		return panel("Результаты", []string{"Серий замеров пока нет."}, w, blue)
	}
	view, _, _ := m.phasePage(w)
	return view
}

func (m ui) phasePage(w int) (string, int, int) {
	r := m.report
	idx := max(0, min(m.phasePos, len(r.Phases)-1))
	ph := r.Phases[idx]
	var data []lab.Row
	for _, row := range lab.Rows(r) {
		if row.Phase == ph.Name {
			data = append(data, row)
		}
	}
	rows := []string{strip(w, fmt.Sprintf(" %d/%d  %s", idx+1, len(r.Phases), ph.Name), ice, titleBG, true)}
	if ph.Source != "" {
		rows = append(rows, fit(ph.Source, w))
	} else if r.Workload != "" {
		rows = append(rows, fit(r.Workload, w))
	}
	if !ph.Complete && (ph.Source != "" || len(r.Coverage) > 0 || r.Error != "") {
		rows = append(rows, ink("Незавершённая серия", gold))
	}
	if len(data) == 0 {
		return strings.Join(append(rows, "Образцы не записаны."), "\n"), 0, 0
	}
	baseline := ph.Baseline
	if baseline == "" && data[0].Compared {
		baseline = r.Phases[0].Name
	}
	if baseline != "" && baseline != ph.Name {
		rows = append(rows, fit(ink("База сравнения · "+baseline, dim), w))
	}
	wide := w >= 80
	rangeWidth := 0
	rangeLabel := ""
	if w >= 105 {
		rangeWidth, rangeLabel = 22, pad("Мин…макс, мс", 22)
	}
	if wide {
		nameWidth := w - 48 - rangeWidth
		rows = append(rows, ink(pad("Операция", nameWidth)+pad("До, мс", 12)+pad("После, мс", 13)+pad("Δ времени", 13)+rangeLabel+"n", blue))
	} else {
		rows = append(rows, ink("Медианы, мс · Δ времени · повторы", blue))
	}
	first := min(max(0, m.operationOffset), len(data)-1)
	blocks := make([][]string, len(data))
	totalLines := 0
	for i, d := range data {
		if wide {
			nameWidth := w - 48 - rangeWidth
			names := wrap(d.Name, nameWidth-1)
			before := "—"
			if d.Compared {
				before = fmt.Sprintf("%.2f", d.BaselineMedian)
			}
			span := ""
			if rangeWidth > 0 {
				span = pad(fmt.Sprintf("%.2f…%.2f", d.Min, d.Max), rangeWidth)
			}
			names[0] = pad(names[0], nameWidth) + pad(before, 12) + pad(fmt.Sprintf("%.2f", d.Median), 13) + pad(deltaText(d), 13) + span + fmt.Sprint(d.N)
			blocks[i] = names
		} else {
			blocks[i] = append(wrap(d.Name, w), fit(timePair(d), w))
		}
		totalLines += len(blocks[i])
	}
	capacity := max(1, m.bodyRows-len(rows))
	paged := totalLines > capacity
	if paged {
		capacity = max(1, capacity-1)
	} else {
		first = 0
	}
	last := first
	for used := 0; last < len(data) && (last == first || used+len(blocks[last]) <= capacity); last++ {
		used += len(blocks[last])
	}
	previous, next := first, first
	if last < len(data) {
		next = last
	}
	for used := 0; previous > 0 && (previous == first || used+len(blocks[previous-1]) <= capacity); previous-- {
		used += len(blocks[previous-1])
	}
	if paged {
		rows = append(rows, m.pageLabel(w, len(rows), "Операции", first, last, len(data), previous, next))
	}
	for _, block := range blocks[first:last] {
		rows = append(rows, block...)
	}
	return strings.Join(rows, "\n"), previous, next
}

func (m *ui) pageResults(direction int) {
	if m.tab == 2 {
		viewport := *m
		height, lead := m.contentHeight(len(m.footer(m.w)))
		viewport.bodyRows = height - lead
		var previous, next int
		switch {
		case !m.suiteDetail && len(m.report.Coverage) > 0:
			_, previous, next = viewport.summaryPage(m.contentWidth())
		case m.phasePos >= 0 && m.phasePos < len(m.report.Phases):
			_, previous, next = viewport.phasePage(m.contentWidth())
		default:
			previous = -1
		}
		if previous >= 0 {
			m.operationOffset = next
			if direction < 0 {
				m.operationOffset = previous
			}
			return
		}
	}
	m.move(6 * direction)
}
