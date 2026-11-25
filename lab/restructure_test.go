package lab

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRestructureProofRequiresMatchingCompletedOperation(t *testing.T) {
	now := time.Now().UTC()
	p := RestructurePlan{Server: "sql", PackageID: "release", PackageSHA256: strings.Repeat("1", 64), BackupSetUUID: "backup"}
	r := restructureResult{started: now, finished: now.Add(time.Second)}
	good := restructureProof{Version: 1, TrialID: "run", Server: "sql", Database: "a", DatabaseGUID: "db", BackupSetUUID: "backup", PackageID: p.PackageID, PackageSHA256: p.PackageSHA256,
		Event: "restructure-completed", Started: now.Add(time.Millisecond), Finished: now.Add(time.Second), Operations: 1, EvidencePath: filepath.Join(t.TempDir(), "gate.log"), EvidenceSHA256: strings.Repeat("2", 64)}
	r.proof = good
	if err := p.validateProof(r, "a", "db", "run"); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*restructureProof){func(f *restructureProof) { f.Operations = 0 }, func(f *restructureProof) { f.Event = "success" }, func(f *restructureProof) { f.PackageSHA256 = "" }, func(f *restructureProof) { f.Database = "b" }, func(f *restructureProof) { f.Started = now.Add(-time.Minute) }, func(f *restructureProof) { f.TrialID = "cached" }} {
		r.proof = good
		edit(&r.proof)
		if p.validateProof(r, "a", "db", "run") == nil {
			t.Fatal("accepted invalid evidence")
		}
	}
}

func fixtureHash(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}
func TestRestructureAdapterFixture(t *testing.T) {
	logDir := os.Getenv("PERFLAB_TEST_RESTRUCTURE_LOGS")
	if logDir == "" || os.Getenv("PERFLAB_TRIAL_ID") == "" {
		t.Skip("helper process for TestSQLIntegration/restructure_pair")
	}
	dbname := os.Getenv("PERFLAB_SQL_DATABASE")
	if dbname != "perflab_trial_a" && dbname != "perflab_trial_b" {
		t.Fatal("not a fixture database")
	}
	if os.Getenv("PERFLAB_TEST_RESTRUCTURE_FAIL") == dbname {
		os.Exit(9)
	}
	base, err := url.Parse(os.Getenv("PERFLAB_CI_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	q := base.Query()
	q.Set("database", dbname)
	base.RawQuery = q.Encode()
	db, err := sql.Open("sqlserver", base.String())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC()
	if _, err = db.Exec("ALTER TABLE dbo.perf_fixture ADD trial_updated int NULL"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	proof := restructureProof{Version: 1, Event: "restructure-completed", Server: os.Getenv("PERFLAB_SQL_SERVER"), Database: dbname, DatabaseGUID: os.Getenv("PERFLAB_DATABASE_GUID"),
		TrialID: os.Getenv("PERFLAB_TRIAL_ID"), BackupSetUUID: os.Getenv("PERFLAB_BACKUP_UUID"), PackageID: os.Getenv("PERFLAB_PACKAGE_ID"), PackageSHA256: os.Getenv("PERFLAB_PACKAGE_SHA256"),
		Operations: 1, Started: start, Finished: time.Now().UTC()}
	proof.EvidencePath = filepath.Join(logDir, proof.TrialID+".log")
	if err = os.WriteFile(proof.EvidencePath, []byte(dbname+" · fixture change completed "+proof.TrialID), 0600); err != nil {
		t.Fatal(err)
	}
	proof.EvidenceSHA256 = fixtureHash(t, proof.EvidencePath)
	if err = json.NewEncoder(os.Stdout).Encode(proof); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func testRestructurePairSQL(t *testing.T, ctx context.Context, master *sql.DB, stand Profile, endpoint *url.URL) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("PERFLAB_TEST_RESTRUCTURE_LOGS", tmp)
	backup := "/var/opt/mssql/data/perflab_trial_input.bak"
	if _, err := master.ExecContext(ctx, `BACKUP DATABASE perflab_integration TO DISK=@p1 WITH COPY_ONLY,CHECKSUM,INIT`, backup); err != nil {
		t.Fatal(err)
	}
	var set string
	if err := master.QueryRowContext(ctx, `SELECT TOP(1) CONVERT(varchar(36),b.backup_set_uuid) FROM msdb.dbo.backupset b JOIN msdb.dbo.backupmediafamily f ON f.media_set_id=b.media_set_id WHERE f.physical_device_name=@p1 ORDER BY b.backup_set_id DESC`, backup).Scan(&set); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pack := filepath.Join(tmp, "update.fixture")
	if err = os.WriteFile(pack, []byte("fixture schema update"), 0600); err != nil {
		t.Fatal(err)
	}
	p := RestructurePlan{Server: stand.ExpectedServer, BaselineDB: "perflab_trial_a", VariantDB: "perflab_trial_b", BackupSetUUID: set, PackageID: "fixture", PackagePath: pack, PackageSHA256: fixtureHash(t, pack),
		AdapterPath: exe, AdapterArgs: []string{"-test.run=^TestRestructureAdapterFixture$"}, AdapterSHA256: fixtureHash(t, exe), VariantMaxDOP: 2, TimeoutSeconds: 30}
	var dop, instance int
	if err = master.QueryRowContext(ctx, `SELECT (SELECT CONVERT(int,value) FROM perflab_integration.sys.database_scoped_configurations WHERE name='MAXDOP'),(SELECT CONVERT(int,value_in_use) FROM sys.configurations WHERE name='max degree of parallelism')`).Scan(&dop, &instance); err != nil {
		t.Fatal(err)
	}
	if effectiveDOP(dop, instance) == p.VariantMaxDOP {
		p.VariantMaxDOP = 1
	}
	open := func(pr Profile) *Live {
		u := *endpoint
		q := u.Query()
		q.Set("database", pr.Database)
		u.RawQuery = q.Encode()
		db, e := sql.Open("sqlserver", u.String())
		if e != nil {
			t.Fatal(e)
		}
		return &Live{P: pr, db: db}
	}
	restore := func() {
		t.Helper()
		for _, db := range []string{p.BaselineDB, p.VariantDB} {
			stmt := "RESTORE DATABASE " + quotedID(db) + " FROM DISK=@p1 WITH REPLACE, RECOVERY, MOVE N'perflab_integration' TO " + unicodeLiteral("/var/opt/mssql/data/"+db+".mdf") + ", MOVE N'perflab_integration_log' TO " + unicodeLiteral("/var/opt/mssql/data/"+db+".ldf")
			if _, e := master.ExecContext(ctx, stmt, backup); e != nil {
				t.Fatal(e)
			}
		}
	}
	restore()
	if _, err = runRestructure(ctx, stand, tmp, p, false, open); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERFLAB_TEST_RESTRUCTURE_FAIL", p.VariantDB)
	if _, err = runRestructure(ctx, stand, tmp, p, true, open); err == nil {
		t.Fatal("accepted failed second adapter")
	}
	checkRollback := func() {
		t.Helper()
		var v int
		if e := master.QueryRowContext(ctx, "SELECT CONVERT(int,value) FROM "+quotedID(p.VariantDB)+".sys.database_scoped_configurations WHERE name='MAXDOP'").Scan(&v); e != nil || v != dop {
			t.Fatalf("rollback: %d want %d; %v", v, dop, e)
		}
	}
	checkRollback()
	t.Setenv("PERFLAB_TEST_RESTRUCTURE_FAIL", "")
	restore()
	path, err := runRestructure(ctx, stand, tmp, p, true, open)
	if err != nil {
		t.Fatal(err)
	}
	checkRollback()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r Report
	if err = json.Unmarshal(b, &r); err != nil || r.Error != "" || len(r.Sections) != 4 {
		t.Fatalf("missing measurements %v %v", r.Error, err)
	}
	if _, err = runRestructure(ctx, stand, tmp, p, true, open); err == nil {
		t.Fatal("reused consumed copies")
	}
	for _, name := range []string{p.BaselineDB, p.VariantDB} {
		if _, err := master.ExecContext(ctx, fmt.Sprintf("DROP DATABASE %s", quotedID(name))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRestructureProcessFixture(t *testing.T) {
	if os.Getenv("PERFLAB_TEST_TRIAL_PROCESS") == "" {
		t.Skip("child process")
	}
	if os.Getenv("PERFLAB_TEST_TRIAL_PROCESS") == "wait" {
		time.Sleep(15 * time.Second)
	}
	os.Exit(17)
}
func TestRestructureProcessStartsAndCancels(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"exit", "wait"} {
		limit := 10 * time.Second
		if mode == "wait" {
			limit = 250 * time.Millisecond
		}
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		cmd := exec.CommandContext(ctx, exe, "-test.run=^TestRestructureProcessFixture$")
		cmd.Env = append(os.Environ(), "PERFLAB_TEST_TRIAL_PROCESS="+mode)
		start := time.Now()
		err := runUpdateProcess(cmd)
		cause := ctx.Err()
		cancel()
		if mode == "exit" {
			var ended *exec.ExitError
			if !errors.As(err, &ended) || ended.ExitCode() != 17 {
				t.Fatalf("child exit: %v", err)
			}
		} else if err == nil || !errors.Is(cause, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
			t.Fatalf("child timeout: %v, %v", err, cause)
		}
	}
}
