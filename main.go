package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/hleserg/1c_sql_perf_lab/lab"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func demoProfile() lab.Profile {
	p := lab.Example()
	p.Name = "Демонстрационный стенд"
	p.AsyncStats = true
	p.Cluster.Connections = 128
	return p
}
func profilePath(root string) string { return filepath.Join(root, "local", "stand.json") }
func ensureProfile(path string) error {
	if _, e := os.Stat(path); e == nil {
		return nil
	} else if !os.IsNotExist(e) {
		return e
	}
	return lab.SaveJSON(path, lab.Example())
}
func openFile(path string, edit bool) tea.Cmd {
	return func() tea.Msg {
		if edit {
			if e := ensureProfile(path); e != nil {
				return note(e.Error())
			}
		}
		var c *exec.Cmd
		switch runtime.GOOS {
		case "windows":
			if edit {
				c = exec.Command("notepad.exe", path)
			} else {
				c = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", path)
			}
		case "darwin":
			c = exec.Command("open", path)
		default:
			c = exec.Command("xdg-open", path)
		}
		if e := c.Start(); e != nil {
			return note(e.Error())
		}
		go c.Wait()
		return note(path)
	}
}
func runLab(root string, demo bool, ids []int, job string, plan *lab.RestorePlan, recoverOnly bool, emit func(lab.Event), ctx context.Context) (lab.Report, string, error) {
	p := demoProfile()
	var b lab.Backend
	if demo {
		b = lab.NewDemo()
	} else {
		var e error
		p, e = lab.Load(profilePath(root))
		if e != nil {
			return lab.Report{}, "", e
		}
		if job != "" {
			p.Cluster.RAC = ""
		}
		live := &lab.Live{P: p}
		defer live.Close()
		b = live
	}
	r := lab.Run{Restore: plan, JobKey: job, Root: root, Profile: p, Demo: demo, Backend: b, Emit: emit}
	if recoverOnly {
		return lab.Report{}, "", r.Recover(ctx)
	}
	return r.Execute(ctx, ids)
}
func run() error {
	exe, _ := os.Executable()
	root := flag.String("home", filepath.Dir(exe), "Каталог local и runs")
	demo := flag.Bool("demo", false, "Демонстрация в консоли")
	live := flag.Bool("run", false, "Прогон на стенде")
	recoverOnly := flag.Bool("recover", false, "Вернуть настройки из журнала")
	initP := flag.Bool("init", false, "Создать пример локального профиля")
	itemList := flag.String("items", "19", "Номера через запятую, 39 — кластер, пусто — исходный замер")
	job := flag.String("job", "", "Работа SQL: inspect, stats, stats-erp, checkdb, backup, restore")
	repo := flag.String("github", "", "Приватный репозиторий протоколов owner/repo или off")
	syncOnly := flag.Bool("sync", false, "Отправить накопившиеся события в GitHub")
	backup := flag.String("backup", "", "Полный путь к .bak, по умолчанию свежий файл из папки восстановления")
	replace := flag.String("replace", "", "Подтверждение восстановления: имя базы стенда")
	flag.Parse()
	path, e := filepath.Abs(*root)
	if e != nil {
		return e
	}
	if *repo != "" || *syncOnly {
		return deliveryCLI(path, *repo, *syncOnly)
	}
	if *initP {
		return ensureProfile(profilePath(path))
	}
	if !*demo && !*live && !*recoverOnly {
		if *job != "" {
			return errors.New("для -job укажи -run или -demo")
		}
		return runTerminal(newUI(path))
	}
	if *demo && (*live || *recoverOnly) {
		return errors.New("выбери один режим")
	}
	if *job != "" {
		if _, ok := lab.FindJob(*job); !ok && *job != "restore" {
			return fmt.Errorf("неизвестная работа %q", *job)
		}
		*itemList = ""
	}
	var ids []int
	for _, s := range strings.Split(*itemList, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		n, e := strconv.Atoi(s)
		if e != nil {
			return e
		}
		ids = append(ids, n)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var plan *lab.RestorePlan
	if *job == "restore" {
		if *recoverOnly {
			return errors.New("выбери восстановление базы или откат параметров")
		}
		p := demoProfile()
		if !*demo {
			p, e = lab.Load(profilePath(path))
			if e != nil {
				return e
			}
		}
		file := *backup
		if file == "" {
			files, e := lab.ListBackups(ctx, p, *demo)
			if e != nil {
				return e
			}
			file = files[0].Path
		}
		preview, e := lab.PrepareRestore(ctx, p, file, *demo)
		if e != nil {
			return e
		}
		fmt.Println(preview.Summary())
		if *replace == "" && !*demo {
			fmt.Println("Только просмотр. Для выполнения добавь -replace с точным именем базы назначения.")
			return nil
		}
		if !*demo && *replace != preview.Target {
			return errors.New("имя базы в -replace отличается от выбранной цели")
		}
		plan = &preview
	} else if *replace != "" || *backup != "" {
		return errors.New("ключи -backup и -replace относятся к -job restore")
	}
	_, file, e := start(path, *demo, ids, *job, plan, *recoverOnly, func(v lab.Event) {
		if v.Values == nil && v.Section == nil {
			fmt.Printf("%s · %s %d/%d\n", v.Phase, v.Detail, v.Step, v.Total)
		}
	}, ctx)
	if file != "" {
		fmt.Println(file)
	}
	return e
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func lastReport(root string) (lab.Report, string, error) {
	paths, _ := filepath.Glob(filepath.Join(root, "runs", "*", "report.json"))
	var latest string
	var latestReport lab.Report
	for _, path := range paths {
		b, e := os.ReadFile(path)
		var r lab.Report
		if e == nil && json.Unmarshal(b, &r) == nil && r.Started.After(latestReport.Started) {
			latest, latestReport = path, r
		}
	}
	if latest == "" {
		return lab.Report{}, "", errors.New("сохранённых прогонов пока нет")
	}
	return latestReport, latest, nil
}
