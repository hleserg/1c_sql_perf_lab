package lab

import (
	"context"
	"testing"
)

// Called with the disposable database in the SQL Server integration job.
func testMonitorSQL(t *testing.T, ctx context.Context, l *Live) {
	t.Helper()
	r, err := l.monitorReading(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.DatabaseGUID == "" || r.SQLStartedServerTime == "" || r.LogicalCPUs < 1 || r.SQLCPUTimeMS < 0 || r.DBReads < 0 || r.DBWrites < 0 || r.SQLMemoryMiB <= 0 {
		t.Fatalf("missing SQL counters: %+v", r)
	}
	other := Live{P: l.P, conn: l.conn}
	other.P.ExpectedServer = "different-from-the-test-instance"
	if r, err = other.monitorReading(ctx); err == nil || r != nil {
		t.Fatal("target identity mismatch was accepted")
	}
}
