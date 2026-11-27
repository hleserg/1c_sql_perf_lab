package lab

import (
	"fmt"
	"html/template"
	"maps"
	"math"
	"os"
	"slices"
)

type Row struct {
	Name, Phase, Baseline   string
	N                       int
	Median, Min, Max, Delta float64
	BaselineMedian          float64
	Compared                bool
}

func Rows(rep Report) []Row {
	type phaseKey struct{ source, name string }
	refs := make(map[phaseKey]map[string]float64)
	// Older stand reports may lack completion flags and named references.
	legacy := len(rep.Coverage) == 0 && rep.Samples > 0 && !rep.Finished.IsZero() && rep.Error == "" &&
		!slices.ContainsFunc(rep.Phases, func(p Phase) bool { return p.Complete || p.Source != "" || p.Baseline != "" })
	var out []Row
	for i, p := range rep.Phases {
		baseline := p.Baseline
		if i > 0 && baseline == "" && len(rep.Coverage) == 0 {
			baseline = rep.Phases[0].Name
		}
		observed := make(map[string][]float64)
		for _, sample := range p.Raw {
			for name, ms := range sample {
				if ms > 0 && !math.IsInf(ms, 0) {
					observed[name] = append(observed[name], ms)
				}
			}
		}
		complete := (p.Complete || legacy) && (rep.Samples == 0 || len(p.Raw) == rep.Samples)
		medians := make(map[string]float64)
		ref := refs[phaseKey{p.Source, baseline}]
		for _, name := range slices.Sorted(maps.Keys(observed)) {
			values := observed[name]
			ms := Median(values)
			row := Row{Name: name, Phase: p.Name, Baseline: baseline, N: len(values), Median: ms, Min: slices.Min(values), Max: slices.Max(values)}
			if complete && len(values) == len(p.Raw) {
				medians[name] = ms
				if b := ref[name]; baseline != "" && baseline != p.Name && b > 0 {
					row.BaselineMedian, row.Compared = b, true
					row.Delta = (ms/b - 1) * 100
				}
			}
			out = append(out, row)
		}
		refs[phaseKey{p.Source, p.Name}] = medians
	}
	return out
}
func Delta(v float64) string {
	if math.Abs(v) < .05 {
		return "0.0%"
	}
	return fmt.Sprintf("%+.1f%%", v)
}

var htmlPage = template.Must(template.New("r").Funcs(template.FuncMap{"delta": Delta}).Parse(`<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Протокол · SQL Perf Lab</title><style>*{box-sizing:border-box}body{font:16px system-ui,sans-serif;background:#10191f;color:#e7f1f1;max-width:1120px;margin:auto;padding:5vw}h1{font-size:clamp(2rem,6vw,4rem);margin:.3em 0}small,th{color:#92b0b6}b{color:#6ce3ca}section{overflow:auto}table{width:100%;border-collapse:collapse}td,th{padding:.7em;text-align:left;border-bottom:1px solid #32454d}td{font-variant-numeric:tabular-nums;vertical-align:top}td:last-child{overflow-wrap:anywhere}li{padding:.25em}pre{white-space:pre-wrap}a{color:#75d6e6}@media print{body{color:#111;background:white}small,th,b{color:#333}}</style><small>1C / SQL PERF LAB</small><h1>Протокол замеров</h1><b>{{if .R.Demo}}ДЕМО · СИНТЕТИЧЕСКИЕ ДАННЫЕ{{else}}{{.R.Profile}}{{end}}</b><p>{{.R.Started.Format "02.01.2006 15:04:05 MST"}} · {{.R.Samples}} повторов · прогревов: {{.R.Warmup}} · сеансов: {{.R.Sessions}}</p>{{if .R.Error}}<h2>Ошибки прогона</h2><pre>{{.R.Error}}</pre>{{end}}{{if .R.Workload}}<p>Нагрузка · {{.R.Workload}}</p>{{end}}{{if .R.Job}}<h2>{{.R.Job}}</h2>{{if .R.Coverage}}<section><h2>Что выполнено по плану</h2><table><tr><th>№</th><th>Пункт</th><th>Статус</th><th>Что установлено и что дальше</th></tr>{{range .R.Coverage}}<tr><td>{{.ID}}</td><td>{{.Title}}</td><td>{{.Status}}</td><td>{{.Reason}}{{if .NextAction}}<p>{{.NextAction}}</p>{{end}}{{if .Missing}}<details><summary>Недоступные проверки</summary><ul>{{range .Missing}}<li>{{.}}</li>{{end}}</ul></details>{{end}}{{if .Evidence}}<details><summary>Собранные данные</summary><ul>{{range .Evidence}}<li>{{.}}</li>{{end}}</ul></details>{{end}}</td></tr>{{end}}</table></section>{{end}}{{if .R.Steps}}<section><h2>Ход прогона</h2><table><tr><th>Этап</th><th>Результат</th><th>Время, мс</th><th>Подробности</th></tr>{{range .R.Steps}}<tr><td>{{.Name}}</td><td>{{.Status}}</td><td>{{.ElapsedMS}}</td><td>{{.Detail}}</td></tr>{{end}}</table></section>{{end}}{{end}}{{range .R.Sections}}<h3>№ {{.ID}} · {{.Title}}</h3>{{if .Error}}<pre>{{.Error}}</pre>{{end}}<section><table><tr>{{range .Columns}}<th>{{.}}</th>{{end}}</tr>{{range .Rows}}<tr>{{range .}}<td>{{.}}</td>{{end}}</tr>{{else}}<tr><td>Нет строк</td></tr>{{end}}</table></section>{{end}}{{if .Rows}}<h2>Время операций</h2><p>{{if .R.Coverage}}Δ относительно указанной для этой серии базы сравнения.{{else}}Δ относительно исходной медианы.{{end}} Отрицательное значение означает сокращение времени. При нескольких сеансах один образец — среднее время операции по сеансам.</p><section><table><tr><th>Серия</th><th>Операция</th>{{if .R.Coverage}}<th>База сравнения</th>{{end}}<th>n</th><th>До, мс</th><th>Медиана серии, мс</th><th>Мин…макс, мс</th><th>Δ</th></tr>{{range .Rows}}<tr><td>{{.Phase}}</td><td>{{.Name}}</td>{{if $.R.Coverage}}<td>{{.Baseline}}</td>{{end}}<td>{{.N}}</td><td>{{if .Compared}}{{printf "%.2f" .BaselineMedian}}{{else}}—{{end}}</td><td>{{printf "%.2f" .Median}}</td><td>{{printf "%.2f" .Min}}…{{printf "%.2f" .Max}}</td><td>{{if .Compared}}{{delta .Delta}}{{else}}—{{end}}</td></tr>{{end}}</table></section>{{end}}<h2>Стенд</h2><ul>{{range $k,$v:=.R.Inventory}}<li>{{$k}} · {{$v}}</li>{{end}}</ul><h2>Изменения</h2><ul>{{range .R.Journal}}<li>{{.}}</li>{{end}}</ul><p><a href="report.json">Исходные измерения и параметры прогона (JSON)</a></p></html>`))

func WriteHTML(path string, r Report) error {
	f, e := os.Create(path)
	if e != nil {
		return e
	}
	e = htmlPage.Execute(f, struct {
		R    Report
		Rows []Row
	}{r, Rows(r)})
	ce := f.Close()
	if e != nil {
		return e
	}
	return ce
}
