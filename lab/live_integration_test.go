package lab

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"testing"
	"time"
)

// CI supplies a disposable SQL Server and database. No default connection.
func TestSQLIntegration(t *testing.T) {
	dsn := os.Getenv("PERFLAB_CI_DSN")
	if dsn == "" {
		t.Skip("requires ephemeral SQL Server")
	}
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
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
	var server string
	for {
		e = master.QueryRowContext(ctx, "SELECT CAST(SERVERPROPERTY('ServerName') AS nvarchar(256))").Scan(&server)
		if e == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("SQL Server did not start")
		}
		time.Sleep(time.Second)
	}
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
}

type sqlWithSample struct {
	*Live
	demo *Demo
}

func (s *sqlWithSample) Measure(c context.Context) (map[string]float64, error) {
	return s.demo.Measure(c)
}
