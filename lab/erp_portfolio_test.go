package lab

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestCOMDiagnosticsWithPowerShellProgress(t *testing.T) {
	got := workloadFailure([]byte("#< CLIXML\r\nPERFLAB-COM|uuid|unclassified|0x80131501\r\n<Objs>private text</Objs>"))
	if !strings.Contains(got, "получение UUID") || strings.Contains(got, "private") {
		t.Fatal(got)
	}
}

// Opt in on a disposable ERP stand. This path only reads documents.
func TestERPPortfolioOnStand(t *testing.T) {
	if os.Getenv("PERFLAB_TEST_PORTFOLIO") != "1" {
		t.Skip("requires an ERP stand")
	}
	p := Example()
	p.ERPServer = os.Getenv("PERFLAB_ERP_SERVER")
	p.ERPDatabase = os.Getenv("PERFLAB_ERP_DATABASE")
	if p.ERPServer == "" || p.ERPDatabase == "" {
		t.Fatal("set both ERP coordinates")
	}
	prepared, err := PrepareERPWorkload(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	live := Live{P: prepared}
	first, err := live.MeasureSessions(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) < 6 {
		t.Fatalf("expected multiple ERP operations; got %v", SortedKeys(first))
	}
	second, err := live.MeasureSessions(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != len(first) {
		t.Fatal("operation set changed")
	}
	for name := range first {
		if second[name] <= 0 {
			t.Fatal(name)
		}
	}
}
