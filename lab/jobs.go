package lab

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Job struct {
	Key, Name, Detail string
	IDs               []int
}

var FullJob = Job{"all", "Полный прогон стенда", "Паспорт, отдельные и совместные замеры с откатом, целостность, обслуживание и копии. Результат по каждому пункту.", nil}
var RestoreJob = Job{"restore", "Восстановить базу из .bak", "Выбранная полная SQL-копия заменит базу стенда. По умолчанию выбран свежий файл в D:\\temp.", []int{15}}
var Jobs = []Job{
	{"inspect", "Снять паспорт SQL", "Сведения о восстановленной базе и экземпляре, включая VLF, tempdb и I/O за 5 секунд. Достаточно SQL.", InspectionIDs()},
	{"stats", "Обновить изменившуюся статистику", "До 20 статистик на крупных таблицах. Обновлённые статистики остаются в копии.", []int{3}},
	{"stats-erp", "Статистика с замером ERP", "Одинаковые серии ERP до и после выборочного обновления. Изменение сохраняется.", []int{3}},
	{"indexes", "Обслужить выбранные индексы", "На восстановленной базе. Отбор по использованию, размеру и фрагментации. Изменения сохраняются в копии.", []int{32}},
	{"checkdb", "Проверить восстановленную базу", "Полный DBCC CHECKDB. Может занять длительное время. Исправление данных отключено.", []int{15}},
	{"backup", "Сравнить резервные копии", "Создаёт на стороне SQL две полные COPY_ONLY копии с CHECKSUM и проверяет каждую. Обе остаются в указанной папке.", []int{14}},
}

func FindJob(key string) (Job, bool) {
	if key == FullJob.Key {
		return FullJob, true
	}
	for _, j := range Jobs {
		if j.Key == key {
			return j, true
		}
	}
	return Job{}, false
}

type sqlJobs interface {
	Work(context.Context, string, func(Section) error, func(string)) error
}

func (r Run) job(ctx context.Context, key string, report *Report, path string, measure func(string, []int) error) error {
	j, ok := FindJob(key)
	if !ok {
		return fmt.Errorf("неизвестная работа SQL: %q", key)
	}
	adapter, ok := r.Backend.(sqlJobs)
	if !ok {
		return errors.New("режим работ SQL недоступен")
	}
	report.Job = j.Name
	record := func(s Section) error {
		report.Sections = append(report.Sections, s)
		r.say(Event{Phase: j.Name, Detail: s.Title, Section: &s})
		return SaveJSON(path, report)
	}
	progress := func(msg string) { r.say(Event{Phase: j.Name, Detail: msg}) }
	if key == "stats-erp" {
		if e := measure("До обновления статистики", nil); e != nil {
			return e
		}
	}
	if e := adapter.Work(ctx, key, record, progress); e != nil {
		return e
	}
	if key == "stats-erp" && !report.Demo {
		changed := maintenanceChanged(report.Sections, "Обновлено")
		if !changed {
			report.Journal = append(report.Journal, "Кандидатов для обновления не осталось. Эффект не измерялся.")
			return nil
		}
	}
	if key == "stats-erp" {
		return measure("После обновления статистики", []int{3})
	}
	return ctx.Err()
}
func maintenanceChanged(sections []Section, result string) bool {
	for _, s := range sections {
		if len(s.Columns) > 0 && s.Columns[0] == "Результат" && len(s.Rows) > 0 && len(s.Rows[0]) > 0 && s.Rows[0][0] == result {
			return true
		}
	}
	return false
}
func (d *Demo) Work(ctx context.Context, key string, save func(Section) error, note func(string)) error {
	j, _ := FindJob(key)
	for _, id := range j.IDs {
		title := ByID(id).Title
		note(title)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.Delay):
		}
		s := Section{ID: id, Title: title, Baseline: id == 35 || id == 36, Columns: []string{"Пример", "Результат"}, Rows: [][]string{{"Синтетическая проверка", "Выполнена"}}}
		switch key {
		case "stats", "stats-erp":
			s.Columns = []string{"Результат"}
			s.Rows = [][]string{{"Обновлено"}}
		case "indexes":
			s.Columns = []string{"Результат"}
			s.Rows = [][]string{{"Обслужено"}}
		}
		if e := save(s); e != nil {
			return e
		}
	}
	return nil
}
func (l *Live) Work(ctx context.Context, key string, save func(Section) error, note func(string)) error {
	switch key {
	case "inspect":
		var failures []error
		for _, c := range inspections {
			if e := ctx.Err(); e != nil {
				return e
			}
			note(c.title)
			bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
			var s Section
			if c == blockingCheck {
				s = l.BlockingEvidence(bounded)
			} else {
				s = fetchSection(bounded, l.conn, c)
			}
			cancel()
			if s.Error != "" {
				failures = append(failures, fmt.Errorf("№%d · %s", c.id, s.Error))
			}
			if e := save(s); e != nil {
				return e
			}
		}
		note("Измеряем чтение и запись за 5 секунд")
		s := l.ioWindow(ctx)
		if e := save(s); e != nil {
			return e
		}
		if s.Error != "" {
			failures = append(failures, errors.New(s.Error))
		}
		return errors.Join(failures...)
	case "stats", "stats-erp":
		return l.updateStats(ctx, save, note)
	case "indexes":
		return l.maintainIndexes(ctx, save, note)
	case "backup":
		return l.backupTrial(ctx, save, note)
	case "checkdb":
		note("SQL выполняет DBCC CHECKDB")
		s := fetchSection(ctx, l.conn, check{15, "Полная проверка целостности", `DBCC CHECKDB WITH TABLERESULTS, ALL_ERRORMSGS, NO_INFOMSGS`})
		if s.Error == "" && len(s.Rows) > 0 {
			s.Error = "DBCC вернул диагностические строки, разбери сообщения в протоколе"
		}
		if s.Error == "" {
			s.Columns = []string{"Результат", "Время, мс"}
			s.Rows = [][]string{{"Проверка завершена, ошибок целостности не выдано", fmt.Sprint(s.ElapsedMS)}}
		}
		if e := save(s); e != nil {
			return e
		}
		if s.Error != "" {
			return errors.New(s.Error)
		}
		return nil
	}
	return fmt.Errorf("неизвестная работа %q", key)
}
