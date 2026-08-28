package postgresql

import (
	"context"
	"database/sql"
)

type pgQueryContext interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
}

func queryContextRows[T any](ctx context.Context, db pgQueryContext, query string, args []interface{}, scan func(*sql.Rows) (T, error)) (values []T, returnErr error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := rows.Close(); err != nil && returnErr == nil {
			returnErr = err
		}
	}()

	for rows.Next() {
		value, err := scan(rows)
		if err != nil {
			return values, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return values, err
	}
	return values, nil
}
