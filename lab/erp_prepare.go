package lab

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var platformBuild = regexp.MustCompile(`^8\.[0-9]+\.[0-9]+\.[0-9]+$`)

// The elevated operation only accepts files from the exact x64 installation of
// the sole running local agent. Resolve reparse points before checking the path.
func installedBin(root string, processes []platformProcess) (string, int, error) {
	var agents []platformProcess
	for _, p := range processes {
		if strings.EqualFold(p.Name, "ragent.exe") {
			agents = append(agents, p)
		}
	}
	if len(agents) != 1 || len(agents[0].Ports) != 1 || agents[0].Ports[0] < 1 || agents[0].Ports[0] > 65535 {
		return "", 0, errors.New("не удалось однозначно выбрать запущенный агент 1С и его порт; проверь службу агента сервера 1С на стенде")
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", 0, err
	}
	path, err := filepath.EvalSymlinks(agents[0].Path)
	if err != nil {
		return "", 0, errors.New("путь к ragent.exe недоступен; открой Perf Lab от имени администратора на сервере 1С")
	}
	rel, err := filepath.Rel(base, path)
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if err != nil || len(parts) != 4 || !(strings.EqualFold(parts[0], "1cv8") || strings.EqualFold(parts[0], "1cv8t")) || !platformBuild.MatchString(parts[1]) || !strings.EqualFold(parts[2], "bin") || !strings.EqualFold(parts[3], "ragent.exe") {
		return "", 0, errors.New("не удалось подтвердить установленную x64-платформу по пути запущенного агента; проверь установку 1С на стенде")
	}
	return filepath.Dir(path), agents[0].Ports[0], nil
}

func platformFile(bin, name string) (string, error) {
	want := filepath.Join(bin, name)
	path, err := filepath.EvalSymlinks(want)
	if err != nil || !strings.EqualFold(path, want) {
		return "", fmt.Errorf("файл %s недоступен в выбранной сборке; восстанови компоненты этой версии через установщик 1С", name)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("файл %s недоступен в выбранной сборке", name)
	}
	return path, nil
}

// PrepareLocalERP is entered only from the explicit repair action or recovery
// of a journal that records a cluster change. Its cleanup owns only its child.
func PrepareLocalERP(ctx context.Context, p Profile, fixCOM bool, note func(string)) (func(), error) {
	l := &Live{P: p}
	defer l.Close()
	if _, err := checkHostEndpoint(p); err != nil {
		return nil, err
	}
	if err := l.connect(ctx, p.Database); err != nil {
		return nil, err
	}
	id, err := l.localSQLIdentity(ctx)
	if err != nil {
		return nil, err
	}
	if p.ERPServer != "" {
		if _, ok := localPort(p.ERPServer, "1541", id.host, id.fqdn); !ok {
			return nil, errors.New("сервер 1С в профиле находится на другом узле; открой Perf Lab на стенде с локальным агентом")
		}
	}
	var procs []platformProcess
	if err := localAdminJSON(ctx, rasHostScript, &procs); err != nil {
		return nil, err
	}
	root, err := platformRoot()
	if err != nil {
		return nil, err
	}
	bin, agentPort, err := installedBin(root, procs)
	if err != nil {
		return nil, err
	}
	port, ok := localPort(p.Cluster.Endpoint, "1545", id.host, id.fqdn)
	if p.Cluster.Endpoint == "" {
		port, ok = "1545", true
	}
	if !ok {
		return nil, errors.New("адрес RAS в профиле указывает на другой узел; выбери локальный стенд")
	}
	if fixCOM {
		note("Проверяем регистрацию COM платформы из " + bin)
		dll, err := platformFile(bin, "comcntr.dll")
		if err != nil {
			return nil, err
		}
		if err := registerConnector(ctx, dll); err != nil {
			return nil, err
		}
		note("COM-коннектор зарегистрирован. Проверяем его в новом процессе Windows")
		var ready bool
		if err := localAdminJSON(ctx, `$ErrorActionPreference='Stop'; $c=New-Object -ComObject V83.COMConnector; if ($null -eq $c.PoolCapacity) {throw 'PoolCapacity unavailable'}; [Console]::WriteLine('true')`, &ready); err != nil || !ready {
			return nil, fmt.Errorf("проверка COM после регистрации: %v. Восстанови компоненты COM в установщике этой сборки 1С", err)
		}
	}
	ports := processPorts(procs, "ras.exe")
	n, _ := strconv.Atoi(port)
	if _, found := ports[n]; found || (p.Cluster.Endpoint == "" && len(ports) > 0) {
		note("Для поиска базы используем уже запущенный RAS")
		return func() {}, nil
	}
	release, err := startInstalledRAS(ctx, bin, port, agentPort, note)
	if err != nil && fixCOM && ctx.Err() == nil {
		note(err.Error() + ". Проверяем доступ к базе через COM агента")
		return func() {}, nil
	}
	return release, err
}

func startInstalledRAS(ctx context.Context, bin, port string, agentPort int, note func(string)) (func(), error) {
	ras, err := platformFile(bin, "ras.exe")
	if err != nil {
		return nil, err
	}
	rac, err := platformFile(bin, "rac.exe")
	if err != nil {
		return nil, err
	}
	endpoint := net.JoinHostPort("127.0.0.1", port)
	note("Запускаем временный RAS на порту " + port)
	return temporaryRAS(ctx, ras, Cluster{RAC: rac, Endpoint: endpoint}, agentPort)
}

func temporaryRAS(ctx context.Context, ras string, c Cluster, agentPort int) (func(), error) {
	_, port, _ := net.SplitHostPort(c.Endpoint)
	if c, err := net.DialTimeout("tcp", c.Endpoint, 300*time.Millisecond); err == nil {
		c.Close()
		return nil, fmt.Errorf("порт %s уже занят другим процессом; выбери свободный порт RAS в профиле", port)
	}
	// Keep RAS alive during rollback, including rollback after Ctrl+C.
	child, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	var runErr error
	cmd := exec.CommandContext(child, ras, "cluster", "--port="+port, net.JoinHostPort("127.0.0.1", strconv.Itoa(agentPort)))
	cmd.Dir = filepath.Dir(ras)
	go func() {
		defer close(done)
		runErr = runUpdateProcess(cmd)
	}()
	closeRAS := func() { stop(); <-done }
	ready, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			stop()
			return nil, fmt.Errorf("временный RAS завершился до подключения (%v); проверь доступность порта %s и службы агента", runErr, port)
		case <-ready.Done():
			closeRAS()
			return nil, fmt.Errorf("RAS на %s не ответил на запрос списка кластеров: %w", c.Endpoint, ready.Err())
		case <-ticker.C:
			if _, err := readRAC(ready, c, "cluster", "list"); err == nil {
				return closeRAS, nil
			}
		}
	}
}

// Recreate the recorded local endpoint and resolve both UUIDs against SQL again.
func (l *Live) recoverCluster(ctx context.Context, key string, note func(string)) (func(), error) {
	parts := strings.Split(key, "|")
	if len(parts) != 3 || !uuid.MatchString(parts[1]) || !uuid.MatchString(parts[2]) {
		return nil, errors.New("в журнале отсутствует подтверждённая цель кластера")
	}
	p := l.P
	if p.Cluster.ClusterID != "" && !strings.EqualFold(p.Cluster.ClusterID, parts[1]) || p.Cluster.ServerID != "" && !strings.EqualFold(p.Cluster.ServerID, parts[2]) {
		return nil, errors.New("кластер в профиле отличается от журнала")
	}
	if p.Cluster.Endpoint != "" {
		h, f, err := windowsHostNames()
		a, aok := localPort(p.Cluster.Endpoint, "1545", h, f)
		b, bok := localPort(parts[0], "1545", h, f)
		if err != nil || !aok || !bok || a != b {
			return nil, errors.New("адрес RAS отличается от журнала")
		}
	}
	p.Cluster.Endpoint, p.Cluster.ClusterID, p.Cluster.ServerID = parts[0], parts[1], parts[2]
	release, err := PrepareLocalERP(ctx, p, false, note)
	if err != nil {
		return nil, err
	}
	found, err := DiscoverLocalCluster(ctx, p)
	if err != nil {
		release()
		return nil, err
	}
	p.Cluster = found
	if clusterKey(p) != key {
		release()
		return nil, errors.New("найденный рабочий сервер отличается от журнала")
	}
	l.P.Cluster = found
	return release, nil
}
