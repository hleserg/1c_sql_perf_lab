package main

import (
	"fmt"
	"github.com/charmbracelet/lipgloss"
	"github.com/hleserg/1c_sql_perf_lab/lab"
	"strings"
)

func sqlJobIndex(id int) int {
	for i, j := range lab.Jobs {
		for _, item := range j.IDs {
			if item == id && j.Key != "inspect" {
				return i
			}
		}
	}
	for _, item := range lab.Jobs[0].IDs {
		if item == id {
			return 0
		}
	}
	return -1
}
func (m ui) jobsView(w int) string {
	jobs := availableSQLJobs()
	pos := min(m.jobPos, len(jobs)-1)
	listW := w
	if w >= 76 {
		listW = w * 56 / 100
	}
	var rows []string
	for i, j := range jobs {
		m.mark(0, len(rows)+1, listW, "job", i)
		rows = append(rows, activeRow(listW-3, j.Name, i == pos))
	}
	list := panel("Работы SQL", rows, listW, mint)
	job := jobs[pos]
	if listW == w {
		return list + "\n" + strings.Join(wrap(job.Detail, w), "\n")
	}
	rw := w - listW - 2
	details := append([]string{bold(job.Name, ice), ""}, wrap(job.Detail, rw-5)...)
	return columns(w, listW, list, panel("Что будет выполнено", details, rw, blue))
}
func (m ui) sectionView(w int) string {
	idx := min(len(m.report.Sections)-1, max(0, m.phasePos-len(m.report.Phases)))
	s := m.report.Sections[idx]
	rows := []string{bold(s.Title, ice), ink(fmt.Sprintf("Раздел %d / %d    Мероприятие %d    Строк %d", idx+1, len(m.report.Sections), s.ID, len(s.Rows)), dim), ""}
	if s.Error != "" {
		rows = append(rows, wrap("Дальше  "+runNextStep(s.Error, m.file != ""), w-5)...)
		for _, line := range wrap(s.Error, w-5) {
			rows = append(rows, ink(line, gold))
		}
	}
	if len(s.Rows) == 0 && s.Error == "" {
		rows = append(rows, "Нет строк")
	}
	if len(s.Rows) > 0 && len(s.Columns) > 0 {
		// Fit a useful prefix of the data table. The complete result is in HTML.
		cols := min(len(s.Columns), max(1, w/17))
		widths := make([]int, cols)
		for i := range widths {
			widths[i] = lipgloss.Width(s.Columns[i])
			for _, row := range s.Rows[:min(len(s.Rows), max(2, m.h-8))] {
				if i < len(row) {
					widths[i] = max(widths[i], lipgloss.Width(row[i]))
				}
			}
			widths[i] = min(widths[i], (w-5)/cols-2)
		}
		line := func(row []string) string {
			var cells []string
			for i, width := range widths {
				v := ""
				if i < len(row) {
					v = strings.TrimSpace(row[i])
				}
				cells = append(cells, pad(v, width))
			}
			return strings.Join(cells, "  ")
		}
		rows = append(rows, ink(line(s.Columns), blue))
		for _, row := range s.Rows[:min(len(s.Rows), max(2, m.h-12))] {
			rows = append(rows, line(row))
		}
		if len(s.Columns) > cols || len(s.Rows) > max(2, m.h-12) {
			rows = append(rows, ink("Полная таблица — в отчёте HTML", dim))
		}
	}
	return panel("Результат SQL", rows, w, blue)
}
