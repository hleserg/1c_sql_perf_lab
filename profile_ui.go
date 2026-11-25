package main

import (
	"encoding/json"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/hleserg/1c_sql_perf_lab/lab"
	"os"
	"strconv"
	"strings"
)

type formField struct{ label, section, value string }
type profileForm struct {
	fields   []formField
	pos      int
	editing  bool
	buffer   string
	cursor   int
	err      string
	original lab.Profile
}

func formFor(path string) (*profileForm, error) {
	p := lab.Example()
	b, e := os.ReadFile(path)
	if e == nil {
		if e = json.Unmarshal(b, &p); e != nil {
			return nil, e
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	dec := strconv.Itoa
	return &profileForm{original: p, fields: []formField{
		{"Название", "Стенд", p.Name}, {"Адрес SQL / host:port", "SQL", p.SQLHost}, {"Именованный экземпляр", "SQL", p.Instance}, {"Имя из SERVERPROPERTY('ServerName')", "SQL", p.ExpectedServer}, {"База SQL", "SQL", p.Database}, {"Доверять сертификату · true/false", "SQL", strconv.FormatBool(p.TrustServerCertificate)},
		{"Сервер 1С", "Нагрузка", p.ERPServer}, {"Имя информационной базы", "Нагрузка", p.ERPDatabase}, {"Вид документа", "Нагрузка", p.Document}, {"UUID существующего документа", "Нагрузка", p.DocumentUUID},
		{"Повторов на серию", "Протокол", dec(p.Samples)}, {"Прогревов", "Протокол", dec(p.Warmup)}, {"Одновременных сеансов", "Протокол", dec(p.Sessions)}, {"Тайм-аут образца, с", "Протокол", dec(p.TimeoutSeconds)},
		{"Целевой MAXDOP", "Эксперимент", dec(p.MaxDOP)}, {"Проверенная сборка для №24", "Эксперимент", p.OptimizerBuild}, {"Асинхронная статистика · true/false", "Эксперимент", strconv.FormatBool(p.AsyncStats)},
		{"Путь к rac.exe", "Кластер 1С", p.Cluster.RAC}, {"Адрес RAS", "Кластер 1С", p.Cluster.Endpoint}, {"UUID кластера", "Кластер 1С", p.Cluster.ClusterID}, {"UUID рабочего сервера", "Кластер 1С", p.Cluster.ServerID}, {"Соединений на процесс", "Кластер 1С", dec(p.Cluster.Connections)},
		{"Папка копий на сервере SQL", "Копирование", p.BackupDir},
		{"Откуда восстановить .bak", "Восстановление", p.RestoreFolder()},
	}}, nil
}
func (f *profileForm) profile() (lab.Profile, error) {
	p := f.original
	v := func(i int) string { return strings.TrimSpace(f.fields[i].value) }
	p.Name, p.SQLHost, p.Instance, p.ExpectedServer, p.Database = v(0), v(1), v(2), v(3), v(4)
	p.ERPServer, p.ERPDatabase, p.Document, p.DocumentUUID = v(6), v(7), v(8), v(9)
	p.OptimizerBuild = v(15)
	p.BackupDir = v(22)
	p.RestoreDir = v(23)
	p.Cluster.RAC, p.Cluster.Endpoint, p.Cluster.ClusterID, p.Cluster.ServerID = v(17), v(18), v(19), v(20)
	for _, n := range []struct {
		i    int
		dest *int
	}{{10, &p.Samples}, {11, &p.Warmup}, {12, &p.Sessions}, {13, &p.TimeoutSeconds}, {14, &p.MaxDOP}, {21, &p.Cluster.Connections}} {
		val, e := strconv.Atoi(v(n.i))
		if e != nil {
			return p, fmt.Errorf("%s · введи целое число", f.fields[n.i].label)
		}
		*n.dest = val
	}
	for _, n := range []struct {
		i    int
		dest *bool
	}{{5, &p.TrustServerCertificate}, {16, &p.AsyncStats}} {
		val, e := strconv.ParseBool(v(n.i))
		if e != nil {
			return p, fmt.Errorf("%s · введи true или false", f.fields[n.i].label)
		}
		*n.dest = val
	}
	return p, p.Validate()
}
func (m *ui) formKey(k tea.KeyMsg) tea.Cmd {
	f := m.form
	key := k.String()
	if key == "ctrl+c" {
		m.form = nil
		return nil
	}
	if f.editing {
		rr := []rune(f.buffer)
		switch key {
		case "enter", "ctrl+s":
			f.fields[f.pos].value = f.buffer
			f.editing = false
			if key == "ctrl+s" {
				return m.formKey(k)
			}
		case "esc":
			f.editing = false
		case "backspace", "ctrl+h":
			if f.cursor > 0 {
				rr = append(rr[:f.cursor-1], rr[f.cursor:]...)
				f.cursor--
				f.buffer = string(rr)
			}
		case "delete":
			if f.cursor < len(rr) {
				f.buffer = string(append(rr[:f.cursor], rr[f.cursor+1:]...))
			}
		case "left":
			f.cursor = max(0, f.cursor-1)
		case "right":
			f.cursor = min(len(rr), f.cursor+1)
		case "home", "ctrl+a":
			f.cursor = 0
		case "end", "ctrl+e":
			f.cursor = len(rr)
		case "ctrl+u":
			f.buffer = ""
			f.cursor = 0
		default:
			if k.Type == tea.KeyRunes || k.Type == tea.KeySpace {
				chars := k.Runes
				if k.Type == tea.KeySpace {
					chars = []rune(" ")
				}
				f.buffer = string(rr[:f.cursor]) + string(chars) + string(rr[f.cursor:])
				f.cursor += len(chars)
			}
		}
		return nil
	}
	switch key {
	case "esc":
		m.form = nil
	case "down", "j", "tab":
		f.pos = (f.pos + 1) % len(f.fields)
	case "up", "k", "shift+tab":
		f.pos = (f.pos + len(f.fields) - 1) % len(f.fields)
	case "pgdown":
		f.pos = min(len(f.fields)-1, f.pos+6)
	case "pgup":
		f.pos = max(0, f.pos-6)
	case "enter":
		f.editing = true
		f.buffer = f.fields[f.pos].value
		f.cursor = len([]rune(f.buffer))
	case "ctrl+s":
		p, e := f.profile()
		if e == nil {
			e = lab.SaveJSON(profilePath(m.root), p)
		}
		if e != nil {
			f.err = e.Error()
		} else {
			m.form = nil
			m.msg = "Профиль сохранён · " + p.Name + ""
		}
	case "o":
		return openFile(profilePath(m.root), true)
	}
	return nil
}
func (m ui) formView(w int) string {
	f := m.form
	caption := fmt.Sprintf("Профиль стенда    %d / %d", f.pos+1, len(f.fields))
	size := max(2, min(len(f.fields), m.h-10))
	start := max(0, min(len(f.fields)-size, f.pos-size/2))
	rows := []string{ink(f.fields[f.pos].section, blue)}
	for i := start; i < min(len(f.fields), start+size); i++ {
		field := f.fields[i]
		v := field.value
		if v == "" {
			v = "-"
		}
		labelW := min(39, w/2)
		if i == f.pos && f.editing {
			rr := []rune(f.buffer)
			// Keep the insertion point visible when editing paths or a long connection name.
			left := rr[:f.cursor]
			for len(left) > 0 && lipgloss.Width(string(left)) > w-labelW-9 {
				left = left[1:]
			}
			v = string(left) + "|" + string(rr[f.cursor:])
		}
		m.mark(1, len(rows)+1, w-2, "field", i)
		rows = append(rows, activeRow(w-3, pad(field.label, labelW)+"  "+v, i == f.pos))
	}
	return panel(caption, rows, w, mint)
}
