package lab

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func layoutNumber(n int64) *int64 { return &n }

func TestFileLayoutLimitsAndPreview(t *testing.T) {
	p := Example()
	target := FileTarget{Name: "logical'file", SizeMB: layoutNumber(16), GrowthMB: layoutNumber(2)}
	cfg := FileLayout{Server: p.ExpectedServer, Database: p.Database, Files: []FileTarget{target}}
	if e := cfg.validate(p, true, false); e != nil {
		t.Fatal(e)
	}
	cfg.Database = "tempdb"
	if cfg.validate(p, true, false) == nil || cfg.validate(p, false, false) != nil || cfg.validate(p, true, true) != nil {
		t.Fatal("tempdb isolation guard")
	}
	cfg.Database = "different"
	if cfg.validate(p, true, true) == nil {
		t.Fatal("accepted other database")
	}
	cfg.Database = p.Database
	file := layoutFile{Name: target.Name, ID: 1, Kind: "ROWS", Volume: "mount", FreeBytes: 1 << 30, Pages: 8 * 128, Growth: 10, Percent: true, Writable: true, MaxPages: -1}
	files := map[string]layoutFile{file.Name: file}
	changes, e := planFileChanges(cfg, files)
	if e != nil || len(changes) != 2 || changes[0].kind != "SIZE" || changes[1].kind != "FILEGROWTH" {
		t.Fatalf("changes=%+v err=%v", changes, e)
	}
	if q := changes[0].statement("db]name"); !strings.Contains(q, "[db]]name]") || !strings.Contains(q, "N'logical''file'") || !strings.Contains(q, "SIZE = 16MB") {
		t.Fatal(q)
	}
	cfg.Files[0].SizeMB = layoutNumber(2)
	if _, e = planFileChanges(cfg, files); e == nil {
		t.Fatal("allowed shrinking")
	}
	cfg.Files[0].SizeMB = layoutNumber(8)
	file.Growth = 2 * 128
	file.Percent = false
	files[file.Name] = file
	if changes, e = planFileChanges(cfg, files); e != nil || len(changes) != 0 {
		t.Fatal(changes, e)
	}
	cfg.Database = "tempdb"
	if _, e = planFileChanges(cfg, files); e == nil {
		t.Fatal("startup size mismatch ignored")
	}
	for _, bad := range []FileTarget{
		{Name: "a"}, {Name: "a", GrowthMB: layoutNumber(0)}, {Name: "a", SizeMB: layoutNumber(maxFileLayoutMB + 1)},
		{Name: "a", GrowthMB: layoutNumber(5), NewDataPath: "/a"}, {Name: "a", NewDataPath: "relative", Filegroup: "PRIMARY", SizeMB: layoutNumber(8), GrowthMB: layoutNumber(1)},
	} {
		cfg.Files = []FileTarget{bad}
		if cfg.validate(p, false, false) == nil {
			t.Fatal("accepted", bad)
		}
	}
	cfg.Files = []FileTarget{target, target}
	if cfg.validate(p, false, false) == nil {
		t.Fatal("duplicate file")
	}
	path := filepath.Join(t.TempDir(), "bad.json")
	if e = os.WriteFile(path, []byte(`{"Files":[{"MaxSizeMB":32}]}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = LoadFileLayout(path); e == nil {
		t.Fatal("ignored unsupported option")
	}
}

func TestSQLIntegrationFileLayout(t *testing.T) {
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
	server := waitSQLIntegration(t, db)
	c, e := db.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	database := "perflab_layout_" + time.Now().UTC().Format("150405000000")
	if _, e = c.ExecContext(ctx, "CREATE DATABASE "+quotedID(database)); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if _, err := c.ExecContext(context.Background(), "USE master; DROP DATABASE "+quotedID(database)); err != nil {
			t.Error(err)
		}
	}()
	if _, e = c.ExecContext(ctx, "USE "+quotedID(database)); e != nil {
		t.Fatal(e)
	}

	files, e := layoutSnapshot(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	f := files[database]
	if f.ID != 1 {
		t.Fatal(files)
	}
	cfg := FileLayout{Server: server, Database: database, Files: []FileTarget{{Name: f.Name, SizeMB: layoutNumber(f.Pages/128 + 4), GrowthMB: layoutNumber(2)},
		{Name: "approved_data", NewDataPath: "/var/opt/mssql/data/" + database + "_added.ndf", Filegroup: "PRIMARY", SizeMB: layoutNumber(3), GrowthMB: layoutNumber(1)}}}
	p := Example()
	p.ExpectedServer = server
	p.Database = database
	if e = cfg.validate(p, true, false); e != nil {
		t.Fatal(e)
	}
	if _, e = c.ExecContext(ctx, "ALTER DATABASE "+quotedID(database)+" MODIFY FILE (NAME="+unicodeLiteral(f.Name)+",FILEGROWTH=13%)"); e != nil {
		t.Fatal(e)
	}
	var preview Report
	if e = operateLayout(ctx, c, cfg, false, &preview, func() error { return nil }, time.Millisecond); e != nil {
		t.Fatal(e)
	}
	files, e = layoutSnapshot(ctx, c)
	if e != nil || files[database].Pages != f.Pages || !files[database].Percent || files["approved_data"].ID != 0 {
		t.Fatal(files, e)
	}
	// Simulate a failed checkpoint after SQL verified the first property.
	var partial Report
	checks := 0
	injected := errors.New("checkpoint failure")
	e = operateLayout(ctx, c, cfg, true, &partial, func() error {
		checks++
		if checks == 3 {
			return injected
		}
		return nil
	}, time.Millisecond)
	if !errors.Is(e, injected) {
		t.Fatal("expected checkpoint failure", e)
	}
	files, e = layoutSnapshot(ctx, c)
	if e != nil || files[database].Pages != *cfg.Files[0].SizeMB*128 || !files[database].Percent || files["approved_data"].ID != 0 {
		t.Fatal(files, e)
	}
	for i := 0; i < 2; i++ {
		var rep Report
		if e = operateLayout(ctx, c, cfg, true, &rep, func() error { return nil }, time.Millisecond); e != nil {
			t.Fatal(e)
		}
		if i == 1 {
			for _, row := range rep.Sections[2].Rows {
				if row[2] != "Уже соответствует" {
					t.Fatal(row)
				}
			}
		}
	}
	files, e = layoutSnapshot(ctx, c)
	if e != nil || files[database].Percent || files[database].Growth != 256 || files["approved_data"].Pages != 384 || files["approved_data"].Growth != 128 {
		t.Fatal(files, e)
	}
	// Read the real tempdb startup template separately from its live size.
	if _, e = c.ExecContext(ctx, "USE tempdb"); e != nil {
		t.Fatal(e)
	}
	files, e = layoutSnapshot(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	temp := files["tempdev"]
	if temp.ID != 1 || temp.StartupPages <= 0 {
		t.Fatal(files)
	}
	cfg.Database = "tempdb"
	cfg.Files = []FileTarget{{Name: temp.Name, GrowthMB: layoutNumber(2)}}
	if cfg.validate(p, true, false) == nil {
		t.Fatal("shared tempdb accepted")
	}
	var previewTemp Report
	if e = operateLayout(ctx, c, cfg, false, &previewTemp, func() error { return nil }, time.Millisecond); e != nil {
		t.Fatal(e)
	}
	if previewTemp.Sections[0].ID != 17 {
		t.Fatal(previewTemp.Sections)
	}
}
