package lab

import (
	"context"
	"fmt"
)

func (l *Live) queryStoreMode(ctx context.Context) (int, error) {
	var desired, actual, maxMB int
	err := l.conn.QueryRowContext(ctx, `SELECT desired_state,actual_state,max_storage_size_mb FROM sys.database_query_store_options`).Scan(&desired, &actual, &maxMB)
	if err != nil {
		return 0, err
	}
	if desired != actual || actual < 0 || actual > 2 {
		return 0, fmt.Errorf("Query Store: запрошен режим %d, действует %d. Проверь причину в паспорте SQL", desired, actual)
	}
	if maxMB <= 0 {
		return 0, fmt.Errorf("задай лимит хранилища Query Store перед экспериментом")
	}
	return actual, nil
}

func (l *Live) setQueryStoreMode(ctx context.Context, mode int) error {
	var query string
	switch mode {
	case 0:
		query = "ALTER DATABASE CURRENT SET QUERY_STORE = OFF"
	case 1:
		query = "ALTER DATABASE CURRENT SET QUERY_STORE = ON (OPERATION_MODE = READ_ONLY)"
	case 2:
		query = "ALTER DATABASE CURRENT SET QUERY_STORE = ON (OPERATION_MODE = READ_WRITE)"
	default:
		return fmt.Errorf("неподдерживаемый режим Query Store %d", mode)
	}
	// The database's existing storage cap, retention and capture policy are preserved.
	_, err := l.conn.ExecContext(ctx, query)
	return err
}
