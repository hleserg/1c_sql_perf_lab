package lab

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestERPNetworkSourceIsNominatedWorker(t *testing.T) {
	for _, s := range []string{"ERP01", "erp01.example."} {
		if !matchesERPProbeHost(s, "erp01", "erp01.example") {
			t.Fatalf("rejected physical host %s", s)
		}
	}
	for _, s := range []string{"", "localhost", ".", "erp01.other", "127.0.0.1", "erp02"} {
		if matchesERPProbeHost(s, "erp01", "erp01.example") {
			t.Fatalf("accepted unpinned source %q", s)
		}
	}
	if tcpChange("15", "21") != "6" || tcpChange("21", "1") != "—" || tcpChange("n/a", "5") != "—" {
		t.Fatal("host TCP counter delta is incorrect")
	}
}

func TestMeasuredTCPDialUsesActualPeer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	d := &measuredDialer{}
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	c, err := d.DialContext(ctx, "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	source, peer, elapsed := d.socket()
	if source != c.LocalAddr().String() || peer != listener.Addr().String() || elapsed <= 0 || elapsed >= 3*time.Second {
		t.Fatalf("unexpected socket %s → %s in %s", source, peer, elapsed)
	}
	c2, err := d.DialContext(ctx, "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if src, dst, _ := d.socket(); src != "" || dst != "" {
		t.Fatal("ambiguous connection labeled as a single endpoint")
	}
}

// Called by the SQL Server container integration test. Use that fixture's
// authenticated test-only endpoint. The production CLI uses the stand profile.
func testSQLNetworkEndpoint(t *testing.T, ctx context.Context, p Profile, endpoint string) {
	t.Helper()
	m, err := measureSQLSocket(ctx, p, endpoint, "")
	if err != nil {
		t.Fatal(err)
	}
	if m.databaseID == "" || m.peer == "" || m.source == "" || m.tcp <= 0 || m.read <= 0 {
		t.Errorf("missing network measurements: %#v", m)
	}
	_, e := measureSQLSocket(ctx, p, endpoint, strings.Repeat("0", 36))
	if !errors.Is(e, errNetworkIdentity) {
		t.Errorf("replaced database accepted: %v", e)
	}
}
