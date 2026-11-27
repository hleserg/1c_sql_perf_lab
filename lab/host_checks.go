package lab

import (
	"context"
	"errors"
	"fmt"
	"net"

	"strings"
	"time"
)

type hostCheck struct {
	id      int
	title   string
	columns []string
	script  string
}

var windowsHostChecks = []hostCheck{
	{12, "Схема питания и частота CPU по данным Windows на узле SQL", []string{"Схема", "Идентификатор Windows", "Текущая частота WMI, МГц по сокетам"}, `
$clocks = try {
    @(Get-CimInstance -ClassName Win32_Processor -Property DeviceID,CurrentClockSpeed | Where-Object { $_.CurrentClockSpeed -gt 0 } | ForEach-Object { "$($_.DeviceID) $($_.CurrentClockSpeed)" }) -join ', '
} catch { '' }
if (-not $clocks) { $clocks = 'n/a' }
try {
    $plan = Get-CimInstance -Namespace root/cimv2/power -ClassName Win32_PowerPlan -Filter 'IsActive=TRUE' -Property ElementName,InstanceID
} catch { $plan = $null }
if ($null -ne $plan) {
    [pscustomobject]@{ cells = @([string]$plan.ElementName, [string]$plan.InstanceID, $clocks) }
} else {
    # The WMI power provider can refuse a terminal-services session even for this read.
    $description = & "$env:SystemRoot\System32\powercfg.exe" /getactivescheme
    if ($LASTEXITCODE -ne 0 -or $description -notmatch '(?i)\b([a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12})\b\s*(?:\((.*)\))?') {
        throw 'Windows не вернула идентификатор активной схемы питания.'
    }
    [pscustomobject]@{ cells = @([string]$Matches[2], ("Microsoft:PowerPlan\{" + $Matches[1] + "}"), $clocks) }
}`},
	{20, "Microsoft Defender на узле SQL: состояние на момент сбора", []string{"Антивирус включён", "Защита в реальном времени", "Версия сигнатур"}, `
$d = Get-MpComputerStatus -ErrorAction Stop
if ($null -ne $d -and $null -ne $d.AntivirusEnabled -and $null -ne $d.RealTimeProtectionEnabled) {
    [pscustomobject]@{ cells = @([string]$d.AntivirusEnabled, [string]$d.RealTimeProtectionEnabled, [string]$d.AntivirusSignatureVersion) }
}`},
	{37, "Процессы запущенных служб Windows на узле SQL, по загрузке CPU", []string{"Службы одного процесса", "PID", "CPU, % одного ядра", "Рабочий набор процесса, МиБ"}, `
$services = Get-CimInstance -ClassName Win32_Service -Filter "State='Running' AND ProcessId>0" -Property Name,ProcessId | Group-Object ProcessId
$counters = @{}
Get-CimInstance -ClassName Win32_PerfFormattedData_PerfProc_Process -Property IDProcess,PercentProcessorTime,WorkingSet | ForEach-Object {
    $counters[[string]$_.IDProcess] = $_
}
$services | Sort-Object @{Expression={ $counters[$_.Name].PercentProcessorTime }; Descending=$true}, @{Expression={ $counters[$_.Name].WorkingSet }; Descending=$true}, Name | ForEach-Object {
    $p = $counters[$_.Name]
    $cpu = if ($null -ne $p.PercentProcessorTime) { [string]$p.PercentProcessorTime } else { 'n/a' }
    $ram = if ($null -ne $p.WorkingSet) { [string][math]::Round($p.WorkingSet / 1MB, 1) } else { 'n/a' }
    [pscustomobject]@{ cells = @(($_.Group.Name -join ', '), [string]$_.Name, $cpu, $ram) }
}`},
}

func hostUnavailable(reason error) []Section {
	sections := make([]Section, 0, len(windowsHostChecks))
	for _, c := range windowsHostChecks {
		sections = append(sections, Section{ID: c.id, Title: c.title, Error: "Нет данных ОС: " + reason.Error()})
	}
	return sections
}

func isLocalSQLEndpoint(endpoint string, names ...string) bool {
	host := endpoint
	if h, _, err := net.SplitHostPort(endpoint); err == nil {
		host = h
	}
	if host == "." || strings.EqualFold(host, "(local)") {
		return true // go-mssqldb translates both names to localhost.
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	for _, name := range names {
		if strings.EqualFold(host, name) {
			return true
		}
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	addrs, _ := net.InterfaceAddrs()
	for _, addr := range addrs {
		if local, _, err := net.ParseCIDR(addr.String()); err == nil && ip.Equal(local) {
			return true
		}
	}
	return false
}

func checkHostEndpoint(p Profile) (string, error) {
	physical, fqdn, err := windowsHostNames()
	if err != nil {
		return "", err
	}
	if !isLocalSQLEndpoint(p.SQLHost, physical, fqdn, strings.SplitN(fqdn, ".", 2)[0]) {
		return "", errors.New("в профиле указан удалённый адрес SQL; запусти сбор на самом узле SQL")
	}
	return physical, nil
}

// StandHostSections connects to the selected local stand. The suite reuses its
// established SQL connection via the Live method.
func StandHostSections(ctx context.Context, p Profile) []Section {
	if _, err := checkHostEndpoint(p); err != nil {
		return hostUnavailable(err)
	}
	l := &Live{P: p}
	defer l.Close()
	if err := l.connect(ctx, p.Database); err != nil {
		return hostUnavailable(err)
	}
	return l.StandHostSections(ctx)
}

func (l *Live) StandHostSections(ctx context.Context) []Section {
	host, err := checkHostEndpoint(l.P)
	if err != nil {
		return hostUnavailable(err)
	}
	if l.conn == nil {
		return hostUnavailable(errors.New("нет соединения с SQL для проверки узла"))
	}
	bounded, stop := context.WithTimeout(ctx, 5*time.Second)
	var physical, platform, server, database string
	err = l.conn.QueryRowContext(bounded, `SELECT CONVERT(nvarchar(128),SERVERPROPERTY('ComputerNamePhysicalNetBIOS')),
host_platform, CONVERT(nvarchar(128),SERVERPROPERTY('ServerName')), DB_NAME() FROM sys.dm_os_host_info`).Scan(&physical, &platform, &server, &database)
	stop()
	if err != nil {
		return hostUnavailable(fmt.Errorf("проверка имени физического узла SQL: %w", err))
	}
	if !strings.EqualFold(platform, "Windows") || !strings.EqualFold(physical, host) ||
		!strings.EqualFold(server, l.P.ExpectedServer) || !strings.EqualFold(database, l.P.Database) {
		return hostUnavailable(errors.New("SQL не подтвердил выбранный экземпляр и базу на этом Windows-узле"))
	}
	sections := make([]Section, 0, len(windowsHostChecks))
	for _, c := range windowsHostChecks {
		sections = append(sections, readHostSection(ctx, c))
	}
	return sections
}
