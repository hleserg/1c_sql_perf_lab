package lab

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"testing"
	"time"
)

// Each integration test can be the first to reach a newly started container.
func waitSQLIntegration(t *testing.T, db *sql.DB) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for {
		attempt, stop := context.WithTimeout(ctx, 5*time.Second)
		var server string
		err := db.QueryRowContext(attempt, `SELECT CONVERT(nvarchar(128),SERVERPROPERTY('ServerName'))
 WHERE HAS_DBACCESS(N'model')=1 AND HAS_DBACCESS(N'tempdb')=1`).Scan(&server)
		stop()
		if err == nil {
			t.Log("SQL Server готов принимать задания стенда")
			return server
		}
		select {
		case <-ctx.Done():
			t.Fatalf("SQL Server не стал доступен за 2 минуты: %v", err)
		case <-time.After(time.Second):
		}
	}
}

// CI supplies a disposable SQL Server and database. No default connection.
func TestSQLIntegration(t *testing.T) {
	dsn := os.Getenv("PERFLAB_CI_DSN")
	if dsn == "" {
		t.Skip("requires ephemeral SQL Server")
	}
	ctx, stop := context.WithTimeout(context.Background(), 12*time.Minute)
	defer stop()
	base, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	master, e := sql.Open("sqlserver", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer master.Close()
	server := waitSQLIntegration(t, master)
	const database = "perflab_integration"
	if _, e = master.ExecContext(ctx, "CREATE DATABASE "+database); e != nil {
		t.Fatal(e)
	}
	q := base.Query()
	q.Set("database", database)
	base.RawQuery = q.Encode()
	db, e := sql.Open("sqlserver", base.String())
	if e != nil {
		t.Fatal(e)
	}
	p := Example()
	p.ExpectedServer = server
	p.Database = database
	p.SQLHost = base.Host
	p.Samples = 3
	p.MaxDOP = 1
	live := &Live{P: p, db: db}
	defer live.Close()
	// The workload is synthetic here; all configuration reads, writes and locking use SQL Server.
	d := NewDemo()
	d.Delay = 0
	backend := &sqlWithSample{Live: live, demo: d}
	r := Run{Root: t.TempDir(), Profile: p, Backend: backend}
	rep, _, e := r.Execute(ctx, []int{19, 25})
	if e != nil {
		t.Fatal(e)
	}
	if len(rep.Phases) != 5 {
		t.Fatalf("missing phases: %v", rep.Phases)
	}
	for _, id := range []int{19, 25} {
		v, e := live.Get(ctx, id)
		if e != nil || v != rep.Before[id] {
			t.Fatalf("restore %d: value=%d err=%v", id, v, e)
		}
	}
	t.Run("blocking_metadata", func(t *testing.T) { testBlockingSQL(t, ctx, live) })
	t.Run("extended_settings", func(t *testing.T) { testExtendedSettingsSQL(t, ctx, live) })
	t.Run("sql_only_workload", func(t *testing.T) { testSQLReadWorkload(t, ctx, live) })
	if _, e = live.conn.ExecContext(ctx, `CREATE TABLE dbo.perf_fixture(id int, v int); INSERT INTO dbo.perf_fixture SELECT TOP(12000) 1,1 FROM sys.all_objects a CROSS JOIN sys.all_objects b; CREATE STATISTICS perf_v ON dbo.perf_fixture(v) WITH FULLSCAN; INSERT INTO dbo.perf_fixture SELECT TOP(4000) 2,2 FROM sys.all_objects a CROSS JOIN sys.all_objects b;`); e != nil {
		t.Fatal(e)
	}
	live.P.BackupDir = "/var/opt/mssql/data"
	for _, key := range []string{"inspect", "stats", "stats", "checkdb", "backup"} {
		t.Run(key, func(t *testing.T) {
			var sections []Section
			err := live.Work(ctx, key, func(s Section) error { sections = append(sections, s); return nil }, func(s string) { t.Log(s) })
			if err != nil {
				t.Fatalf("%s: %v", key, err)
			}
			switch key {
			case "inspect":
				testInspectionEvidence(t, sections, database)
				if len(sections) != len(inspections)+1 {
					t.Fatal("incomplete inspection")
				}
				if len(sections[0].Rows) == 0 {
					t.Fatal("missing fixture statistics")
				}
			case "stats":
				if t.Name() == "TestSQLIntegration/stats" {
					if len(sections) < 2 || sections[1].Rows[0][0] != "Обновлено" {
						t.Fatal(sections)
					}
				} else if len(sections[0].Rows) != 0 {
					t.Fatal("unexpected repeat update")
				}
			case "backup":
				if len(sections) != 4 || sections[1].Rows[0][5] != "VERIFYONLY успешно" || sections[3].Rows[0][5] != "VERIFYONLY успешно" {
					t.Fatal(sections)
				}
			}
		})
	}

	t.Run("mail", func(t *testing.T) { testMailSQL(t, ctx, live) })
	t.Run("network_endpoint", func(t *testing.T) { testSQLNetworkEndpoint(t, ctx, p, base.String()) })
	t.Run("monitor", func(t *testing.T) { testMonitorSQL(t, ctx, live) })
	t.Run("access_status_and_repair", func(t *testing.T) { testAccessStatusSQL(t, ctx, live) })
	// Subsequent subtests model independent CLI invocations. Verify the
	// lock conflict while the old session is alive and its immediate release.
	t.Run("instance_lock_lifetime", func(t *testing.T) {
		other, err := master.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		if live.instanceTrialsUsed {
			if err := instanceLock(ctx, other, "Shared"); err == nil {
				t.Fatal("second session acquired shared lock during instance trial")
			}
		}
		live.Close()
		if err := instanceLock(ctx, other, "Exclusive"); err != nil {
			t.Fatalf("old session retained master lock: %v", err)
		}
		if err := releaseInstanceLock(ctx, other); err != nil {
			t.Fatal(err)
		}
	})
	live.Close() // idempotent, also handles an early assertion in the subtest
	t.Run("suite_live_sql", func(t *testing.T) { testFullStandSQL(t, ctx, master, p, base) })
	t.Run("restore_connection_survives_idle_read", func(t *testing.T) {
		p := Example()
		p.SQLHost = base.Host
		endpoint, err := url.Parse(sqlEndpoint(p, "master"))
		if err != nil {
			t.Fatal(err)
		}
		endpoint.User = base.User
		q := endpoint.Query()
		q.Set("encrypt", "disable")
		endpoint.RawQuery = q.Encode()
		sqlDB, err := sql.Open("sqlserver", endpoint.String())
		if err != nil {
			t.Fatal(err)
		}
		live := &Live{P: p, db: sqlDB}
		defer live.Close()
		if err = live.connect(ctx, "master"); err != nil {
			t.Fatal(err)
		}
		var n int
		if err = live.conn.QueryRowContext(ctx, "WAITFOR DELAY '00:00:14'; SELECT 1").Scan(&n); err != nil || n != 1 {
			t.Fatalf("long SQL read: value=%d, err=%v", n, err)
		}
	})
	t.Run("restore_in_use", func(t *testing.T) { testRestoreSQL(t, ctx, master, p) })

}

type sqlWithSample struct {
	*Live
	demo *Demo
}

func (s *sqlWithSample) Measure(c context.Context) (map[string]float64, error) {
	return s.demo.Measure(c)
}
