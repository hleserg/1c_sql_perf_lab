package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/hleserg/1c_sql_perf_lab/lab"
)

func restoreStageTitle(stage lab.RestoreStage, failed bool) string {
	if failed {
		switch stage {
		case lab.RestorePrepare, lab.RestoreRecheck:
			return "Ошибка подготовки"
		case lab.RestoreVerify:
			return "Проверка копии прервана"
		case lab.RestoreExclusive:
			return "Ошибка отключения сеансов"
		case lab.RestoreWrite:
			return "Восстановление прервано"
		case lab.RestoreAccess:
			return "Ошибка проверки доступа"
		default:
			return "Ошибка восстановления"
		}
	}
	switch stage {
	case lab.RestoreVerify:
		return "Проверяем резервную копию"
	case lab.RestoreRecheck:
		return "Сверяем файл и базу"
	case lab.RestoreExclusive:
		return "Отключаем сеансы базы"
	case lab.RestoreWrite:
		return "Восстанавливаем базу"
	case lab.RestoreAccess:
		return "Проверяем доступ к базе"
	case lab.RestoreReady:
		return "База восстановлена"
	default:
		return "Готовим восстановление"
	}
}

func restoreImpact(p lab.RestoreProgress, failed bool) string {
	switch p.Stage {
	case lab.RestorePrepare, lab.RestoreRecheck, lab.RestoreVerify:
		if failed {
			return "Сеансы не отключались. Восстановление не начиналось."
		}
		return "Во время проверки сеансы остаются подключёнными."
	case lab.RestoreReady:
		return "Доступ подтверждён: " + p.State
	}
	if p.State != "" {
		return "SQL подтвердил состояние: " + p.State + "."
	}
	if failed && p.Write.Complete {
		return "Файлы восстановлены. Доступ к базе не подтверждён."
	}
	if failed {
		return "Состояние базы не подтверждено."
	}
	switch p.Stage {
	case lab.RestoreExclusive:
		return "Получаем монопольный доступ к базе."
	case lab.RestoreWrite:
		return "SQL записывает файлы базы."
	case lab.RestoreAccess:
		return "Файлы восстановлены. Ожидаем ответ SQL о доступе."
	}
	return ""
}

type restoreAdvice struct{ reason, next string }

func failureAdvice(p lab.RestoreProgress, raw string, legacy bool) restoreAdvice {
	e := strings.ToLower(raw)
	before := p.Stage == lab.RestorePrepare || p.Stage == lab.RestoreVerify || p.Stage == lab.RestoreRecheck
	var a restoreAdvice
	switch {
	case strings.Contains(e, "timed out"), strings.Contains(e, "timeout"), strings.Contains(e, "deadline exceeded"), strings.Contains(e, "время ожидания"):
		a.reason = "SQL Server не ответил вовремя."
		if legacy && p.Stage == lab.RestoreVerify && strings.Contains(e, "pipe io timed out") {
			a.next = "Нажми «Выбрать копию» и укажи тот же .bak. В этой версии снят прежний лимит ожидания ответа SQL."
		} else {
			a.next = "Проверь соединение с SQL Server и повтори проверку копии."
		}
	case strings.Contains(e, "access is denied"), strings.Contains(e, "access denied"), strings.Contains(e, "операционная система вернула ошибку 5"), strings.Contains(e, "operating system error 5("), strings.Contains(e, "отказано в доступе"):
		a.reason = "Отказано в доступе к файлу или папке."
		a.next = "Проверь права службы SQL Server на чтение .bak и запись в папки данных и журнала."
	case strings.Contains(e, "permission"), strings.Contains(e, "denied"), strings.Contains(e, "недостаточно прав"), strings.Contains(e, "login failed"):
		a.reason = "SQL Server отклонил запрос."
		a.next = "Проверь учётную запись и её права на эту операцию в SQL Server."
	case strings.Contains(e, "cannot find"), strings.Contains(e, "no such file"), strings.Contains(e, "не удается найти"), strings.Contains(e, "не удаётся найти"), strings.Contains(e, "operating system error 2("), strings.Contains(e, "operating system error 3("):
		a.reason = "SQL Server не нашёл файл."
		a.next = "Нажми «Выбрать копию» и укажи .bak по пути, доступному на сервере SQL."
	case strings.Contains(e, "disk space"), strings.Contains(e, "space on the disk"), strings.Contains(e, "no space"), strings.Contains(e, "disk is full"), strings.Contains(e, "недостаточно места"), strings.Contains(e, "места на диске"):
		a.reason = "Недостаточно места на диске."
		a.next = "Проверь свободное место на томах для файлов данных и журнала."
	case strings.Contains(e, "checksum"), strings.Contains(e, "media family"), strings.Contains(e, "поврежд"):
		a.reason = "SQL сообщил об ошибке носителя резервной копии."
		a.next = "Выбери другую полную копию и повтори проверку."
	case strings.Contains(e, "context canceled"):
		a.reason = "Операция остановлена."
		a.next = "Для нового запуска нажми «Выбрать копию»."
	default:
		a.reason = "Операция завершилась с ошибкой."
		a.next = "Открой протокол и устрани указанную причину, затем нажми «Выбрать копию»."
	}
	if !before {
		switch {
		case p.State == "RESTORING" || strings.HasPrefix(p.State, "RESTORING /"):
			a.next = "Проверь в SQL Server, продолжается ли RESTORE. Дождись его окончания перед повтором."
		case p.State == "":
			a.next = "Проверь состояние базы и активные RESTORE в SQL Server перед повтором."
		case p.Write.Complete:
			a.next = "Проверь состояние базы и режим доступа в SQL Server."
		default:
			a.next = "Проверь состояние базы в SQL Server. " + a.next
		}
	}
	return a
}

func restoreElapsed(r lab.Report) string {
	if r.Started.IsZero() {
		return ""
	}
	end := r.Finished
	if end.IsZero() {
		end = time.Now()
	}
	d := max(0, int(end.Sub(r.Started).Seconds()))
	if d >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", d/3600, d/60%60, d%60)
	}
	return fmt.Sprintf("%02d:%02d", d/60, d%60)
}

func restoreMeter(title string, m lab.RestoreMeter, active, failed, unknown bool, w int, compact bool) []string {
	status, c := "в очереди", dim
	if active {
		status, c = "ждём SQL", blue
		if failed {
			status, c = "прервано", red
		}
	}
	if !active && failed {
		status = "не начато"
	}
	if unknown {
		status = "нет данных"
	}
	filled := 0
	if m.Percent != nil {
		filled = max(0, min(100, *m.Percent))
		status = fmt.Sprintf("%d%%", filled)
	}
	if m.Complete {
		filled, status, c = 100, "готово", mint
	}
	if !compact && m.Percent != nil && active && failed && !m.Complete {
		status += " · прервано"
	}
	barW := w
	if compact {
		barW = max(4, w-16-11)
	}
	n := barW * filled / 100
	// Both blocks have the same fixed cell width in Windows terminal fonts.
	bar := ink(strings.Repeat("█", n), c) + ink(strings.Repeat("░", barW-n), line)
	if compact {
		return []string{pad(title, 16) + bar + "  " + ink(status, c)}
	}
	return []string{between(w, bold(title, ice), ink(status, c)), bar}
}

func (m ui) restoreDashboard(w int) string {
	lines := strings.Split(m.restoreDashboardContent(w), "\n")
	offset := min(m.phasePos, max(0, len(lines)-max(1, m.bodyRows)))
	return strings.Join(lines[offset:], "\n")
}

func (m *ui) scrollRestoreReport(delta int) {
	w := m.contentWidth()
	foot := m.footer(m.w)
	height, lead := m.contentHeight(len(foot))
	view := *m
	view.bodyRows = max(1, height-lead)
	rows := strings.Count(view.restoreDashboardContent(w), "\n") + 1
	limit := max(0, rows-view.bodyRows)
	m.phasePos = max(0, min(limit, min(m.phasePos, limit)+delta))
}

func (m ui) restoreDashboardContent(w int) string {
	r := m.report
	p := r.RestoreStatus()
	failed := r.Error != ""
	tint := blue
	if failed {
		tint = red
	} else if p.Stage == lab.RestoreReady {
		tint = mint
	}
	title := restoreStageTitle(p.Stage, failed)
	elapsed := restoreElapsed(r)
	compact := m.bodyRows > 0 && m.bodyRows <= 12
	tight := w < 60 || compact
	if w >= 60 && elapsed != "" {
		elapsed = "Время  " + elapsed
	}
	header := strip(w, between(w-1, " "+title, elapsed), tint, titleBG, true)
	impact := restoreImpact(p, failed)
	a := failureAdvice(p, r.Error, r.Restore == nil)
	meters := func(mw int, small bool) []string {
		var rows []string
		for _, i := range []struct {
			title string
			meter lab.RestoreMeter
			stage lab.RestoreStage
		}{
			{"Проверка копии", p.Verify, lab.RestoreVerify}, {"Запись базы", p.Write, lab.RestoreWrite},
		} {
			rows = append(rows, restoreMeter(i.title, i.meter, p.Stage == i.stage, failed, p.Stage == "", mw, small)...)
		}
		return rows
	}
	if tight {
		var rows = []string{header}
		// Keep progress, cause and next action above optional technical details.
		rows = append(rows, meters(w, true)...)
		if failed {
			rows = append(rows, wrap(a.reason, w)...)
			rows = append(rows, wrap(ink(impact, dim), w)...)
			rows = append(rows, wrap(bold("Дальше  ", mint)+a.next, w)...)
		} else {
			rows = append(rows, wrap(impact, w)...)
		}
		if v := r.Inventory["База"]; v != "" {
			rows = append(rows, "База  "+v)
		}
		if failed {
			rows = append(rows, wrap(r.Error, w)...)
		}
		return strings.Join(rows, "\n")
	}
	leftW := w
	wide := w >= 78
	if wide {
		leftW = w * 52 / 100
	}
	progress := panel("Этапы восстановления", meters(leftW-5, false), leftW, blue)
	details := func(width int) string {
		var rows []string
		for _, k := range []string{"База", "SQL", "Файл бэкапа"} {
			if v := r.Inventory[k]; v != "" {
				rows = append(rows, wrap(ink(k+"  ", dim)+v, width-4)...)
			}
		}
		return panel("Назначение и источник", rows, width, blue)
	}
	var lines = []string{header, ""}
	if wide {
		lines = append(lines, columns(w, leftW, progress, details(w-leftW-2)))
	} else {
		lines = append(lines, progress)
	}
	lines = append(lines, "")
	if failed {
		lines = append(lines, bold(a.reason, red))
		lines = append(lines, wrap(impact, w)...)
		lines = append(lines, "", bold("Что делать дальше", mint))
		lines = append(lines, wrap(a.next, w)...)
		lines = append(lines, "", ink("Сообщение операции", dim))
		lines = append(lines, wrap(r.Error, w)...)
	} else {
		lines = append(lines, wrap(impact, w)...)
	}
	if !wide {
		lines = append(lines, "", details(w))
	}
	return strings.Join(lines, "\n")
}
