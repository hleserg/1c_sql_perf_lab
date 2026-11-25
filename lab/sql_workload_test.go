package lab

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSQLReadMixedIndexDirections(t *testing.T) {
	idx := sqlReadIndex{
		table: "[dbo].[table]", index: "[ordered]",
		keys: []sqlReadKey{{"a]b", true}, {"h", false}},
	}
	ops := idx.operations()
	for i, order := range []string{"ORDER BY src.[a]]b] DESC, src.[h] ASC", "ORDER BY src.[a]]b] ASC, src.[h] DESC"} {
		for _, part := range []string{"TOP (8192)", "CHECKSUM(src.[a]]b], src.[h])", "FROM [dbo].[table] AS src WITH (INDEX([ordered]))", order} {
			if !strings.Contains(ops[i].query, part) {
				t.Fatalf("missing %q in SQL %s", part, ops[i].query)
			}
		}
	}
}

func TestSQLReadWaitingHonorsDeadline(t *testing.T) {
	live := &Live{P: Example(), conn: &sql.Conn{}}
	w := &live.sqlReads
	w.once.Do(func() { w.gate = make(chan struct{}, 1) })
	w.gate <- struct{}{}
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if _, err := live.MeasureSQL(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting measurement: %v", err)
	}
}

// Invoked from TestSQLIntegration in CI with its single verified SQL session.
func testSQLReadWorkload(t *testing.T, ctx context.Context, live *Live) {
	t.Helper()
	if _, err := live.MeasureSQL(ctx); err == nil {
		t.Fatal("empty database produced SQL measurements")
	}
	run := func(q string) {
		t.Helper()
		if _, err := live.conn.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		run(`DROP TABLE IF EXISTS dbo.[read]]fixture], dbo.new_candidate`)
	})
	run(`CREATE TABLE dbo.[read]]fixture] (bucket int NOT NULL, [seq]]key] int NOT NULL,
		CONSTRAINT [ix]]range] PRIMARY KEY CLUSTERED(bucket DESC, [seq]]key] ASC));
		WITH src AS (SELECT TOP (20000) ROW_NUMBER() OVER(ORDER BY (SELECT NULL)) AS n
		FROM sys.all_objects AS a CROSS JOIN sys.all_objects AS b)
		INSERT INTO dbo.[read]]fixture] SELECT n%9,n FROM src`)

	first, err := live.MeasureSQL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("expected two operations: %v", first)
	}
	for name, ms := range first {
		if !strings.Contains(name, "SQL · [dbo].[read]]fixture] ([ix]]range])") || ms <= 0 {
			t.Fatalf("unexpected SQL measurement %q: %v", name, ms)
		}
	}
	for _, v := range live.sqlReads.reference {
		if v.rows != 8192 {
			t.Fatalf("unexpected sample size: %d", v.rows)
		}
	}
	run(`CREATE TABLE dbo.new_candidate(id int PRIMARY KEY);
		INSERT INTO dbo.new_candidate VALUES (1)`)
	// A newly eligible table must not enter the workload between phases.
	again, err := live.MeasureSQL(ctx)
	if err != nil || !reflect.DeepEqual(SortedKeys(first), SortedKeys(again)) {
		t.Fatalf("cached workload: %v %v", again, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = live.MeasureSQL(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled measurement: %v", err)
	}
	run(`DELETE FROM dbo.[read]]fixture] WHERE [seq]]key] > 100`)
	if values, err := live.MeasureSQL(ctx); err == nil || values != nil {
		t.Fatalf("accepted changed sample or substituted table: %v %v", values, err)
	}
}
