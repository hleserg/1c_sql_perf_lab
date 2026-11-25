package lab

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWeeklyRejectsProfileDriftAndStaleBackups(t *testing.T) {
	p := Example()
	o := WeeklyOptions{Server: p.ExpectedServer, Measurement: p.Database, Source: "backup_source", Folder: "/backups", MaxAge: 24 * time.Hour}
	if err := o.Validate(p); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*WeeklyOptions){
		func(o *WeeklyOptions) { o.Server = "OTHER" }, func(o *WeeklyOptions) { o.Measurement = "other" },
		func(o *WeeklyOptions) { o.Folder = "relative" }, func(o *WeeklyOptions) { o.Source = "master" },
		func(o *WeeklyOptions) { o.MaxAge = 169 * time.Hour },
	} {
		bad := o
		mutate(&bad)
		if bad.Validate(p) == nil {
			t.Fatalf("accepted invalid options: %+v", bad)
		}
	}
	tm := time.Date(2026, 1, 14, 10, 0, 0, 0, time.UTC)
	header := metaRow{"DatabaseName": o.Source, "BackupSetGUID": "set-id", "BackupFinishDate": "2026-01-13 10:00:00.0000000"}
	if !eligibleWeekly(header, o.Source, "other", tm, o.MaxAge) {
		t.Fatal("did not accept fresh backup at boundary")
	}
	if eligibleWeekly(header, o.Source, "SET-ID", tm, o.MaxAge) || eligibleWeekly(header, "other", "", tm, o.MaxAge) ||
		eligibleWeekly(header, o.Source, "", tm.Add(time.Second), o.MaxAge) || eligibleWeekly(header, o.Source, "", tm.Add(-25*time.Hour), o.MaxAge) {
		t.Fatal("accepted stale, duplicate, future or unrelated backup")
	}
	if _, e := weeklyPlan(context.Background(), nil, p, o, ""); e == nil {
		t.Fatal("accepted ordinary target")
	}
}

func TestSQLIntegrationWeekly(t *testing.T) {
	dsn := os.Getenv("PERFLAB_CI_DSN")
	if dsn == "" {
		t.Skip("requires ephemeral SQL Server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	db, e := sql.Open("sqlserver", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	server := waitSQLIntegration(t, db)
	c, e := db.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()

	token := time.Now().UTC().Format("20060102150405.000000000")
	fixture := "perflab_verify_fixture_" + token
	if _, e = c.ExecContext(ctx, "CREATE DATABASE "+quotedID(fixture)); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if _, e := c.ExecContext(context.Background(), "DROP DATABASE "+quotedID(fixture)); e != nil {
			t.Error(e)
		}
	}()
	const folder = "/var/opt/mssql/data"
	if _, e = c.ExecContext(ctx, "BACKUP DATABASE "+quotedID(fixture)+" TO DISK=@p1 WITH COPY_ONLY,CHECKSUM", folder+"/"+fixture+".bak"); e != nil {
		t.Fatal(e)
	}
	o := WeeklyOptions{Server: server, Measurement: fixture, Source: fixture, Folder: folder, MaxAge: 24 * time.Hour}
	p := Example()
	p.ExpectedServer = server
	p.Database = fixture
	if e = o.Validate(p); e != nil {
		t.Fatal(e)
	}
	p.Database = "perflab_weekly_0123456789abcdef0123456789abcdef"
	l := &Live{P: p, db: db, conn: c}
	plan, e := weeklyPlan(ctx, c, p, o, "")
	if e != nil {
		t.Fatal(e)
	}
	if plan.TargetID != 0 {
		t.Fatal("target already exists")
	}
	if _, e = weeklyPlan(ctx, c, p, o, plan.SetID); e == nil {
		t.Fatal("accepted already tested set")
	}
	log := &restoreLog{emit: func(s string, _ RestoreProgress) { t.Log(s) }}
	if e = applyRestore(ctx, l, p, plan, log); e != nil {
		t.Fatal(e)
	}
	if _, e = weeklyPlan(ctx, c, p, o, ""); e == nil {
		t.Fatal("would overwrite existing target")
	}
	pending := filepath.Join(t.TempDir(), "pending.json")
	if e = SaveJSON(pending, p.Database); e != nil {
		t.Fatal(e)
	}
	if e = checkWeeklyPending(ctx, c, pending); e == nil {
		t.Fatal("would allocate another database after an interrupted check")
	}
	var rep Report
	if e = weeklyCheckAndRemove(ctx, l, plan, &rep, func(s string) { t.Log(s) }); e != nil {
		t.Fatal(e)
	}
	if len(rep.Sections) != 1 || rep.Sections[0].ID != 15 || rep.Sections[0].Error != "" {
		t.Fatal(rep.Sections)
	}
	if e = checkWeeklyPending(ctx, c, pending); e != nil {
		t.Fatal(e)
	}
	var remaining, protected int
	if e = c.QueryRowContext(ctx, "SELECT COALESCE(DB_ID(@p1),0),COALESCE(DB_ID(@p2),0)", p.Database, fixture).Scan(&remaining, &protected); e != nil || remaining != 0 || protected <= 4 {
		t.Fatalf("temporary=%d measurement=%d err=%v", remaining, protected, e)
	}
}
