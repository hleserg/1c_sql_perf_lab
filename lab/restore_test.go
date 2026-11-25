package lab

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestRestoreMetadata(t *testing.T) {
	rows := []metaRow{
		{"BackupType": "1", "IsDamaged": "false", "BackupSetGUID": "a", "BackupFinishDate": "2026-01-01", "Position": "1"},
		{"BackupType": "2", "BackupSetGUID": "log", "BackupFinishDate": "2026-01-04", "Position": "2"},
		{"BackupType": "1", "BackupSetGUID": "b", "BackupFinishDate": "2026-01-03", "Position": "3"},
		{"BackupType": "1", "IsDamaged": "true", "BackupSetGUID": "c", "BackupFinishDate": "2026-01-05", "Position": "4"}}
	h, e := fullSet(rows)
	if e != nil || h["Position"] != "3" {
		t.Fatal(h, e)
	}
	if _, e = fullSet(rows[3:]); e == nil {
		t.Fatal("accepted damaged backup")
	}
	for _, name := range []string{"master", "MSDB", "tempdb", "model", "distribution", ""} {
		if safeTarget(name) {
			t.Fatal(name)
		}
	}
	if quotedID("test]a") != "[test]]a]" || unicodeLiteral("a'b") != "N'a''b'" {
		t.Fatal("quoting")
	}
	if (Profile{}).RestoreFolder() != `D:\temp` {
		t.Fatal("wrong default folder")
	}
}
func testRestoreSQL(t *testing.T, ctx context.Context, master *sql.DB, p Profile) {
	c, e := master.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	p.Database = "perflab_restore_test"
	p.RestoreDir = "/var/opt/mssql/data"
	path := p.RestoreDir + "/z_restore_integration.bak"
	other := p.RestoreDir + "/a_restore_integration.bak"
	for _, f := range []string{other, path} {
		if _, e = c.ExecContext(ctx, "BACKUP DATABASE [perflab_integration] TO DISK=@p1 WITH COPY_ONLY,CHECKSUM,INIT", f); e != nil {
			t.Fatal(e)
		}
		if f == other {
			time.Sleep(1100 * time.Millisecond)
		}
	}
	targets, e := localTargets(ctx, c, p, "", path)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, target := range targets {
		if target.Profile.Database == "perflab_integration" && target.SourceMatch && target.Login != "" && target.Profile.ExpectedServer == p.ExpectedServer {
			found = true
		}
		if !safeTarget(target.Profile.Database) {
			t.Fatal("system target", target)
		}
	}
	if !found {
		t.Fatal("could not identify original database from backup", targets)
	}
	files, e := listBackups(ctx, c, p.RestoreDir)
	if e != nil {
		t.Fatal(e)
	}
	if len(files) < 2 || files[0].Path != path {
		t.Fatalf("file order %v", files)
	}
	plan, e := prepareRestore(ctx, c, p, path)
	if e != nil {
		t.Fatal(e)
	}
	if plan.Source != "perflab_integration" || plan.TargetID != 0 || len(plan.Files) < 2 {
		t.Fatal(plan)
	}
	runner := &Live{P: p, conn: c, db: master}
	note := func(s string) { t.Log(s) }
	if e = applyRestore(ctx, runner, p, plan, note); e != nil {
		t.Fatal(e)
	}
	// applyRestore owns a session application lock; release it between CLI-equivalent runs.
	unlock := func() {
		var n int
		if e := c.QueryRowContext(ctx, `DECLARE @r int,@key nvarchar(255)=N'1c_sql_perf_lab:'+UPPER(@p1);EXEC @r=master.sys.sp_releaseapplock @Resource=@key,@LockOwner='Session';SELECT @r`, p.Database).Scan(&n); e != nil {
			t.Fatal(e)
		}
	}
	unlock()
	if _, e = c.ExecContext(ctx, "ALTER DATABASE [perflab_restore_test] SET AUTO_UPDATE_STATISTICS_ASYNC ON; DELETE FROM [perflab_restore_test].dbo.perf_fixture"); e != nil {
		t.Fatal(e)
	}
	// Leave a real 1C-like SQL session open with an uncommitted write.
	held, e := master.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer held.Close()
	if _, e = held.ExecContext(ctx, "USE perflab_restore_test; BEGIN TRAN; INSERT dbo.perf_fixture VALUES (999,999);"); e != nil {
		t.Fatal(e)
	}
	plan, e = prepareRestore(ctx, c, p, path)
	if e != nil {
		t.Fatal(e)
	}
	stale := plan
	stale.SetID = "another-set"
	if e = applyRestore(ctx, runner, p, stale, note); e == nil {
		t.Fatal("accepted changed source")
	}
	unlock()
	var cnt int
	if e = held.QueryRowContext(ctx, "SELECT @@TRANCOUNT").Scan(&cnt); e != nil || cnt != 1 {
		t.Fatal("preflight interrupted target", e, cnt)
	}
	if e = applyRestore(ctx, runner, p, plan, note); e != nil {
		t.Fatal(e)
	}
	unlock()
	var state, mode string
	if e = c.QueryRowContext(ctx, "SELECT state_desc,user_access_desc FROM sys.databases WHERE name=@p1", p.Database).Scan(&state, &mode); e != nil || state != "ONLINE" || mode != "MULTI_USER" {
		t.Fatal(state, mode, e)
	}
	if e = c.QueryRowContext(ctx, "SELECT COUNT(*) FROM perflab_restore_test.dbo.perf_fixture").Scan(&cnt); e != nil || cnt != 16000 {
		t.Fatal(cnt, e)
	}
	if e = held.QueryRowContext(ctx, "SELECT @@TRANCOUNT").Scan(&cnt); e == nil {
		t.Fatal("previous session survived restore")
	}
	var overlap int
	if e = c.QueryRowContext(ctx, `SELECT COUNT(*) FROM sys.master_files a JOIN sys.master_files b ON a.physical_name=b.physical_name AND a.database_id<>b.database_id WHERE a.database_id=DB_ID(@p1)`, p.Database).Scan(&overlap); e != nil || overlap != 0 {
		t.Fatal("file collision", e)
	}
	if _, e := prepareRestore(ctx, c, p, strings.Replace(path, "integration.bak", "missing.bak", 1)); e == nil {
		t.Fatal("missing backup accepted")
	}
}
