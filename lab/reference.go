package lab

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type approvedSnapshot struct {
	Version                 int
	SQL, Database, Approved string
	Rows                    map[string][]string
}

func referencePath(root string, p Profile) string {
	return filepath.Join(root, "local", "reference-"+p.Key()+".json")
}

func configSnapshot(p Profile, r Report) (approvedSnapshot, error) {
	s := approvedSnapshot{Version: 1, SQL: p.ExpectedServer, Database: p.Database, Rows: map[string][]string{}}
	if r.Demo || !strings.EqualFold(r.Inventory["SQL"], s.SQL) || !strings.EqualFold(r.Inventory["База"], s.Database) {
		return s, errors.New("паспорт не соответствует выбранному стенду")
	}
	for _, item := range r.Sections {
		if !item.Baseline {
			continue
		}
		if item.Error != "" {
			return s, fmt.Errorf("%s: %s", item.Title, item.Error)
		}
		rows := make([]string, 0, len(item.Rows))
		for _, row := range item.Rows {
			b, e := json.Marshal(row)
			if e != nil {
				return s, e
			}
			rows = append(rows, string(b))
		}
		sort.Strings(rows)
		s.Rows[item.Title] = rows
	}
	if len(s.Rows) == 0 {
		return s, errors.New("в паспорте отсутствуют настройки для сравнения")
	}
	return s, nil
}

// Approving is a separate explicit command. A routine stand run never changes the reference.
func ApproveReference(root string, p Profile, r Report) (string, error) {
	s, err := configSnapshot(p, r)
	if err != nil {
		return "", err
	}
	if r.Error != "" {
		return "", errors.New("паспорт содержит ошибки; прежний эталон сохранён")
	}
	s.Approved = time.Now().Format(time.RFC3339)
	path := referencePath(root, p)
	return path, SaveJSON(path, s)
}
func compareBaseline(root string, p Profile, current Report) Section {
	result := Section{ID: 35, Title: "Сравнение с утверждённым эталоном", Columns: []string{"Раздел", "Изменение", "Значения"}}
	b, err := os.ReadFile(referencePath(root, p))
	if errors.Is(err, os.ErrNotExist) {
		result.Error = "Нет эталона. После проверки настроек выполни perf-lab -approve-baseline."
		return result
	}
	if err != nil {
		result.Error = err.Error()
		return result
	}
	var before approvedSnapshot
	if err = json.Unmarshal(b, &before); err != nil {
		result.Error = err.Error()
		return result
	}
	if before.Version != 1 || !strings.EqualFold(before.SQL, p.ExpectedServer) || !strings.EqualFold(before.Database, p.Database) || len(before.Rows) == 0 {
		result.Error = "Файл эталона не соответствует стенду или формату"
		return result
	}
	now, err := configSnapshot(p, current)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	titles := map[string]bool{}
	for t := range before.Rows {
		titles[t] = true
	}
	for t := range now.Rows {
		titles[t] = true
	}
	sorted := make([]string, 0, len(titles))
	for t := range titles {
		sorted = append(sorted, t)
	}
	sort.Strings(sorted)
	for _, title := range sorted {
		a, was := before.Rows[title]
		b, is := now.Rows[title]
		if !was || !is {
			status := "Добавлен раздел"
			if !is {
				status = "Раздел отсутствует"
			}
			result.Rows = append(result.Rows, []string{title, status, ""})
			continue
		}
		for i, j := 0, 0; i < len(a) || j < len(b); {
			switch {
			case i < len(a) && j < len(b) && a[i] == b[j]:
				i++
				j++
			case j == len(b) || i < len(a) && a[i] < b[j]:
				result.Rows = append(result.Rows, []string{title, "Было", readReferenceRow(a[i])})
				i++
			default:
				result.Rows = append(result.Rows, []string{title, "Стало", readReferenceRow(b[j])})
				j++
			}
		}
	}
	if len(result.Rows) == 0 {
		result.Rows = [][]string{{"Все разделы", "Совпадают", before.Approved}}
	}
	return result
}
func readReferenceRow(s string) string {
	var v []string
	if json.Unmarshal([]byte(s), &v) == nil {
		return strings.Join(v, " · ")
	}
	return s
}
