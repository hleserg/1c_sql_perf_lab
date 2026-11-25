package lab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// DiscoverStandERP fills missing fields using a unique registration pointing
// at the verified SQL database. It does not start RAS or change either server.
func DiscoverStandERP(ctx context.Context, p Profile) (Profile, error) {
	return DiscoverStandERPWithLog(ctx, p, nil)
}
func DiscoverStandERPWithLog(ctx context.Context, p Profile, note func(string)) (Profile, error) {
	if runtime.GOOS != "windows" {
		return p, errors.New("поиск локального RAS доступен на Windows")
	}
	_, err := checkHostEndpoint(p)
	if err != nil {
		return p, errors.New("автопоиск доступен на узле выбранного SQL")
	}
	ctx, stop := context.WithTimeout(ctx, 45*time.Second)
	defer stop()
	l := &Live{P: p}
	defer l.Close()
	if err = l.connect(ctx, p.Database); err != nil {
		return p, err
	}
	reg, err := l.localRegistration(ctx, true, note)
	if err != nil {
		return p, err
	}
	p.ERPServer = reg.endpoint
	if p.ERPDatabase == "" {
		p.ERPDatabase = reg.name
	}
	if reg.cluster.ServerID != "" {
		p.Cluster = reg.cluster
	} else {
		// A COM-only discovery identifies the infobase, not a writable RAS worker.
		p.Cluster.RAC = ""
	}
	return p, nil
}

func DiscoverLocalCluster(ctx context.Context, p Profile) (Cluster, error) {
	found, err := DiscoverStandERP(ctx, p)
	if err != nil {
		return p.Cluster, err
	}
	if !uuid.MatchString(found.Cluster.ServerID) || !uuid.MatchString(found.Cluster.ClusterID) {
		return p.Cluster, errors.New("для выбора рабочего сервера нужен однозначный выделенный кластер или явный UUID сервера")
	}
	return found.Cluster, nil
}

type registration struct {
	cluster                  Cluster
	endpoint, name, infobase string
}
type sqlIdentity struct{ host, fqdn, instance, db, port string }
type racRecord map[string]string
type racReader func(context.Context, Cluster, ...string) ([]racRecord, error)

func localRAC(path string) (string, error) {
	if path != "" {
		if !filepath.IsAbs(path) || strings.HasPrefix(path, `\\`) || !strings.EqualFold(filepath.Base(path), "rac.exe") {
			return "", errors.New("укажи абсолютный путь к локальному rac.exe")
		}
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() {
			return path, nil
		}
		return "", errors.New("указанный rac.exe недоступен")
	}
	choices := map[string]string{}
	for _, env := range []string{"ProgramW6432", "ProgramFiles", "ProgramFiles(x86)"} {
		dir := os.Getenv(env)
		if !filepath.IsAbs(dir) {
			continue
		}
		for _, base := range []string{"1cv8", "1cv8t"} {
			files, _ := filepath.Glob(filepath.Join(dir, base, "8.*", "bin", "rac.exe"))
			for _, f := range files {
				choices[strings.ToLower(f)] = f
			}
		}
	}
	if len(choices) != 1 {
		return "", errors.New("найдено несколько версий rac.exe или ни одной; укажи путь к клиенту установленной версии RAS")
	}
	for _, f := range choices {
		return f, nil
	}
	return "", errors.New("rac.exe не найден")
}

// Only loopback and the exact local machine name are accepted. Resolve no
// aliases through DNS and never use a discovered hostname as a remote target.
func localPort(endpoint, defaultPort string, names ...string) (string, bool) {
	node, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		node = endpoint
		port = defaultPort
	}
	n, e := strconv.Atoi(port)
	if e != nil || n < 1 || n > 65535 || !sameLocalNode(node, names...) {
		return "", false
	}
	return strconv.Itoa(n), true
}
func sameLocalNode(node string, names ...string) bool {
	// A colon is only valid here for an IPv6 literal. A port belongs to an
	// endpoint and must be validated separately.
	if strings.Contains(node, ":") && net.ParseIP(node) == nil {
		return false
	}
	return isLocalSQLEndpoint(node, names...)
}

func matchesRegisteredSQL(s string, want sqlIdentity) bool {
	server, port, hasPort := strings.Cut(s, ",")
	host, instance, _ := strings.Cut(server, `\`)
	if !sameLocalNode(host, want.host, want.fqdn) || !strings.EqualFold(instance, want.instance) {
		return false
	}
	return !hasPort || (want.port != "" && port == want.port)
}

func (l *Live) verifiedRegistration(ctx context.Context) (registration, error) {
	return l.localRegistration(ctx, false, nil)
}

func (l *Live) localRegistration(ctx context.Context, allowCOM bool, note func(string)) (registration, error) {
	var empty registration
	host, fqdn, err := windowsHostNames()
	if err != nil || !isLocalSQLEndpoint(l.P.SQLHost, host, fqdn) {
		return empty, errors.New("для сопоставления RAS запусти сбор на локальном узле SQL и 1С")
	}
	id := sqlIdentity{fqdn: fqdn}
	var reported string
	var dbid int
	err = l.conn.QueryRowContext(ctx, `SELECT CONVERT(nvarchar(128),SERVERPROPERTY('ComputerNamePhysicalNetBIOS')),
 COALESCE(CONVERT(nvarchar(128),SERVERPROPERTY('InstanceName')),N''),DB_NAME(),
 COALESCE(CONVERT(varchar(10),CONNECTIONPROPERTY('local_tcp_port')),''),CONVERT(nvarchar(128),SERVERPROPERTY('ServerName')),DB_ID()`).Scan(&id.host, &id.instance, &id.db, &id.port, &reported, &dbid)
	if err != nil || dbid <= 4 || !strings.EqualFold(id.host, host) || !strings.EqualFold(reported, l.P.ExpectedServer) || id.db != l.P.Database {
		return empty, errors.New("не подтверждены локальный экземпляр и база SQL из профиля")
	}
	var processes []platformProcess
	if allowCOM {
		e := localAdminJSON(ctx, rasHostScript, &processes)
		if note != nil {
			if e != nil {
				note("Не удалось прочитать порты процессов Windows: " + e.Error())
			} else if len(processPorts(processes, "ras.exe")) == 0 {
				note("У локального ras.exe не найден порт в состоянии LISTEN.")
			}
		}
	}
	c, err := rasTarget(l.P.Cluster, processes, host, fqdn)
	if err == nil {
		if note != nil {
			note("Проверяем RAS " + c.Endpoint + " · " + c.RAC)
		}
		var r registration
		r, err = findRegistration(ctx, l.P, id, c, readRAC)
		if err == nil {
			if note != nil {
				note("Регистрация подтверждена через RAS: " + r.endpoint + " / " + r.name)
			}
			return r, nil
		}
	}
	if !allowCOM || ctx.Err() != nil {
		return empty, err
	}
	if note != nil {
		note("RAS: " + err.Error() + ". Проверяем регистрацию через локальный агент 1С")
		if hint := rasStartHint(l.P.Cluster, processes, host, fqdn); hint != "" {
			note(hint)
		}
	}
	r, comErr := discoverViaAgent(ctx, l.P, id, processes)
	if comErr != nil {
		return empty, fmt.Errorf("RAS: %v. %w", err, comErr)
	}
	if note != nil {
		note("Регистрация подтверждена через COM агента: " + r.endpoint + " / " + r.name)
	}
	return r, nil
}

func findRegistration(ctx context.Context, p Profile, id sqlIdentity, c Cluster, read racReader) (registration, error) {
	var none registration
	var manager string
	if p.ERPServer != "" {
		var ok bool
		manager, ok = localPort(p.ERPServer, "1541", id.host, id.fqdn)
		if !ok {
			return none, errors.New("сервер 1С из профиля не является локальным")
		}
	}
	clusters, err := read(ctx, c, "cluster", "list")
	if err != nil {
		return none, err
	}
	if len(clusters) > 8 {
		return none, errors.New("слишком много записей кластеров для автопоиска")
	}
	var candidates []registration
	for _, cl := range clusters {
		guid, port := cl["cluster"], cl["port"]
		if !uuid.MatchString(guid) || !sameLocalNode(cl["host"], id.host, id.fqdn) || (manager != "" && port != manager) || (c.ClusterID != "" && !strings.EqualFold(c.ClusterID, guid)) {
			continue
		}
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			continue
		}
		chosen := c
		chosen.ClusterID = guid
		bases, e := read(ctx, chosen, "infobase", "summary", "list", "--cluster="+guid)
		if e != nil {
			return none, e
		}
		if len(bases) > 64 && p.ERPDatabase == "" {
			return none, errors.New("укажи имя информационной базы для поиска среди более 64 регистраций")
		}
		for _, b := range bases {
			ib, name := b["infobase"], b["name"]
			if !uuid.MatchString(ib) || name == "" || strings.ContainsAny(name, ";\"\r\n") || (p.ERPDatabase != "" && !strings.EqualFold(name, p.ERPDatabase)) {
				continue
			}
			info, e := read(ctx, chosen, "infobase", "info", "--cluster="+guid, "--infobase="+ib)
			if e != nil {
				return none, e
			}
			if len(info) != 1 {
				return none, errors.New("RAS не вернул однозначные сведения о регистрации")
			}
			r := info[0]
			if !strings.EqualFold(r["infobase"], ib) || r["name"] != name || !strings.EqualFold(r["dbms"], "MSSQLServer") || r["db-name"] != id.db || !matchesRegisteredSQL(r["db-server"], id) {
				continue
			}
			servers, e := read(ctx, chosen, "server", "list", "--cluster="+guid)
			if e != nil {
				return none, e
			}
			worker := ""
			allLocal := len(servers) > 0
			for _, s := range servers {
				if !uuid.MatchString(s["server"]) || !sameLocalNode(s["agent-host"], id.host, id.fqdn) {
					allLocal = false
					continue
				}
				pinned := c.ServerID != "" && strings.EqualFold(c.ServerID, s["server"])
				if pinned || (c.ServerID == "" && len(servers) == 1 && len(bases) == 1) {
					worker = s["server"]
				}
			}
			if !allLocal {
				return none, errors.New("автопоиск требует подтверждённых локальных рабочих серверов 1С")
			}
			if c.ServerID != "" && worker == "" {
				return none, errors.New("рабочий сервер из профиля не подтверждён")
			}
			result := chosen
			result.ServerID = worker
			candidates = append(candidates, registration{result, net.JoinHostPort("127.0.0.1", port), name, ib})
		}
	}
	if len(candidates) != 1 {
		return none, fmt.Errorf("регистраций 1С с подтверждённым SQL и базой: %d; нужен один вариант", len(candidates))
	}
	return candidates[0], nil
}

type racOutput struct{ data bytes.Buffer }

func (b *racOutput) Write(p []byte) (int, error) {
	if len(p) > 8*1024*1024-b.data.Len() {
		return 0, errors.New("ответ RAS превысил ограничение размера")
	}
	return b.data.Write(p)
}
func readRAC(ctx context.Context, c Cluster, args ...string) ([]racRecord, error) {
	ctx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	var out, stderr racOutput
	cmd := exec.CommandContext(ctx, c.RAC, append(args, c.Endpoint)...)
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		msg := stderr.data.Bytes()
		if len(msg) == 0 {
			msg = out.data.Bytes()
		}
		return nil, fmt.Errorf("rac %s на %s: %w · %s", args[0], c.Endpoint, err, platformFailure(msg))
	}
	decoded, err := decodeRACConsoleIfNeeded(out.data.Bytes())
	if err != nil {
		return nil, err
	}
	return parseRAC(decoded)
}
func decodeRACConsoleIfNeeded(b []byte) (string, error) {
	if utf8.Valid(b) {
		return string(b), nil
	}
	return decodeRACConsole(b)
}
func parseRAC(raw string) ([]racRecord, error) {
	if !utf8.ValidString(raw) {
		return nil, errors.New("не удалось прочитать кодировку вывода rac.exe")
	}
	var out []racRecord
	var row racRecord
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			if len(row) > 0 {
				out = append(out, row)
				row = nil
			}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch key = strings.TrimSpace(key); key {
		case "cluster", "host", "port", "agent-host", "server", "infobase", "name", "dbms", "db-name", "db-server", "db-proc-info", "session-id":
			if row == nil {
				row = racRecord{}
			}
			if _, exists := row[key]; exists {
				return nil, errors.New("неоднозначный формат записи rac.exe")
			}
			row[key] = strings.Trim(strings.TrimSpace(value), "\"")
		}
	}
	if len(row) > 0 {
		out = append(out, row)
	}
	return out, nil
}
