package lab

import (
	"context"
	"errors"
	"fmt"
)

// PAGE_VERIFY has three states, including TORN_PAGE_DETECTION. The remaining
// bits represent AUTO_CLOSE and AUTO_SHRINK. Each journal entry changes one
// field only: a crash between ALTER commands is fully recoverable.
const (
	pageMask           = 3
	closeMask          = 4
	shrinkMask         = 8
	databaseOptionsSQL = `SELECT CONVERT(int,page_verify_option)+4*CONVERT(int,is_auto_close_on)+8*CONVERT(int,is_auto_shrink_on)
 FROM sys.databases WHERE database_id=DB_ID() AND database_id>4`
)

func validOptions(v int) bool { return v >= 0 && v <= 14 && v&pageMask <= 2 }
func optionPlan(ch Entry) ([]Entry, error) {
	if !validOptions(ch.Before) || !validOptions(ch.After) {
		return nil, errors.New("неподдерживаемые параметры базы №23")
	}
	var steps []Entry
	v := ch.Before
	for _, mask := range []int{pageMask, closeMask, shrinkMask} {
		after := v&^mask | ch.After&mask
		if after != v {
			steps = append(steps, Entry{ch.ID, v, after})
			v = after
		}
	}
	return steps, nil
}
func optionText(v int) string {
	if !validOptions(v) {
		return fmt.Sprint(v)
	}
	onOff := func(set bool) string {
		if set {
			return "ON"
		}
		return "OFF"
	}
	return fmt.Sprintf("PAGE_VERIFY=%s, AUTO_CLOSE=%s, AUTO_SHRINK=%s", []string{"NONE", "TORN_PAGE_DETECTION", "CHECKSUM"}[v&pageMask], onOff(v&closeMask != 0), onOff(v&shrinkMask != 0))
}
func (l *Live) readDBOptions(ctx context.Context) (int, error) {
	var v int
	err := l.conn.QueryRowContext(ctx, databaseOptionsSQL).Scan(&v)
	return v, err
}
func (l *Live) setDBOption(ctx context.Context, ch Entry, undo bool) error {
	from, to := ch.Before, ch.After
	if undo {
		from, to = to, from
	}
	steps, e := optionPlan(Entry{23, from, to})
	if e != nil {
		return e
	}
	if len(steps) != 1 {
		return errors.New("одна запись журнала должна менять один защитный параметр базы")
	}
	var command string
	diff := from ^ to
	switch {
	case diff&^pageMask == 0:
		command = "PAGE_VERIFY " + []string{"NONE", "TORN_PAGE_DETECTION", "CHECKSUM"}[to&pageMask]
	case diff == closeMask:
		command = "AUTO_CLOSE " + []string{"OFF", "ON"}[(to&closeMask)/closeMask]
	case diff == shrinkMask:
		command = "AUTO_SHRINK " + []string{"OFF", "ON"}[(to&shrinkMask)/shrinkMask]
	default:
		return errors.New("неоднозначная замена защитного параметра")
	}
	// Exact state in SQL immediately before issuing the individual ALTER. Rollback
	// may also encounter a value already restored by an earlier interrupted run.
	sql := `DECLARE @current int=(` + databaseOptionsSQL + `);
 IF @current=@p2 AND @p3=1 RETURN;
 IF @current IS NULL OR @current<>@p1 THROW 50003, 'Database options changed outside this trial', 1;
 ALTER DATABASE CURRENT SET ` + command + ` WITH NO_WAIT`
	if _, err := l.conn.ExecContext(ctx, sql, from, to, undo); err != nil {
		return err
	}
	v, err := l.readDBOptions(ctx)
	if err != nil {
		return err
	}
	if v != to {
		return fmt.Errorf("№23: проверка не прошла: %s", optionText(v))
	}
	return nil
}
