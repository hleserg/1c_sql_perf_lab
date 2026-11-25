package lab

import (
	"context"
	_ "embed"
	"encoding/json"
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

//go:embed ras_host.ps1
var rasHostScript string

//go:embed erp_agent.ps1
var erpAgentScript string

type platformProcess struct {
	Name, Path string
	Ports      []int
}
type agentRegistration struct {
	Name, DB, SQL, DBMS string
	Port                int
}

func localAdminJSON(ctx context.Context, script string, result any, env ...string) error {
	bounded, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, "powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePS(script))
	cmd.Env = append(os.Environ(), env...)
	var out, stderr racOutput
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		if bounded.Err() != nil {
			return bounded.Err()
		}
		return fmt.Errorf("%w · %s", err, platformFailure(stderr.data.Bytes()))
	}
	return json.Unmarshal(out.data.Bytes(), result)
}

var platformCode = regexp.MustCompile("(?i)0x[0-9a-f]{8}|\\b100(?:60|61)\\b")

func platformFailure(b []byte) string {
	msg := strings.ToLower(string(b))
	if s, e := decodeRACConsoleIfNeeded(b); e == nil {
		msg = strings.ToLower(s)
	}
	// Emit classified messages and numeric codes only. Vendor errors may contain
	// connection strings, user names, or complete server records.
	detail := "проверь версию клиента 1С, адрес и права администратора кластера"
	switch {
	case strings.Contains(msg, "80040154"), strings.Contains(msg, "class not registered"), strings.Contains(msg, "класс не зарегистрирован"):
		detail = "COM-коннектор V83 не зарегистрирован для 64-разрядного PowerShell; установи компонент COM-соединения соответствующей версии платформы"
	case strings.Contains(msg, "10061"), strings.Contains(msg, "refused"), strings.Contains(msg, "отверг"), strings.Contains(msg, "отказал в подключении"):
		detail = "соединение отклонено; проверь адрес и запущен ли ras.exe"
	case strings.Contains(msg, "10060"), strings.Contains(msg, "timed out"):
		detail = "узел не ответил до истечения тайм-аута"
	case strings.Contains(msg, "несовместим"), strings.Contains(msg, "protocol version"), strings.Contains(msg, "различаются версии"), strings.Contains(msg, "version mismatch"):
		detail = "проверь соответствие сборок клиента и сервера 1С"
	case strings.Contains(msg, "authentic"), strings.Contains(msg, "парол"), strings.Contains(msg, "доступ"), strings.Contains(msg, "аутентифик"):
		detail = "проверь учётную запись и её права на указанную операцию"
	case strings.Contains(msg, "no local 1c worker"):
		detail = "у агента отсутствует доступный локальный рабочий процесс"
	}
	if c := platformCode.FindString(msg); c != "" {
		detail += " (" + c + ")"
	}
	return detail
}

// The auto-selected client must be installed in a platform installation folder.
// A process called ras.exe owned by another local user cannot nominate a binary.
func siblingRAC(exe string) string {
	for _, key := range []string{"ProgramW6432", "ProgramFiles", "ProgramFiles(x86)"} {
		root := os.Getenv(key)
		if !filepath.IsAbs(root) {
			continue
		}
		rel, err := filepath.Rel(root, exe)
		if err != nil {
			continue
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 4 || (!strings.EqualFold(parts[0], "1cv8") && !strings.EqualFold(parts[0], "1cv8t")) || !strings.HasPrefix(parts[1], "8.") || !strings.EqualFold(parts[2], "bin") {
			continue
		}
		candidate, err := localRAC(filepath.Join(filepath.Dir(exe), "rac.exe"))
		if err == nil {
			return candidate
		}
	}
	return ""
}

func processPorts(procs []platformProcess, name string) map[int]string {
	ports := map[int]string{}
	for _, proc := range procs {
		if !strings.EqualFold(proc.Name, name) {
			continue
		}
		for _, p := range proc.Ports {
			if p > 0 && p <= 65535 {
				ports[p] = proc.Path
			}
		}
	}
	return ports
}
func rasTarget(c Cluster, procs []platformProcess, host, fqdn string) (Cluster, error) {
	ports := processPorts(procs, "ras.exe")
	if c.Endpoint == "" {
		if len(ports) > 1 {
			return c, errors.New("у локальных процессов ras.exe несколько портов; выбери адрес RAS в профиле")
		}
		c.Endpoint = "localhost:1545"
		for p := range ports {
			c.Endpoint = net.JoinHostPort("localhost", strconv.Itoa(p))
		}
	}
	port, ok := localPort(c.Endpoint, "1545", host, fqdn)
	if !ok {
		return c, errors.New("адрес RAS в профиле не указывает на локальный узел")
	}
	c.Endpoint = net.JoinHostPort("127.0.0.1", port)
	if c.RAC == "" {
		n, _ := strconv.Atoi(port)
		c.RAC = siblingRAC(ports[n])
		if c.RAC == "" {
			agents := processPorts(procs, "ragent.exe")
			if len(agents) == 1 {
				for _, exe := range agents {
					c.RAC = siblingRAC(exe)
				}
			}
		}
	}
	var e error
	c.RAC, e = localRAC(c.RAC)
	return c, e
}

// A diagnostic can show the command for the matching installation when no
// RAS listener was observed. Starting a server is an explicit local operation.
func rasStartHint(c Cluster, procs []platformProcess, host, fqdn string) string {
	if len(processPorts(procs, "ras.exe")) != 0 {
		return ""
	}
	agents := processPorts(procs, "ragent.exe")
	if len(agents) != 1 {
		return ""
	}
	port := "1545"
	if c.Endpoint != "" {
		var ok bool
		port, ok = localPort(c.Endpoint, port, host, fqdn)
		if !ok {
			return ""
		}
	}
	for n, path := range agents {
		client := siblingRAC(path)
		if client == "" {
			return ""
		}
		ras := filepath.Join(filepath.Dir(client), "ras.exe")
		if f, e := os.Stat(ras); e != nil || !f.Mode().IsRegular() {
			return ""
		}
		return fmt.Sprintf("Для работы с настройками кластера запусти в отдельном PowerShell на стенде: & '%s' cluster --port=%s 127.0.0.1:%d", strings.ReplaceAll(ras, "'", "''"), port, n)
	}
	return ""
}
func LocalERPHost(p Profile) bool { _, e := checkHostEndpoint(p); return e == nil }

func selectAgentRegistration(rows []agentRegistration, p Profile, id sqlIdentity) (registration, error) {
	var matches []registration
	for _, b := range rows {
		if p.ERPServer != "" {
			port, ok := localPort(p.ERPServer, "1541", id.host, id.fqdn)
			if !ok || port != strconv.Itoa(b.Port) {
				continue
			}
		}
		if b.Port < 1 || b.Port > 65535 || b.Name == "" || strings.ContainsAny(b.Name, ";\"\r\n") || (!strings.EqualFold(b.DBMS, "MSSQLServer")) || b.DB != id.db || !matchesRegisteredSQL(b.SQL, id) {
			continue
		}
		if p.ERPDatabase != "" && !strings.EqualFold(p.ERPDatabase, b.Name) {
			continue
		}
		matches = append(matches, registration{endpoint: net.JoinHostPort("127.0.0.1", strconv.Itoa(b.Port)), name: b.Name})
	}
	if len(matches) != 1 {
		return registration{}, fmt.Errorf("COM: регистраций с подтверждёнными экземпляром SQL и базой: %d; проверь регистрацию стенда", len(matches))
	}
	return matches[0], nil
}

func discoverViaAgent(ctx context.Context, p Profile, id sqlIdentity, processes []platformProcess) (registration, error) {
	var none registration
	if p.Cluster.ClusterID != "" {
		return none, errors.New("в профиле указан UUID кластера; для его проверки нужен доступ к RAS")
	}
	ports := processPorts(processes, "ragent.exe")
	if len(ports) > 1 {
		return none, errors.New("найдено несколько агентов 1С; укажи адрес нужного RAS в профиле")
	}
	port := 1540
	for n := range ports {
		port = n
	}
	manager := ""
	if p.ERPServer != "" {
		var ok bool
		manager, ok = localPort(p.ERPServer, "1541", id.host, id.fqdn)
		if !ok {
			return none, errors.New("сервер 1С в профиле не является локальным")
		}
	}
	var rows []agentRegistration
	if err := localAdminJSON(ctx, erpAgentScript, &rows, "PERFLAB_AGENT_PORT="+strconv.Itoa(port), "PERFLAB_MANAGER_PORT="+manager, "PERFLAB_SQL_NODE="+id.host, "PERFLAB_FQDN="+id.fqdn); err != nil {
		return none, fmt.Errorf("COM агента 1С 127.0.0.1:%d: %w", port, err)
	}
	return selectAgentRegistration(rows, p, id)
}
