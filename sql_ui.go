package main

import (
	"fmt"
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
	rows := []string{}
	for i, j := range lab.Jobs {
		pointer := "  "
		if i == m.jobPos {
			pointer = bold("› ", mint)
		}
		rows = append(rows, pointer+j.Name)
	}
	rows = append(rows, "")
	rows = append(rows, wrap(lab.Jobs[m.jobPos].Detail, w-5)...)
	rows = append(rows, "", ink("Enter  выполнить выбранную работу", mint))
	return panel("Работы SQL", rows, w, blue)
}
func (m ui) sectionView(w int) string {
	idx := min(len(m.report.Sections)-1, max(0, m.phasePos-len(m.report.Phases)))
	s := m.report.Sections[idx]
	rows := []string{ink(fmt.Sprintf("↑↓  Раздел %d / %d · №%d", idx+1, len(m.report.Sections), s.ID), dim)}
	rows = append(rows, wrap(s.Title, w-5)...)
	if s.Error != "" {
		rows = append(rows, wrap(s.Error, w-5)...)
	}
	if len(s.Rows) == 0 && s.Error == "" {
		rows = append(rows, "Нет строк")
	}
	if len(s.Rows) > 0 {
		rows = append(rows, "")
		for i, v := range s.Rows[0] {
			if i >= len(s.Columns) {
				break
			}
			rows = append(rows, fit(s.Columns[i]+"  "+strings.TrimSpace(v), w-4))
			if len(rows) >= max(5, m.h-12) {
				break
			}
		}
	}
	rows = append(rows, "", fmt.Sprintf("Строк: %d   o  полный HTML", len(s.Rows)))
	return panel("Результат SQL", rows, w, blue)
}
