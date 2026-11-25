package lab

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	_ "github.com/microsoft/go-mssqldb"
)

//go:embed workload.ps1
var script string
var uuid = regexp.MustCompile("(?i)^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$")

type Live struct {
	P    Profile
	db   *sql.DB
	conn *sql.Conn
}

func (l *Live) Close() {
	if l.conn != nil {
		l.conn.Close()
	}
	if l.db != nil {
		l.db.Close()
	}
}
func clusterKey(p Profile) string {
	return strings.ToLower(p.Cluster.Endpoint + "|" + p.Cluster.ClusterID + "|" + p.Cluster.ServerID)
}
func readPending(file string) (Journal, error) {
	var j Journal
	b, e := os.ReadFile(file)
	if e == nil {
		e = json.Unmarshal(b, &j)
	}
	return j, e
}
func (l *Live) connect(ctx context.Context, database string) error {
	p := l.P
	u := url.URL{Scheme: "sqlserver", Host: p.SQLHost, Path: p.Instance}
	q := url.Values{"database": {database}, "app name": {"1C SQL Perf Lab"}, "encrypt": {"true"}, "connection timeout": {"12"}}
	if p.SQLProtocol != "" {
		q.Set("protocol", p.SQLProtocol)
	}
	if p.TrustServerCertificate {
		q.Set("TrustServerCertificate", "true")
	}
	u.RawQuery = q.Encode()
	var e error
	if l.db == nil {
		l.db, e = sql.Open("sqlserver", u.String())
	}
	if e != nil {
		return e
	}
	l.db.SetMaxOpenConns(1)
	l.conn, e = l.db.Conn(ctx)
	return e
}
func (l *Live) Identify(ctx context.Context) (map[string]string, error) {
	p := l.P
	e := l.connect(ctx, p.Database)
	if e != nil {
		return nil, e
	}
	if e = labLock(ctx, l.conn, p.Database, "Shared"); e != nil {
		return nil, e
	}
	var server, db, guid, version, edition, async, engineDOP string
	var dbid int
	e = l.conn.QueryRowContext(ctx, `SELECT CAST(SERVERPROPERTY('ServerName') AS nvarchar(256)),name, database_id, CONVERT(varchar(36),service_broker_guid),CAST(SERVERPROPERTY('ProductVersion') AS varchar(40)),CAST(SERVERPROPERTY('Edition') AS nvarchar(256)),CONVERT(int,is_auto_update_stats_on),(SELECT CONVERT(int,value_in_use) FROM sys.configurations WHERE name='max degree of parallelism') FROM sys.databases WHERE database_id=DB_ID()`).Scan(&server, &db, &dbid, &guid, &version, &edition, &async, &engineDOP)
	if e != nil {
		return nil, e
	}
	if dbid <= 4 || !strings.EqualFold(db, p.Database) || !strings.EqualFold(server, p.ExpectedServer) {
		return nil, fmt.Errorf("цель отличается от профиля: SQL %q, база %q", server, db)
	}
	var lock int
	e = l.conn.QueryRowContext(ctx, `DECLARE @r int; EXEC @r=sys.sp_getapplock @Resource=N'1c_sql_perf_lab',@LockMode='Exclusive',@LockOwner='Session',@LockTimeout=0; SELECT @r`).Scan(&lock)
	if e != nil {
		return nil, e
	}
	if lock < 0 {
		return nil, errors.New("на этой базе уже идёт эксперимент")
	}
	facts := map[string]string{"SQL": server, "База": db, "Идентификатор базы": guid, "Сборка SQL": version, "Редакция": edition, "Автостатистика": async, "MAXDOP экземпляра": engineDOP}
	for _, id := range []int{19, 24, 25} {
		v, e := l.Get(ctx, id)
		if e != nil {
			return nil, e
		}
		facts[ByID(id).Title] = strconv.Itoa(v)
	}
	if p.Cluster.RAC != "" {
		v, e := l.Get(ctx, ClusterID)
		if e != nil {
			return nil, e
		}
		facts["Соединений на процесс"] = strconv.Itoa(v)
	}
	l.extras(ctx, facts)
	return facts, nil
}

var scopeKeys = map[int]string{19: "MAXDOP", 24: "QUERY_OPTIMIZER_HOTFIXES"}

func (l *Live) Get(ctx context.Context, id int) (int, error) {
	if id == ClusterID {
		out, e := l.rac(ctx, "info")
		if e != nil {
			return 0, e
		}
		for _, line := range strings.Split(out, "\n") {
			k, v, ok := strings.Cut(line, ":")
			if ok && strings.TrimSpace(k) == "connections-limit" {
				return strconv.Atoi(strings.TrimSpace(v))
			}
		}
		return 0, errors.New("rac не вернул connections-limit")
	}
	var v int
	var e error
	if id == 25 {
		e = l.conn.QueryRowContext(ctx, `SELECT CONVERT(int,is_auto_update_stats_async_on) FROM sys.databases WHERE database_id=DB_ID()`).Scan(&v)
	} else if key, ok := scopeKeys[id]; ok {
		e = l.conn.QueryRowContext(ctx, `SELECT CONVERT(int,value) FROM sys.database_scoped_configurations WHERE name=@p1`, key).Scan(&v)
	} else {
		return 0, errors.New("неизвестный параметр")
	}
	return v, e
}
func (l *Live) Set(ctx context.Context, id, v int) error {
	var command string
	switch id {
	case ClusterID:
		if v < 0 || v > 2147483647 {
			return errors.New("неверный лимит соединений")
		}
		_, e := l.rac(ctx, "update", "--connections-limit="+strconv.Itoa(v))
		return e
	case 19:
		if v < 0 || v > 32767 {
			return errors.New("недопустимый MAXDOP")
		}
		command = "ALTER DATABASE SCOPED CONFIGURATION SET MAXDOP = " + strconv.Itoa(v)
	case 24, 25:
		if v != 0 && v != 1 {
			return errors.New("недопустимый переключатель")
		}
		flag := []string{"OFF", "ON"}[v]
		if id == 24 {
			command = "ALTER DATABASE SCOPED CONFIGURATION SET QUERY_OPTIMIZER_HOTFIXES = " + flag
		} else {
			command = "ALTER DATABASE CURRENT SET AUTO_UPDATE_STATISTICS_ASYNC " + flag
		}
	default:
		return errors.New("неизвестный эксперимент")
	}
	_, e := l.conn.ExecContext(ctx, command)
	return e
}
func (l *Live) rac(ctx context.Context, verb string, extra ...string) (string, error) {
	c := l.P.Cluster
	if c.RAC == "" || c.Endpoint == "" || strings.HasPrefix(c.Endpoint, "-") || !uuid.MatchString(c.ClusterID) || !uuid.MatchString(c.ServerID) {
		return "", errors.New("заполни путь rac, адрес RAS и UUID кластера и рабочего сервера")
	}
	bounded, stop := context.WithTimeout(ctx, 25*time.Second)
	defer stop()
	args := []string{"server", verb, "--cluster=" + c.ClusterID, "--server=" + c.ServerID}
	args = append(args, extra...)
	args = append(args, c.Endpoint)
	out, e := exec.CommandContext(bounded, c.RAC, args...).CombinedOutput()
	if e != nil {
		return "", fmt.Errorf("rac server %s: %w · %s", verb, e, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
func encodePS(s string) string {
	u := utf16.Encode([]rune(s))
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, u)
	return base64.StdEncoding.EncodeToString(b.Bytes())
}
func (l *Live) Measure(ctx context.Context) (map[string]float64, error) {
	p := l.P
	one := func(worker int) (map[string]float64, error) {
		var cmd *exec.Cmd
		if len(p.Workload) > 0 {
			cmd = exec.CommandContext(ctx, p.Workload[0], p.Workload[1:]...)
		} else {
			if p.ERPDatabase == "" || p.ERPServer == "" || !uuid.MatchString(p.DocumentUUID) || p.Document == "" {
				return nil, errors.New("для нагрузки заполни ERPServer, ERPDatabase, Document и DocumentUUID либо задай команду Workload")
			}
			cmd = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePS(script))
		}
		cmd.Env = append(os.Environ(), "PERFLAB_ERP_SERVER="+p.ERPServer, "PERFLAB_ERP_DATABASE="+p.ERPDatabase, "PERFLAB_DOCUMENT="+p.Document, "PERFLAB_UUID="+p.DocumentUUID, "PERFLAB_WORKER="+strconv.Itoa(worker), "PERFLAB_SQL_DATABASE="+p.Database)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		b, e := cmd.Output()
		if e != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("сеанс %d: %w · %s", worker, e, strings.TrimSpace(stderr.String()))
		}
		var result map[string]float64
		if e = json.Unmarshal(bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf}), &result); e != nil {
			return nil, errors.New("нагрузка должна вернуть JSON {операция: миллисекунды}")
		}
		return result, nil
	}
	results := make([]map[string]float64, p.Sessions)
	errs := make([]error, p.Sessions)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], errs[i] = one(i + 1) }(i)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return nil, e
		}
	}
	out := map[string]float64{}
	for _, r := range results {
		if len(r) != len(results[0]) {
			return nil, errors.New("сеансы выполнили разный набор операций")
		}
		for k, v := range r {
			if _, ok := results[0][k]; !ok || v <= 0 {
				return nil, errors.New("ошибка в результате сеанса")
			}
			out[k] += v / float64(len(results))
		}
	}
	return out, nil
}
