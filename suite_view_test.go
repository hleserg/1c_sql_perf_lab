package main

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/hleserg/1c_sql_perf_lab/lab"
	"strings"
	"testing"
)

func comparisonReport() lab.Report {
	base, changed := map[string]float64{}, map[string]float64{}
	for i := 1; i <= 16; i++ {
		name := fmt.Sprintf("Операция %02d", i)
		base[name], changed[name] = 100, 80
	}
	changed["Операция 02"] = 100
	samples := func(v map[string]float64) []map[string]float64 { return []map[string]float64{v, v, v} }
	return lab.Report{
		SuiteComplete: true, Workload: "Чтение заказов 1С",
		Coverage: []lab.ItemResult{{ID: 1, Title: "Аудит", Status: "Собраны сведения"}, {ID: 19, Title: "Параллелизм", Status: "Измерено с откатом"}},
		Phases: []lab.Phase{
			{Name: "Исходная", Source: "Чтение заказов 1С", Raw: samples(base), Complete: true},
			{Name: "Параллелизм", Source: "Чтение заказов 1С", Baseline: "Исходная", IDs: []int{19}, Raw: samples(changed), Complete: true},
		},
	}
}

func TestSuiteOpensAtIndividualComparisonAndShowsMeasuredTime(t *testing.T) {
	m := newUI(t.TempDir())
	m.w, m.h, m.bodyRows, m.tab = 44, 14, 8, 2
	m.report = comparisonReport()
	m.suitePos = initialSuitePosition(m.report)
	view := ansi.Strip(m.fullSummary(40))
	for _, part := range []string{"Чтение заказов 1С", "Парный замер у 1 из 2 пунктов", "Операция 01", "100.00", "80.00", "-20.0%"} {
		if !strings.Contains(view, part) {
			t.Fatalf("compact summary misses %q\n%s", part, view)
		}
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.suiteDetail || m.phasePos != 1 {
		t.Fatal("Enter does not open the selected comparison")
	}
}

func TestResultPagesReachEveryOperationWithoutChangingPhase(t *testing.T) {
	m := newUI(t.TempDir())
	m.w, m.h, m.tab, m.phasePos, m.suiteDetail = 44, 14, 2, 1, true
	m.report = comparisonReport()
	seen := ""
	for i := 0; i < 20; i++ {
		seen += ansi.Strip(m.View()) + "\n"
		m = press(m, tea.KeyMsg{Type: tea.KeyPgDown})
		if m.phasePos != 1 {
			t.Fatal("paging changed series")
		}
	}
	for i := 1; i <= 16; i++ {
		if name := fmt.Sprintf("Операция %02d", i); !strings.Contains(seen, name) {
			t.Fatalf("%s never visible", name)
		}
	}
	if !strings.Contains(seen, "0.0%") || !strings.Contains(seen, "-20.0%") {
		t.Fatal("equal times and shorter times must be distinguishable")
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.phasePos != 0 || !strings.Contains(ansi.Strip(m.View()), "Операция 01") {
		t.Fatal("series navigation must reset the operation page")
	}
}

func TestSuiteNeverAttributesCombinedAndIncompleteTrialsToOneItem(t *testing.T) {
	m := newUI(t.TempDir())
	m.report = comparisonReport()
	m.report.Phases[1].IDs = []int{19, 24}
	incomplete := m.report.Phases[1]
	incomplete.Name, incomplete.IDs, incomplete.Complete = "Незаконченная", []int{19}, false
	m.report.Phases = append(m.report.Phases, incomplete)
	m.suitePos, m.bodyRows = 1, 16
	v := ansi.Strip(m.fullSummary(80))
	if !strings.Contains(v, "Парный замер у 0 из 2 пунктов") || strings.Contains(v, "-20.0%") {
		t.Fatal(v)
	}
}

func TestResultMousePagingAndEscapeReturnToSelectedItem(t *testing.T) {
	m := newUI(t.TempDir())
	m.w, m.h, m.tab, m.phasePos, m.suitePos, m.suiteDetail = 44, 14, 2, 1, 1, true
	m.report = comparisonReport()
	m, _ = pointer(t, m, "  ›", tea.MouseButtonLeft)
	if m.operationOffset <= 0 || m.phasePos != 1 {
		t.Fatal("mouse did not page the operations")
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.suiteDetail || m.suitePos != 1 || m.focus != contentArea || m.operationOffset != 0 {
		t.Fatal("Escape must restore selection in the summary")
	}
}

func TestLongPortfolioNamesKeepTheDocumentVisible(t *testing.T) {
	for _, width := range []int{44, 86} {
		m := newUI(t.TempDir())
		m.w, m.h, m.tab, m.phasePos, m.suiteDetail = width, 14, 2, 1, true
		m.report = comparisonReport()
		for i := range m.report.Phases {
			for j := range m.report.Phases[i].Raw {
				m.report.Phases[i].Raw[j] = map[string]float64{"Сводка по проведённости за 30 дней · ДвижениеПродукцииИМатериалов": 9}
			}
		}
		v := ansi.Strip(m.View())
		if !strings.Contains(v, "ДвижениеПродукцииИМатериалов") || !strings.Contains(v, "9.00") {
			t.Fatalf("%d columns obscure the measured document\n%s", width, v)
		}
	}
}

func TestLongDiagnosticCanBeReadToEnd(t *testing.T) {
	m := newUI(t.TempDir())
	m.w, m.h, m.tab = 44, 14, 2
	m.report = lab.Report{
		Workload: "1С · исходный замер не получен",
		Error:    "Подготовка подключения 1С: " + strings.Repeat("Длинное сообщение драйвера. ", 25) + "SQLSTATE 08001",
		Coverage: []lab.ItemResult{{ID: 11}},
	}
	for i := 0; i < 20 && !strings.Contains(ansi.Strip(m.View()), "08001"); i++ {
		m = press(m, tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if !strings.Contains(ansi.Strip(m.View()), "08001") || m.suitePos != 0 {
		t.Fatal("full diagnostic must remain readable in compact view", m.View())
	}
}

func TestBenchCountsCompletedSamplesAndClearsPreviousSeries(t *testing.T) {
	m := newUI(t.TempDir())
	m.running, m.tab, m.bodyRows = true, 1, 20
	emit := func(v lab.Event) { changed, _ := m.Update(streamMsg{v}); m = changed.(ui) }
	measure := lab.Event{Phase: "Исходная", Detail: "Замер", Step: 1, Total: 3}
	emit(measure)
	if !strings.Contains(ansi.Strip(m.bench(80)), "0 / 3") {
		t.Fatal("starting a sample incorrectly advances progress")
	}
	measure.Values = map[string]float64{"Чтение заказа": 55.5, "Список": 10}
	emit(measure)
	measure.Step, measure.Values = 2, nil
	emit(measure)
	v := ansi.Strip(m.bench(80))
	for _, want := range []string{"1 / 3", "Чтение заказа", "Список", "55.50 мс"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q in %s", want, v)
		}
	}
	measure.Phase, measure.Step = "После изменения", 1
	emit(measure)
	v = ansi.Strip(m.bench(80))
	if strings.Contains(v, "55.50 мс") || !strings.Contains(v, "0 / 3") {
		t.Fatal(v)
	}
}

func TestFullRunVisibleOnPlanAndRestoredResult(t *testing.T) {
	for _, size := range [][2]int{{44, 14}, {100, 22}, {170, 38}} {
		for _, tab := range []int{0, 2} {
			m := newUI(t.TempDir())
			m.w, m.h, m.tab, m.demo = size[0], size[1], tab, false
			m.report.Job = lab.RestoreReportTitle
			m.report.Restore = &lab.RestoreProgress{Stage: lab.RestoreReady, State: "ONLINE / MULTI_USER"}
			out, cmd := commandClick(t, m, "full")
			if cmd == nil || !out.running || out.confirm || out.tab != 1 {
				t.Fatal("command must start on saved stand", size, tab)
			}
			out.cancel()
		}
	}
}

func TestProfileInstanceTrialsCanBeToggledWithoutTyping(t *testing.T) {
	m := newUI(t.TempDir())
	f, err := formFor(profilePath(m.root))
	if err != nil {
		t.Fatal(err)
	}
	m.form = f
	f.pos = 26
	m.formKey(tea.KeyMsg{Type: tea.KeyEnter})
	f.pos = 28
	m.formKey(tea.KeyMsg{Type: tea.KeySpace})
	if f.editing {
		t.Fatal("switch opened text entry")
	}
	m.formKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	p, err := lab.Load(profilePath(m.root))
	if err != nil || !p.IsolatedInstanceTrials || !p.TestAdHocWorkloads || !m.liveProfile.TestAdHocWorkloads {
		t.Fatal(p, err)
	}
}

func TestERPRepairFromPlanAndOldFailedReport(t *testing.T) {
	for _, tab := range []int{0, 2} {
		for _, size := range [][2]int{{44, 14}, {148, 42}} {
			m := newUI(t.TempDir())
			m.demo, m.tab, m.w, m.h = false, tab, size[0], size[1]
			m.report = lab.Report{Coverage: []lab.ItemResult{{ID: 1, Title: "Мониторинг"}}, Workload: "1С · исходный замер не получен", Error: "RAS: refused. COM: 0x8002801d"}
			if tab == 2 && size[0] > 80 && !strings.Contains(ansi.Strip(m.View()), "библиотека типов COM") {
				t.Fatal("cause missing", m.View())
			}
			next, _ := commandClick(t, m, "erp-repair")
			if !next.confirm || next.running || next.chosenJob != "erp-repair" {
				t.Fatal("incorrect repair action", size, tab)
			}
			out, _ := next.Update(tea.KeyMsg{Type: tea.KeyEsc})
			if out.(ui).confirm || out.(ui).running {
				t.Fatal("cancel started work")
			}
		}
	}
}

func TestStrictReportOpensAtFailedWorkload(t *testing.T) {
	r := lab.Report{Error: "COM: 0x8002801d", Workload: "1С · исходный замер не получен", Coverage: []lab.ItemResult{{ID: 1, Title: "Мониторинг"}, {ID: 11, NextAction: "Служебная подсказка про нагрузочные испытания"}}}
	m := newUI(t.TempDir())
	m.report, m.suitePos, m.bodyRows = r, initialSuitePosition(r), 20
	view := ansi.Strip(m.fullSummary(94))
	if m.suitePos != 1 || strings.Contains(view, r.Coverage[1].NextAction) || !strings.Contains(view, "Исправить подключение 1С") {
		t.Fatal(view)
	}
}
