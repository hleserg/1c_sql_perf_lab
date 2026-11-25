package lab

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMatchingAccessLog(t *testing.T) {
	for _, tc := range []struct {
		text, db, login string
		want            bool
	}{
		{"Login failed for user 'app'. Reason: Failed to open the explicitly specified database 'ERP'. [CLIENT: 10.0.0.3]", "ERP", "app", true},
		{"Login failed for user 'app'. Reason: Failed to open the database 'ERP' specified in the login properties. [CLIENT: x]", "ERP", "", true},
		{`Сбой входа для пользователя "ДОМЕН\Служба". Причина: не удалось открыть базу данных "Стенд". [КЛИЕНТ: x]`, "Стенд", "", true},
		{"Login failed for user 'ERP'. Reason: Failed to open the database 'other'.", "ERP", "", false},
		{"Login failed for user 'other'. Reason: Failed to open the database 'ERP'.", "ERP", "app", false},
		{"Login failed for user 'app'. Reason: Failed to open the database 'ERP_copy'.", "ERP", "app", false},
		{"Login failed for user 'app'. Reason: Password did not match.", "ERP", "app", false},
		{"SELECT 'Login failed for user x: database ERP'", "ERP", "", false},
	} {
		_, reason, ok := matchingLoginFailure(tc.text, tc.db, tc.login)
		if ok != tc.want || strings.Contains(reason, "[") {
			t.Errorf("%q: %v %q", tc.text, ok, reason)
		}
	}
}

func testAccessStatusSQL(t *testing.T, ctx context.Context, live *Live) {
	t.Helper()
	db, err := sql.Open("sqlserver", os.Getenv("PERFLAB_CI_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p := live.P
	var operator string
	if err = c.QueryRowContext(ctx, "SELECT ORIGINAL_LOGIN()").Scan(&operator); err != nil {
		t.Fatal(err)
	}
	report, err := readAccessStatus(ctx, c, p, operator)
	if err != nil || len(report.Sections) != 6 || !strings.Contains(fmt.Sprint(report.Sections[0].Rows), "Открытие оператором") {
		t.Fatalf("status failed: %v %#v", err, report.Sections)
	}
	for _, s := range report.Sections {
		if s.Error != "" {
			t.Fatalf("%s: %s", s.Title, s.Error)
		}
	}
	wrong := p
	wrong.ExpectedServer = "different-instance"
	if r, err := readAccessStatus(ctx, c, wrong, ""); err == nil || len(r.Sections) != 1 {
		t.Fatalf("expected identity mismatch, got %v", err)
	}
	p.Database = fmt.Sprintf("perflab_access_%d", time.Now().UnixNano())
	if r, err := readAccessStatus(ctx, c, p, ""); err != nil || !strings.Contains(fmt.Sprint(r.Sections[0].Rows), "отсутствует или скрыта") {
		t.Fatalf("missing database: %v %#v", err, r)
	}
	if _, err = c.ExecContext(ctx, "CREATE DATABASE "+quotedID(p.Database)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := c.ExecContext(ctx, "DROP DATABASE "+quotedID(p.Database)); err != nil {
			t.Error(err)
		}
	}()
	name := "perf]lab_access_" + fmt.Sprint(time.Now().UnixNano())
	var random [24]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	password := "aB0!" + hex.EncodeToString(random[:])
	if _, err = c.ExecContext(ctx, "CREATE LOGIN "+quotedID(name)+" WITH PASSWORD="+unicodeLiteral(password)); err != nil {
		t.Fatal("create disposable SQL login:", err)
	}
	defer func() {
		if _, err := c.ExecContext(ctx, "DROP LOGIN "+quotedID(name)); err != nil {
			t.Error(err)
		}
	}()
	if _, err = repairAccessOnConn(ctx, c, p, name+"_missing", ""); err == nil {
		t.Fatal("created missing server principal")
	}
	if _, err = repairAccessOnConn(ctx, c, p, name, "sysadmin"); err == nil {
		t.Fatal("accepted server role")
	}

	runDB := func(stmt string) {
		t.Helper()
		if _, err := c.ExecContext(ctx, "EXEC "+quotedID(p.Database)+".sys.sp_executesql "+unicodeLiteral(stmt)); err != nil {
			t.Fatal(err)
		}
	}
	runDB("CREATE USER " + quotedID(name) + " WITHOUT LOGIN")
	if _, err = repairAccessOnConn(ctx, c, p, name, ""); err == nil {
		t.Fatal("same-name user with a different SID was taken over")
	}
	runDB("DROP USER " + quotedID(name))
	for _, role := range []string{"", "", "db_owner", "db_owner"} {
		if _, err = repairAccessOnConn(ctx, c, p, name, role); err != nil {
			t.Fatal(role, err)
		}
		var access, serverRole, permission int
		query := `DECLARE @a int,@s int,@f int; EXECUTE AS LOGIN=@p1;
 SELECT @a=HAS_DBACCESS(@p2),@s=IS_SRVROLEMEMBER('sysadmin');
 EXEC ` + quotedID(p.Database) + `.sys.sp_executesql N'SELECT @x=HAS_PERMS_BY_NAME(DB_NAME(),''DATABASE'',''CREATE FUNCTION'')', N'@x int OUTPUT', @f OUTPUT;
 REVERT; SELECT @a,@s,@f`
		if err := c.QueryRowContext(ctx, query, name, p.Database).Scan(&access, &serverRole, &permission); err != nil {
			t.Fatal(err)
		}
		if access != 1 || serverRole != 0 || (permission == 1) != (role == "db_owner") {
			t.Fatalf("role %q: access %d, server role %d, CREATE FUNCTION %d", role, access, serverRole, permission)
		}
	}
	runDB("DENY CONNECT TO " + quotedID(name))
	if _, err = repairAccessOnConn(ctx, c, p, name, ""); err == nil {
		t.Fatal("removed explicit denial")
	}
}
