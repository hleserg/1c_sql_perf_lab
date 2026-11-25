package lab

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"
)

// Run against a separate real database; report and rollback must be usable with no ERP/RAS profile.
func testFullStandSQL(t *testing.T, ctx context.Context, master *sql.DB, p Profile, base *url.URL) {
	const fixture = "perflab_suite_fixture"
	if _, err := master.ExecContext(ctx, "CREATE DATABASE "+fixture); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := master.ExecContext(context.Background(), "DROP DATABASE "+fixture); err != nil {
			t.Error(err)
		}
	}()
	d := *base
	q := d.Query()
	q.Set("database", fixture)
	d.RawQuery = q.Encode()
	db, err := sql.Open("sqlserver", d.String())
	if err != nil {
		t.Fatal(err)
	}
	p.Database = fixture
	p.Samples = 3
	p.Warmup = 1
	p.ERPDatabase = ""
	p.BackupDir = ""
	l := &Live{P: p, db: db}
	defer l.Close()
	if _, err = db.ExecContext(ctx, `ALTER DATABASE CURRENT SET QUERY_STORE = OFF;
 CREATE TABLE dbo.suite_rows(id int NOT NULL PRIMARY KEY, n int NOT NULL, pad char(64) NOT NULL);
 INSERT dbo.suite_rows SELECT TOP(20000) ROW_NUMBER() OVER(ORDER BY (SELECT NULL)), 1, 'x' FROM sys.all_objects a CROSS JOIN sys.all_objects b;
 CREATE STATISTICS suite_n ON dbo.suite_rows(n) WITH FULLSCAN;
 INSERT dbo.suite_rows SELECT TOP(5000) 20000+ROW_NUMBER() OVER(ORDER BY (SELECT NULL)),2,'x' FROM sys.all_objects a CROSS JOIN sys.all_objects b`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `CREATE TABLE dbo.ix_trial(id uniqueidentifier NOT NULL PRIMARY KEY CLUSTERED, payload char(250) NOT NULL);
 INSERT dbo.ix_trial SELECT TOP(40000) NEWID(), 'x' FROM sys.all_objects a CROSS JOIN sys.all_objects b OPTION (MAXDOP 1);
 INSERT dbo.ix_trial SELECT TOP(40000) NEWID(), 'y' FROM sys.all_objects a CROSS JOIN sys.all_objects b OPTION (MAXDOP 1);
 DECLARE @count bigint; SELECT @count=COUNT_BIG(*) FROM dbo.ix_trial WITH (INDEX(1));`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	r := Run{Root: root, Profile: p, JobKey: FullJob.Key, Backend: l}
	report, path, err := r.Execute(ctx, nil)
	if err != nil {
		for _, s := range report.Sections {
			if s.Error != "" {
				t.Log(s.ID, s.Title, s.Error)
			}
		}
		t.Fatal(err)
	}
	if !report.SuiteComplete || len(report.Coverage) != 39 || len(report.Phases) < 3 || report.Workload == "" || filepath.Ext(path) != ".json" {
		t.Fatalf("incomplete %d/%d %v", len(report.Coverage), len(report.Phases), report.Error)
	}
	reorg := false
	for _, sec := range report.Sections {
		if sec.ID == 32 && len(sec.Rows) > 0 && len(sec.Rows[0]) > 0 && sec.Rows[0][0] == "Обслужено" {
			reorg = true
		}
	}
	if !reorg {
		t.Fatal("fragmented used index was not trialled")
	}
	for id, was := range report.Before {
		got, err := l.Get(ctx, id)
		if err != nil || got != was {
			t.Fatalf("rollback %d: got %d want %d, err %v", id, got, was, err)
		}
	}
	if _, err := ApproveReference(root, p, report); err != nil {
		t.Fatal(err)
	}
	cmp := compareBaseline(root, p, report)
	if cmp.Error != "" || len(cmp.Rows) != 1 || cmp.Rows[0][1] != "Совпадают" {
		t.Fatalf("reference %+v", cmp)
	}
}
