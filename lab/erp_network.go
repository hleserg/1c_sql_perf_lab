package lab

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
)

// ERPNetworkSamples is the number of sequential SQL probes in a complete series.
const ERPNetworkSamples = 8

// The driver can dial several addresses for a listener. Only report a TCP
// time and peer when exactly one TCP socket was successfully opened.
type measuredDialer struct {
	net.Dialer
	mu     sync.Mutex
	count  int
	peer   string
	source string
	spent  time.Duration
}

func (d *measuredDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	elapsed := networkTimer()
	c, err := d.Dialer.DialContext(ctx, network, address)
	spent := elapsed()
	if err == nil && strings.HasPrefix(network, "tcp") {
		d.mu.Lock()
		d.count++
		d.peer, d.source, d.spent = c.RemoteAddr().String(), c.LocalAddr().String(), spent
		d.mu.Unlock()
	}
	return c, err
}

func (d *measuredDialer) socket() (string, string, time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.count == 1 {
		return d.source, d.peer, d.spent
	}
	return "", "", 0
}

func matchesERPProbeHost(expected, physical, fqdn string) bool {
	expected = strings.TrimSuffix(strings.TrimSpace(expected), ".")
	return expected != "" && (strings.EqualFold(expected, physical) || strings.EqualFold(expected, fqdn))
}

const hostTCPState = `
$g = [System.Net.NetworkInformation.IPGlobalProperties]::GetIPGlobalProperties()
$v4 = try { [string]$g.GetTcpIPv4Statistics().SegmentsResent } catch { 'n/a' }
$v6 = try { [string]$g.GetTcpIPv6Statistics().SegmentsResent } catch { 'n/a' }
$n = @(Get-Process -Name rphost -ErrorAction SilentlyContinue).Count
[pscustomobject]@{ cells = @([string]$n, $v4, $v6) }`

const hostICMP = `$p = New-Object System.Net.NetworkInformation.Ping
try {
  1..8 | ForEach-Object {
    $sample = [string]$_
    $at = [DateTime]::UtcNow.ToString('o')
    try {
      $r = $p.Send([System.Net.IPAddress]::Parse('PEER'), 600)
      $ms = if ($r.Status -eq 'Success') { [string]$r.RoundtripTime } else { '—' }
      [pscustomobject]@{ cells = @($sample, $at, $ms, [string]$r.Status) }
    } catch { [pscustomobject]@{ cells = @($sample, $at, '—', 'Ошибка ICMP') } }
  }
} finally { $p.Dispose() }`

type networkSample struct {
	databaseID, source, peer string
	tcp, read                time.Duration
}

var errNetworkIdentity = errors.New("экземпляр, база, идентификатор или транспорт SQL не совпали")

func measureSQLSocket(ctx context.Context, p Profile, endpoint, identity string) (result networkSample, err error) {
	connector, err := mssql.NewConnector(endpoint)
	if err != nil {
		return result, err
	}
	dial := &measuredDialer{}
	connector.Dialer = dial
	db := sql.OpenDB(connector)
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return result, err
	}
	defer conn.Close()
	var server, database, transport string
	var id int
	err = conn.QueryRowContext(ctx, `SELECT CONVERT(nvarchar(128),SERVERPROPERTY('ServerName')),DB_NAME(),database_id,
 CONVERT(varchar(36),service_broker_guid),CONVERT(nvarchar(40),COALESCE(CONNECTIONPROPERTY('physical_net_transport'),CONNECTIONPROPERTY('net_transport')))
 FROM sys.databases WHERE database_id=DB_ID()`).Scan(&server, &database, &id, &result.databaseID, &transport)
	if err != nil {
		return result, err
	}
	if id <= 4 || !strings.EqualFold(server, p.ExpectedServer) || !strings.EqualFold(database, p.Database) ||
		result.databaseID == "" || (identity != "" && !strings.EqualFold(identity, result.databaseID)) || !strings.EqualFold(transport, "TCP") {
		return result, errNetworkIdentity
	}
	elapsed := networkTimer()
	var n int
	err = conn.QueryRowContext(ctx, "SELECT CONVERT(int,1)").Scan(&n)
	result.read = elapsed()
	if err == nil && n != 1 {
		err = errors.New("контрольный SELECT вернул неожиданное значение")
	}
	result.source, result.peer, result.tcp = dial.socket()
	return result, err
}

func networkMS(d time.Duration) string {
	return fmt.Sprintf("%.3f", float64(d)/float64(time.Millisecond))
}

func tcpChange(before, after string) string {
	a, e1 := strconv.ParseInt(before, 10, 64)
	b, e2 := strconv.ParseInt(after, 10, 64)
	if e1 != nil || e2 != nil || a < 0 || b < a {
		return "—"
	}
	return strconv.FormatInt(b-a, 10)
}

// ERPNetworkEvidence opens independent, bounded, TCP-only SQL connections.
// It runs on the named ERP worker host. A profiling workstation cannot serve
// as the source of an ERP-to-SQL network result.
func (l *Live) ERPNetworkEvidence(ctx context.Context, expectedERPHost string) (s Section) {
	start := time.Now()
	defer func() { s.ElapsedMS = time.Since(start).Milliseconds() }()
	s = Section{ID: 31, Title: "Сеть от рабочего узла 1С до SQL стенда", Columns: []string{"Проба", "Начало UTC", "Источник → назначение", "TCP connect мс", "SELECT 1 мс", "ICMP мс", "Результат"}}
	ctx, cancel := context.WithTimeout(ctx, 80*time.Second)
	defer cancel()
	physical, fqdn, err := windowsHostNames()
	if err != nil {
		s.Error = "Для сетевого замера запусти лабораторию на объявленном рабочем Windows-узле 1С"
		return
	}
	if !matchesERPProbeHost(expectedERPHost, physical, fqdn) {
		s.Error = fmt.Sprintf("Указан узел 1С %q, лаборатория запущена на %q", expectedERPHost, physical)
		return
	}
	state := hostCheck{31, "Состояние TCP источника", []string{"rphost", "IPv4", "IPv6"}, hostTCPState}
	before := readHostSection(ctx, state)
	if before.Error != "" {
		s.Error = before.Error
		return
	}
	count, err := strconv.Atoi(before.Rows[0][0])
	if err != nil || count < 1 {
		s.Error = fmt.Sprintf("На узле %s отсутствует видимый рабочий процесс rphost", physical)
		return
	}
	utc := func(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
	route := physical + " → " + l.P.ExpectedServer + " / " + l.P.Database
	s.Rows = append(s.Rows, []string{"Узлы", utc(start), route, "—", "—", "—", fmt.Sprintf("Рабочих процессов rphost: %d. TCP устанавливается заново для каждой пробы.", count)})
	var identity string
	peers := map[string]bool{}
	var tcpMS, readMS []float64
	var successful int
	profile := l.P
	profile.SQLProtocol = "tcp"
	endpoint := sqlEndpoint(profile, profile.Database)
	for i := 1; i <= ERPNetworkSamples; i++ {
		if ctx.Err() != nil {
			break
		}
		t := time.Now()
		trial, stop := context.WithTimeout(ctx, 5*time.Second)
		m, e := measureSQLSocket(trial, profile, endpoint, identity)
		stop()
		row := []string{strconv.Itoa(i), utc(t), route, "—", "—", "—", "SQL ответил"}
		if e != nil {
			row[6] = e.Error()
			s.Rows = append(s.Rows, row)
			if errors.Is(e, errNetworkIdentity) || successful == 0 {
				s.Error = "Не удалось подтвердить TCP-сеанс выбранного стенда: " + e.Error()
				return
			}
			continue
		}
		identity = m.databaseID
		successful++
		row[4] = networkMS(m.read)
		readMS = append(readMS, float64(m.read)/float64(time.Millisecond))
		if m.peer != "" {
			row[2], row[3] = m.source+" → "+m.peer, networkMS(m.tcp)
			tcpMS = append(tcpMS, float64(m.tcp)/float64(time.Millisecond))
			host, _, _ := net.SplitHostPort(m.peer)
			if ip := net.ParseIP(host); ip != nil {
				peers[ip.String()] = true
				if isLocalSQLEndpoint(host) {
					row[6] += "; адрес SQL принадлежит этому же узлу"
				}
			}
		} else {
			row[6] += "; у драйвера нет единственного TCP-сокета для замера подключения"
		}
		s.Rows = append(s.Rows, row)
	}
	if ctx.Err() != nil {
		s.Error = ctx.Err().Error()
		return
	}
	tcpMedian := "—"
	if len(tcpMS) > 0 {
		tcpMedian = fmt.Sprintf("%.3f", Median(tcpMS))
	}
	s.Rows = append(s.Rows, []string{"Медианы", utc(time.Now()), route, tcpMedian, fmt.Sprintf("%.3f", Median(readMS)), "—", fmt.Sprintf("SQL-проб с ответом: %d из %d; измерений TCP connect: %d", successful, ERPNetworkSamples, len(tcpMS))})
	after := readHostSection(ctx, state)
	if after.Error == "" {
		s.Rows = append(s.Rows, []string{"Повторы TCP узла", utc(time.Now()), physical, "—", "—", "—", "Сегментов, отправленных повторно за серию, по всем соединениям узла: IPv4 " + tcpChange(before.Rows[0][1], after.Rows[0][1]) + ", IPv6 " + tcpChange(before.Rows[0][2], after.Rows[0][2])})
	} else {
		s.Rows = append(s.Rows, []string{"Повторы TCP узла", utc(time.Now()), physical, "—", "—", "—", after.Error})
	}
	if len(peers) == 1 {
		for peer := range peers {
			// ParseIP.String above supplies an IP literal with no PowerShell syntax.
			pingRows := readHostSection(ctx, hostCheck{31, "Ответы ICMP", []string{"№", "UTC", "мс", "Статус"}, strings.ReplaceAll(hostICMP, "PEER", peer)})
			if pingRows.Error != "" {
				s.Rows = append(s.Rows, []string{"ICMP", utc(time.Now()), peer, "—", "—", "—", pingRows.Error})
			}
			var replies int
			for _, r := range pingRows.Rows {
				if r[3] == "Success" {
					replies++
				}
				s.Rows = append(s.Rows, []string{"ICMP " + r[0], r[1], physical + " → " + peer, "—", "—", r[2], r[3]})
			}
			if pingRows.Error == "" {
				s.Rows = append(s.Rows, []string{"Ответы ICMP", utc(time.Now()), physical + " → " + peer, "—", "—", "—", fmt.Sprintf("Получено ответов: %d из %d. Тайм-аут одной пробы 600 мс.", replies, len(pingRows.Rows))})
			}
		}
	} else {
		s.Rows = append(s.Rows, []string{"ICMP", utc(time.Now()), physical, "—", "—", "—", "У серии отсутствует единственный подтверждённый IP для ICMP"})
	}
	if ctx.Err() != nil {
		s.Error = ctx.Err().Error()
	} else if successful != ERPNetworkSamples {
		s.Error = fmt.Sprintf("Успешных SQL-проб %d из %d", successful, ERPNetworkSamples)
	}
	return
}
