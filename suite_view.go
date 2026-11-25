package main

import (
	"fmt"
	"github.com/hleserg/1c_sql_perf_lab/lab"
	"strings"
)

func (m ui) fullSummary(w int) string {
	r := m.report
	pos := max(0, min(m.suitePos, len(r.Coverage)-1))
	selected := r.Coverage[pos]
	title := "Полный прогон · результат"
	if r.Error != "" {
		title = "Полный прогон · есть ошибки"
	}
	rows := []string{strip(w, " "+title, ice, titleBG, true)}
	blocked := r.Workload == "1С · исходный замер не получен" && r.Error != ""
	if blocked {
		title = "Остановлено · нет исходного замера 1С"
		rows[0] = strip(w, " "+title, ice, titleBG, true)
		cause := strings.TrimPrefix(r.Error, "Прогон через 1С остановлен до изменения настроек: ")
		if strings.HasPrefix(cause, "Подготовка подключения 1С: ") {
			cause = strings.TrimPrefix(cause, "Подготовка подключения 1С: ")
		} else if strings.Contains(strings.ToLower(cause), "8002801d") {
			cause = "Windows: библиотека типов COM 1С не зарегистрирована (0x8002801D)."
		}
		rows = append(rows, wrap(ink(cause, gold), w)...)
	} else if r.Workload != "" {
		rows = append(rows, wrap("Нагрузка  "+r.Workload, w)...)
	}
	rows = append(rows, wrap(bold(lab.Label(selected.ID)+"  "+selected.Title, mint), w)...)
	rows = append(rows, wrap(selected.Status+" · "+selected.Reason, w)...)
	if selected.NextAction != "" && m.bodyRows >= 12 {
		rows = append(rows, wrap(ink(selected.NextAction, dim), w)...)
	}
	visible := max(2, min(15, m.bodyRows-len(rows)-1))
	from := max(0, min(len(r.Coverage)-visible, pos-visible/2))
	for i := from; i < min(from+visible, len(r.Coverage)); i++ {
		item := r.Coverage[i]
		m.mark(0, len(rows), w, "coverage", i)
		line := between(w-4, lab.Label(item.ID)+"  "+item.Title, item.Status)
		rows = append(rows, activeRow(w-1, line, i == pos))
	}
	rows = append(rows, ink(fmt.Sprintf("%d из %d   ↑ / ↓ по пунктам", pos+1, len(r.Coverage)), dim))
	return strings.Join(rows, "\n")
}
