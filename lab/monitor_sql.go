package lab

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MonitorReading contains point-in-time gauges and cumulative file counters.
// Keep the SQL start time and database identity alongside the counters so that
// samples separated by a restart or database replacement can be distinguished.
type MonitorReading struct {
	DatabaseGUID                string
	SQLStartedServerTime        string
	SQLCPUTimeMS                int64
	LogicalCPUs                 int
	SQLMemoryMiB                int64
	AvailableOSMemoryMiB        int64
	InstanceUserSessions        int64
	BlockedDBRequests           int64
	DBReads, DBWrites           int64
	DBReadWaitMS, DBWriteWaitMS int64
}

type MonitorSample struct {
	Blocking *Section `json:",omitempty"`
	UTC      time.Time
	Reading  *MonitorReading `json:",omitempty"`
	Error    string          `json:",omitempty"`
}

// ReadMonitorSample opens a short-lived read-only session for one sample; it
// never acquires the experiment lock and can run alongside an ERP measurement.
func ReadMonitorSample(ctx context.Context, p Profile) MonitorSample {
	var s MonitorSample
	bounded, stop := context.WithTimeout(ctx, 25*time.Second)
	defer stop()
	l := &Live{P: p}
	defer l.Close()
	err := l.connect(bounded, p.Database)
	if err == nil {
		s.Reading, err = l.monitorReading(bounded)
	}
	s.UTC = time.Now().UTC()
	if err != nil {
		s.Error = err.Error()
	} else if p.MonitorERP {
		blocking := l.BlockingEvidence(bounded)
		s.Blocking = &blocking
	}
	return s
}

func (l *Live) monitorReading(ctx context.Context) (*MonitorReading, error) {
	var server, database string
	var id int
	var r MonitorReading
	err := l.conn.QueryRowContext(ctx, `SELECT CONVERT(nvarchar(128),SERVERPROPERTY('ServerName')), DB_NAME(), DB_ID(),
CONVERT(varchar(36),database_guid) FROM sys.database_recovery_status WHERE database_id=DB_ID()`).Scan(&server, &database, &id, &r.DatabaseGUID)
	if err != nil {
		return nil, err
	}
	if id <= 4 || !strings.EqualFold(server, l.P.ExpectedServer) || !strings.EqualFold(database, l.P.Database) {
		return nil, fmt.Errorf("цель мониторинга отличается от профиля: SQL %q, база %q", server, database)
	}
	err = l.conn.QueryRowContext(ctx, `SELECT CONVERT(varchar(23),s.sqlserver_start_time,121), s.process_kernel_time_ms + s.process_user_time_ms, s.cpu_count,
p.physical_memory_in_use_kb / 1024, m.available_physical_memory_kb / 1024,
(SELECT COUNT_BIG(*) FROM sys.dm_exec_sessions WHERE is_user_process=1 AND session_id<>@@SPID),
(SELECT COUNT_BIG(*) FROM sys.dm_exec_requests WHERE database_id=DB_ID() AND blocking_session_id>0),
f.reads, f.writes, f.read_wait_ms, f.write_wait_ms
FROM sys.dm_os_sys_info AS s CROSS JOIN sys.dm_os_process_memory AS p CROSS JOIN sys.dm_os_sys_memory AS m
CROSS JOIN (SELECT SUM(num_of_reads) AS reads,SUM(num_of_writes) AS writes,
SUM(io_stall_read_ms) AS read_wait_ms,SUM(io_stall_write_ms) AS write_wait_ms
FROM sys.dm_io_virtual_file_stats(DB_ID(),NULL)) AS f`).Scan(&r.SQLStartedServerTime, &r.SQLCPUTimeMS, &r.LogicalCPUs, &r.SQLMemoryMiB, &r.AvailableOSMemoryMiB,
		&r.InstanceUserSessions, &r.BlockedDBRequests, &r.DBReads, &r.DBWrites, &r.DBReadWaitMS, &r.DBWriteWaitMS)
	if err != nil {
		return nil, err
	}
	return &r, nil
}
