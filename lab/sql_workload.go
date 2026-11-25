package lab

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Select one ordered rowstore index per populated user table. Partitioned or
// filtered indexes, computed/encrypted keys and row filters could require
// extra reads or evaluate application code, so leave them out of this workload.
const sqlReadIndexes = `
WITH candidates AS (
	SELECT t.object_id, i.index_id, p.rows AS row_estimate,
		ROW_NUMBER() OVER (PARTITION BY t.object_id ORDER BY i.type, i.index_id) AS preference
	FROM sys.tables AS t
	JOIN sys.indexes AS i ON i.object_id=t.object_id
	JOIN sys.filegroups AS fg ON fg.data_space_id=i.data_space_id
	JOIN sys.partitions AS p ON p.object_id=i.object_id AND p.index_id=i.index_id
	WHERE t.is_ms_shipped=0 AND t.is_memory_optimized=0 AND p.rows>0
		AND i.type IN (1,2) AND i.is_disabled=0 AND i.is_hypothetical=0 AND i.has_filter=0
		AND HAS_PERMS_BY_NAME(QUOTENAME(SCHEMA_NAME(t.schema_id))+'.'+QUOTENAME(t.name), 'OBJECT', 'SELECT')=1
		AND NOT EXISTS (SELECT 1 FROM sys.security_predicates AS sp
			JOIN sys.security_policies AS pol ON pol.object_id=sp.object_id
			WHERE sp.target_object_id=t.object_id AND pol.is_enabled=1)
		AND NOT EXISTS (SELECT 1 FROM sys.index_columns AS ic
			JOIN sys.columns AS c ON c.object_id=ic.object_id AND c.column_id=ic.column_id
			WHERE ic.object_id=i.object_id AND ic.index_id=i.index_id AND ic.key_ordinal>0
				AND (c.is_computed=1 OR c.encryption_type IS NOT NULL OR c.system_type_id=240))
), picked AS (
	SELECT TOP (3) object_id, index_id, row_estimate FROM candidates
	WHERE preference=1 ORDER BY row_estimate DESC, object_id
)
SELECT t.object_id, SCHEMA_NAME(t.schema_id), t.name, i.name, c.name, ic.is_descending_key
FROM picked AS k
JOIN sys.tables AS t ON t.object_id=k.object_id
JOIN sys.indexes AS i ON i.object_id=k.object_id AND i.index_id=k.index_id
JOIN sys.index_columns AS ic ON ic.object_id=i.object_id AND ic.index_id=i.index_id
JOIN sys.columns AS c ON c.object_id=ic.object_id AND c.column_id=ic.column_id
WHERE ic.key_ordinal>0
ORDER BY k.row_estimate DESC, k.object_id, ic.key_ordinal`

type sqlReadKey struct {
	name string
	desc bool
}

type sqlReadIndex struct {
	table, index string // quoted SQL identifiers
	keys         []sqlReadKey
}

type sqlReadOperation struct {
	name, query string
}

func (idx sqlReadIndex) operations() []sqlReadOperation {
	cols, forward, backward := []string{}, []string{}, []string{}
	for _, k := range idx.keys {
		col := "src." + quotedID(k.name)
		a, b := " ASC", " DESC"
		if k.desc {
			a, b = b, a
		}
		cols = append(cols, col)
		forward, backward = append(forward, col+a), append(backward, col+b)
	}
	// The TOP and ordering belong to the inner query, before any grouping.
	// The forced index supplies the requested order in either direction. When
	// a nonunique key ties, CHECKSUM respects the column's comparison rules.
	sample := func(order []string) string {
		return fmt.Sprintf(`WITH sample AS (
	SELECT TOP (8192) CHECKSUM(%s) AS h
	FROM %s AS src WITH (INDEX(%s))
	ORDER BY %s
)
`, strings.Join(cols, ", "), idx.table, idx.index, strings.Join(order, ", "))
	}
	label := "SQL · " + idx.table + " (" + idx.index + ") · "
	return []sqlReadOperation{
		{label + "чтение начала индекса", sample(forward) + "SELECT COUNT_BIG(*), COALESCE(CHECKSUM_AGG(h),0) FROM sample"},
		{label + "группировка конца индекса", sample(backward) + `SELECT COALESCE(SUM(n),0), COALESCE(CHECKSUM_AGG(BINARY_CHECKSUM(bucket,n,digest)),0)
FROM (SELECT h & 255 AS bucket, COUNT_BIG(*) AS n, CHECKSUM_AGG(h) AS digest
	FROM sample GROUP BY h & 255) AS groups_of_keys`},
	}
}

func selectSQLReadOperations(ctx context.Context, conn *sql.Conn) ([]sqlReadOperation, error) {
	rows, err := conn.QueryContext(ctx, sqlReadIndexes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var indexes []sqlReadIndex
	var previousID int
	for rows.Next() {
		var id int
		var schema, table, index string
		var key sqlReadKey
		if err := rows.Scan(&id, &schema, &table, &index, &key.name, &key.desc); err != nil {
			return nil, err
		}
		if len(indexes) == 0 || id != previousID {
			indexes = append(indexes, sqlReadIndex{table: quotedID(schema) + "." + quotedID(table), index: quotedID(index)})
			previousID = id
		}
		idx := &indexes[len(indexes)-1]
		idx.keys = append(idx.keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(indexes) == 0 {
		return nil, errors.New("для SQL-замера нужна доступная непустая пользовательская таблица с обычным несекционированным индексом")
	}
	var operations []sqlReadOperation
	for _, idx := range indexes {
		operations = append(operations, idx.operations()...)
	}
	return operations, nil
}

type readSignature struct {
	rows, checksum int64
}

type sqlReadWorkload struct {
	once       sync.Once
	gate       chan struct{}
	operations []sqlReadOperation
	reference  []readSignature
}

// MeasureSQL times read-only statements on the session established by Identify.
// A run reuses the same SQL text and compares row counts and checksums with
// its first successful sample. Only elapsed milliseconds leave the backend.
func (l *Live) MeasureSQL(ctx context.Context) (map[string]float64, error) {
	if l.conn == nil {
		return nil, errors.New("для SQL-замера сначала подключись к базе стенда")
	}
	limit := l.P.Timeout()
	if limit <= 0 {
		limit = 2 * time.Minute
	}
	ctx, stop := context.WithTimeout(ctx, limit)
	defer stop()
	w := &l.sqlReads
	w.once.Do(func() { w.gate = make(chan struct{}, 1) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case w.gate <- struct{}{}:
		defer func() { <-w.gate }()
	}

	if len(w.operations) == 0 {
		operations, err := selectSQLReadOperations(ctx, l.conn)
		if err != nil {
			return nil, fmt.Errorf("выбор таблиц для SQL-замера: %w", err)
		}
		w.operations = operations
	}
	times := make(map[string]float64, len(w.operations))
	signatures := make([]readSignature, len(w.operations))
	for i, op := range w.operations {
		v := &signatures[i]
		start := time.Now()
		err := l.conn.QueryRowContext(ctx, op.query).Scan(&v.rows, &v.checksum)
		elapsed := time.Since(start)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", op.name, err)
		}
		if v.rows == 0 || (w.reference != nil && *v != w.reference[i]) {
			return nil, fmt.Errorf("%s: строки выборки изменились или отсутствуют, сравнение прервано", op.name)
		}
		times[op.name] = float64(elapsed) / float64(time.Millisecond)
	}
	if w.reference == nil {
		w.reference = signatures
	}
	return times, nil
}
