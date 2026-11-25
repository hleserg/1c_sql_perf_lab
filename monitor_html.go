package main

import (
	"fmt"
	"html/template"
	"os"
	"path/filepath"
)

var dailyMonitorPage = template.Must(template.New("day").Parse(`<!doctype html><html lang="ru"><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>SQL · {{.DateUTC}} UTC</title>
<style>body{font:16px system-ui,sans-serif;max-width:95rem;margin:2rem auto;padding:0 1rem;color:#17272c}section{overflow:auto}table{border-collapse:collapse;width:100%;font-variant-numeric:tabular-nums}th,td{padding:.55em;text-align:right;border-bottom:1px solid #ced9dc}th:first-child,td:first-child{text-align:left}th{background:#eef3f3}td.error{text-align:left;color:#952a25}</style>
<h1>Мониторинг SQL · {{.DateUTC}} UTC</h1>
<p>{{.Profile}} · {{.ExpectedServer}} · {{.Database}}</p>
<p>Замеров с данными: {{.Successful}} из {{len .Samples}}. Интервал сбора — {{.IntervalMinutes}} минут. Пропуски видны по времени замеров.</p>
<section><table><thead><tr><th>Время UTC</th><th>CPU SQL, ядер³</th><th>Память SQL, МиБ</th><th>Доступно ОС, МиБ</th><th>Сеансы экземпляра¹</th><th>Запросы базы, заблокированные другим сеансом</th><th>Чтения²</th><th>Записи²</th><th>Ожидание чтения², мс</th><th>Ожидание записи², мс</th></tr></thead><tbody>
{{range $i, $sample := .Samples}}<tr><td>{{.UTC.UTC.Format "15:04:05"}}</td>{{with .Reading}}<td>{{$.AverageCores $i}}</td><td>{{.SQLMemoryMiB}}</td><td>{{.AvailableOSMemoryMiB}}</td><td>{{.InstanceUserSessions}}</td><td>{{.BlockedDBRequests}}</td><td>{{.DBReads}}</td><td>{{.DBWrites}}</td><td>{{.DBReadWaitMS}}</td><td>{{.DBWriteWaitMS}}</td>{{else}}<td class="error" colspan="9">{{.Error}}</td>{{end}}</tr>{{end}}</tbody></table></section>
<p>¹ Пользовательские сеансы, кроме соединения самого монитора. ² Накопительные счётчики выбранной базы. Для сравнения соседних замеров учитывай перезапуск SQL, восстановление базы и изменения состава файлов. ³ Среднее число занятых логических ядер между двумя замерами процесса SQL. После перезапуска отсчёт начинается заново.</p>
{{range $sample := .Samples}}{{with .Blocking}}<details><summary>Блокировки SQL и сеансы 1С · {{$sample.UTC.UTC.Format "15:04:05"}}</summary>{{if .Error}}<p>{{.Error}}</p>{{end}}<section><table><thead><tr>{{range .Columns}}<th>{{.}}</th>{{end}}</tr></thead><tbody>{{range .Rows}}<tr>{{range .}}<td>{{.}}</td>{{end}}</tr>{{end}}</tbody></table></section></details>{{end}}{{end}}
<p><a href="{{.DateUTC}}.json">Исходные измерения, идентификатор базы и время запуска SQL</a></p></html>`))

func (d monitorDay) Successful() int {
	count := 0
	for _, s := range d.Samples {
		if s.Reading != nil {
			count++
		}
	}
	return count
}

func (d monitorDay) AverageCores(i int) string {
	if i < 1 {
		return "—"
	}
	current, previous := d.Samples[i], d.Samples[i-1]
	r, b := current.Reading, previous.Reading
	elapsed := current.UTC.Sub(previous.UTC).Milliseconds()
	if r == nil || b == nil || r.SQLStartedServerTime != b.SQLStartedServerTime || r.SQLCPUTimeMS < b.SQLCPUTimeMS || elapsed <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.2f", float64(r.SQLCPUTimeMS-b.SQLCPUTimeMS)/float64(elapsed))
}

func writeMonitorHTML(dir string, d monitorDay) error {
	f, err := os.CreateTemp(dir, ".monitor-html-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = dailyMonitorPage.Execute(f, d)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(dir, d.DateUTC+".html"))
}
