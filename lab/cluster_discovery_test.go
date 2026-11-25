package lab

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestRegisteredStandRequiresUniqueLocalSQLTarget(t *testing.T) {
	const cluster = "10000000-0000-0000-0000-000000000001"
	const base = "20000000-0000-0000-0000-000000000002"
	const other = "20000000-0000-0000-0000-000000000003"
	const worker = "30000000-0000-0000-0000-000000000003"
	id := sqlIdentity{host: "host", instance: "test", db: "fixture", port: "1422"}
	p := Profile{Cluster: Cluster{Connections: 0}}
	remote := false
	bases := []racRecord{{"infobase": base, "name": "Тестовая"}}
	otherDB := "another"
	reader := func(ctx context.Context, c Cluster, args ...string) ([]racRecord, error) {
		switch strings.Join(args[:2], " ") {
		case "cluster list":
			return []racRecord{{"cluster": cluster, "host": "host", "port": "1541"}}, nil
		case "infobase summary":
			return bases, nil
		case "infobase info":
			i := 0
			db := "fixture"
			dbhost := `localhost\test`
			if strings.HasSuffix(args[3], other) {
				i = 1
				db = otherDB
			}
			if remote {
				dbhost = `other\test`
			}
			return []racRecord{{"infobase": bases[i]["infobase"], "name": bases[i]["name"], "dbms": "MSSQLServer", "db-name": db, "db-server": dbhost}}, nil
		case "server list":
			return []racRecord{{"server": worker, "agent-host": "host"}}, nil
		default:
			return nil, fmt.Errorf("unexpected RAC args %v", args)
		}
	}
	find := func() (registration, error) { return findRegistration(context.Background(), p, id, p.Cluster, reader) }
	first, err := find()
	if err != nil || first.name != "Тестовая" || first.endpoint != "127.0.0.1:1541" || first.cluster.ServerID != worker || first.cluster.Connections != 0 {
		t.Fatalf("dedicated registration: %+v, %v", first, err)
	}
	bases = append(bases, racRecord{"infobase": other, "name": "Соседняя"})
	shared, err := find()
	if err != nil || shared.name != first.name || shared.cluster.ServerID != "" {
		t.Fatalf("shared worker selected: %+v %v", shared, err)
	}
	otherDB = id.db
	if _, err := find(); err == nil {
		t.Fatal("accepted ambiguous registration")
	}
	p.ERPDatabase = "Тестовая"
	if _, err := find(); err != nil {
		t.Fatal(err)
	}
	remote = true
	if _, err := find(); err == nil {
		t.Fatal("accepted same DB name on another instance")
	}
	remote = false
	p.ERPServer = "other:1541"
	if _, err := find(); err == nil {
		t.Fatal("accepted remote ERP host")
	}
}

func TestRACOutputCarriesOnlyNeededFields(t *testing.T) {
	r, err := parseRAC("session-id : 86\r\nuser-name : private\r\ninfobase : id\r\ndb-proc-info : \"56\"\r\napp-id : sensitive\r\n")
	want := []racRecord{{"session-id": "86", "infobase": "id", "db-proc-info": "56"}}
	if err != nil || !reflect.DeepEqual(r, want) {
		t.Fatalf("fields %v %v", r, err)
	}
	id := sqlIdentity{host: "stand", instance: "dev", port: "14330"}
	for _, s := range []string{`localhost:1500\dev`, `remote\dev`, `localhost\other`, `localhost\dev,9999`} {
		if matchesRegisteredSQL(s, id) {
			t.Fatalf("accepted %q", s)
		}
	}
}
