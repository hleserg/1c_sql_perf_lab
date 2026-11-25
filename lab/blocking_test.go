package lab

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Reuse the real SQL Server XML projection without creating blocked processes,
// changing the instance's threshold or altering its Extended Events sessions.
func testBlockingSQL(t *testing.T, ctx context.Context, l *Live) {
	var id int
	if err := l.conn.QueryRowContext(ctx, "SELECT DB_ID()").Scan(&id); err != nil {
		t.Fatal(err)
	}
	input := fmt.Sprintf(`<RingBufferTarget><event name="xml_deadlock_report" timestamp="2026-01-02T12:00:00Z"><data><value><deadlock><process-list>
<process spid="73" currentdb="%d" ecid="0" lockMode="X" waittime="200"><inputbuf>private SQL text</inputbuf></process>
<process spid="74" currentdb="1"/></process-list></deadlock></value></data></event>
<event name="blocked_process_report" timestamp="2026-01-02T12:00:01Z"><data><value><blocked-process-report><blocked-process>
<process spid="75" currentdb="%d" waittime="6000"><inputbuf>private SQL text</inputbuf></process></blocked-process>
<blocking-process><process spid="76"/></blocking-process></blocked-process-report></value></data></event></RingBufferTarget>`, id, id)
	for _, q := range []struct{ name, sql, spid, blocker string }{
		{"deadlock", "DECLARE @history xml=CONVERT(xml,@p1); " + deadlockProjection, "73", "—"},
		{"blocked", "WITH targets AS (SELECT CONVERT(xml,@p1) AS doc) " + blockedProjection, "75", "76"},
	} {
		got := fetchSection(ctx, l.conn, check{22, q.name, q.sql}, input)
		if got.Error != "" || len(got.Rows) != 1 || len(got.Rows[0]) != 5 || got.Rows[0][2] != q.spid || got.Rows[0][3] != q.blocker || strings.Contains(strings.Join(got.Rows[0], " "), "private") {
			t.Fatalf("%s projection: %+v", q.name, got)
		}
	}
}

func TestRASOnlyMatchesStableCurrentEdges(t *testing.T) {
	before := Section{Rows: [][]string{{"t", "8", "53", "52", "x"}, {"t", "8", "59", "58", "x"}}}
	after := Section{Rows: [][]string{{"t", "8", "53", "52", "x"}, {"t", "8", "59", "61", "x"}}}
	got := stableBlockingSPIDs(before, after)
	if len(got) != 2 || got["52"] != "8" || got["53"] != "8" {
		t.Fatalf("SPIDs %v", got)
	}
}
