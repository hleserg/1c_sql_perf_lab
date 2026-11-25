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
	if job == "access-status" && !demo {
		return savedStatus(ctx, root, emit)
	}
	repairERP := job == "erp-repair"
	if repairERP && (demo || recoverOnly || runtime.GOOS != "windows") {
		return lab.Report{}, "", errors.New("исправление подключения доступно отдельным прогоном на стенде Windows")
	}
	requireERP := job == "erp" || repairERP
	var prepareErr error
	if requireERP {
		job = lab.FullJob.Key
	}
	var diagnostics []string
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
		if job == lab.FullJob.Key && runtime.GOOS == "windows" {
			progress := func(s string) {
				diagnostics = append(diagnostics, s)
				if emit != nil {
					emit(lab.Event{Phase: "Подключение 1С", Detail: s})
				}
			}
			if repairERP {
				if len(p.Workload) > 0 {
					prepareErr = errors.New("в профиле задан внешний сценарий. Проверь его подключение и нажми «Прогон через 1С»")
				} else {
					closeRAS, err := lab.PrepareLocalERP(ctx, p, true, progress)
					if err != nil {
						prepareErr = err
					} else {
						defer closeRAS()
					}
				}
				if prepareErr != nil {
					progress(prepareErr.Error())
				}
			}
			verifyBuiltin := requireERP && len(p.Workload) == 0
			if prepareErr == nil && (verifyBuiltin || p.ERPDatabase == "" || p.ERPServer == "" || p.Cluster.RAC == "") && lab.LocalERPHost(p) {
				progress("Ищем регистрацию выбранной базы на локальном RAS")
				found, err := lab.DiscoverStandERPWithLog(ctx, p, progress)
				if err == nil {
					p = found
				} else {
					progress(err.Error())
					if verifyBuiltin {
						// This run must never measure a different registration on the same host.
						p.ERPDatabase = ""
					}
				}
			}
			if prepareErr == nil && len(p.Workload) == 0 && p.ERPDatabase != "" && p.ERPServer != "" && p.DocumentUUID == "" {
				progress("Выбираем документ для всех замеров")
				id, err := lab.FindWorkloadDocument(ctx, p)
				if err == nil {
					p.DocumentUUID = id
				} else {
					progress(err.Error())
				}
			}
		}
		if job != "" && job != lab.FullJob.Key {
			p.Cluster.RAC = ""
		}
		live := &lab.Live{P: p}
		defer live.Close()
		b = live
	}
	r := lab.Run{ERPPrepareError: prepareErr, RequireERP: requireERP, ERPDiagnostics: diagnostics, Restore: plan, JobKey: job, Root: root, Profile: p, Demo: demo, Backend: b, Emit: emit}
	if recoverOnly {
		return lab.Report{}, "", r.Recover(ctx)
	}
	return r.Execute(ctx, ids)
}
func run() error {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "mail-test":
			return mailCLI(os.Args[2:])
		case "file-layout":
			return fileLayoutCLI(os.Args[2:])
		case "network-probe":
			return networkProbeCLI(os.Args[2:])
		case "restructure-trial":
			return restructureCLI(os.Args[2:])
		case "weekly-verify":
			return weeklyCLI(os.Args[2:])
		}
	}
	exe, _ := os.Executable()
	root := flag.String("home", filepath.Dir(exe), "Каталог local и runs")
	demo := flag.Bool("demo", false, "Демонстрация в консоли")
	live := flag.Bool("run", false, "Прогон на стенде")
	recoverOnly := flag.Bool("recover", false, "Вернуть настройки из журнала")
	initP := flag.Bool("init", false, "Создать пример локального профиля")
	itemList := flag.String("items", "19", "Номера через запятую, 39 — кластер, пусто — исходный замер")
	job := flag.String("job", "", "Полный прогон: all. Через 1С с обязательным замером: erp. Исправить подключение и повторить: erp-repair. Отдельно: inspect, stats, stats-erp, indexes, checkdb, backup, restore")
	repo := flag.String("github", "", "Приватный репозиторий протоколов owner/repo или off")
	syncOnly := flag.Bool("sync", false, "Отправить накопившиеся события в GitHub")
	backup := flag.String("backup", "", "Полный путь к .bak, по умолчанию свежий файл из папки восстановления")
	replace := flag.String("replace", "", "Подтверждение восстановления: имя базы стенда")
	status := flag.Bool("status", false, "Состояние базы и диагностика доступа после восстановления")
	repair := flag.Bool("repair-access", false, "Восстановить доступ указанного SQL-логина к выбранной базе")
	login := flag.String("sql-login", "", "Существующий SQL-логин для диагностики или исправления")
	role := flag.String("access-role", "", "При -repair-access: connect или db_owner")
	monitor := flag.Bool("monitor", false, "Мониторинг стенда каждые 15 минут до Ctrl+C")
	baseline := flag.Bool("approve-baseline", false, "Сохранить текущие настройки выбранного стенда как эталон для сравнения")
	version := flag.Bool("version", false, "Показать версию программы")
	flag.Parse()
	if *version {
		v := currentRelease
		if v == "" {
			v = "локальная сборка"
		}
		fmt.Println(v)
		return nil
	}
	path, e := filepath.Abs(*root)
	if e != nil {
		return e
	}
	if *status || *repair || *login != "" || *role != "" {
		if *status == *repair || *status && *role != "" || *baseline || *monitor || *demo || *live || *recoverOnly || *job != "" || *initP || *repo != "" || *syncOnly || *backup != "" || *replace != "" {
			return errors.New("выбери -status или -repair-access как отдельную команду")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if *status {
			return statusCLI(ctx, path, *login)
		}
		if *login == "" {
			return errors.New("укажи существующий логин через -sql-login")
		}
		return repairAccess(ctx, path, *login, *role)
	}
	if *baseline || *monitor {
		if *baseline && *monitor || *demo || *live || *recoverOnly || *job != "" || *initP || *repo != "" || *syncOnly {
			return errors.New("используй -monitor или -approve-baseline как отдельную команду")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if *monitor {
			return runMonitor(ctx, path)
		}
		return approveBaseline(ctx, path)
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
		m := newUI(path)
		m.msg = os.Getenv("PERFLAB_UPDATE_NOTICE")
		pathErr := ensureUserPath(exe)
		m.checkOnOpen = runtime.GOOS == "windows" && runtime.GOARCH == "amd64" && m.msg == ""
		if os.Getenv("PERFLAB_UPDATE_RESULTS") == "1" && m.msg != "" {
			if report, file, err := lastReport(path); err == nil {
				m.report, m.file, m.tab = report, file, 2
				m.suitePos = initialSuitePosition(report)
				m.delivery = report.Delivery
			}
		}
		os.Unsetenv("PERFLAB_UPDATE_RESULTS")
		os.Unsetenv("PERFLAB_UPDATE_NOTICE")
		if pathErr != nil {
			m.msg = strings.TrimSpace(m.msg + "  Не удалось добавить perf-lab в PATH: " + pathErr.Error())
		}
		return runTerminal(m)
	}
	if *demo && (*live || *recoverOnly) {
		return errors.New("выбери один режим")
	}
	if *job != "" {
		if _, ok := lab.FindJob(*job); !ok && *job != "restore" && *job != "erp" && *job != "erp-repair" {
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
	if code, forwarded := pathLauncherExitCode(); forwarded {
		os.Exit(code)
	}
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
