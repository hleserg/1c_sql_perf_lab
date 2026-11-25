package lab

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRasUsesListeningPortAndRespectsExplicitEndpoint(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "rac.exe")
	if err := os.WriteFile(exe, nil, 0600); err != nil {
		t.Fatal(err)
	}
	procs := []platformProcess{{"ras.exe", "", []int{2645, 2645}}, {"ragent.exe", "", []int{2640}}}
	c, err := rasTarget(Cluster{RAC: exe}, procs, "node", "node.local")
	if err != nil || c.Endpoint != "127.0.0.1:2645" {
		t.Fatal(c, err)
	}
	procs = append(procs, platformProcess{"ras.exe", "", []int{3645}})
	if _, err = rasTarget(Cluster{RAC: exe}, procs, "node", "node.local"); err == nil {
		t.Fatal("ambiguous endpoint")
	}
	c, err = rasTarget(Cluster{RAC: exe, Endpoint: "node:3645"}, procs, "node", "node.local")
	if err != nil || c.Endpoint != "127.0.0.1:3645" {
		t.Fatal(c, err)
	}
}
func TestAgentRegistrationRequiresExactLocalSQL(t *testing.T) {
	id := sqlIdentity{host: "node", instance: "test", db: "fixture"}
	row := agentRegistration{"Филиал", "fixture", `node\test`, "MSSQLServer", 2541}
	r, e := selectAgentRegistration([]agentRegistration{row}, Profile{}, id)
	if e != nil || r.name != "Филиал" || r.endpoint != "127.0.0.1:2541" || r.cluster.ServerID != "" {
		t.Fatal(r, e)
	}
	for _, rows := range [][]agentRegistration{{row, row}, {{"Филиал", "fixture", `other\test`, "MSSQLServer", 2541}}, {{"Филиал", "another", `node\test`, "MSSQLServer", 2541}}} {
		if _, e = selectAgentRegistration(rows, Profile{}, id); e == nil {
			t.Fatal(rows)
		}
	}
	if _, e = selectAgentRegistration([]agentRegistration{row}, Profile{ERPServer: "localhost:3541"}, id); e == nil {
		t.Fatal("wrong manager")
	}
}
func TestERPRequiredStopsBeforeChangesAndPersistsCause(t *testing.T) {
	for _, configured := range []bool{true, false} {
		r, b := newSuiteRun(t)
		r.RequireERP = true
		r.ERPDiagnostics = []string{"RAS на 127.0.0.1:1545 недоступен"}
		if configured {
			r.Profile.Workload = []string{"fixture"}
			b.erpError = true
		}
		rep, _, err := r.Execute(context.Background(), nil)
		if err == nil || rep.SuiteComplete || b.sqlSamples != 0 || len(b.touched) > 0 || strings.Join(b.jobs, ",") != "inspect" {
			t.Fatal(configured, rep.Workload, b.jobs, err)
		}
		if !strings.Contains(strings.Join(rep.Journal, " "), r.ERPDiagnostics[0]) {
			t.Fatal(rep.Journal)
		}
	}
}
func TestPlatformErrorNeverEchoesConnectionString(t *testing.T) {
	msg := platformFailure([]byte("Could not connect: 10061; Pwd=private"))
	if strings.Contains(msg, "private") || !strings.Contains(msg, "10061") {
		t.Fatal(msg)
	}
}
