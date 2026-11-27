package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"sre-aiobs/internal/config"
)

func (s Store) SourceRoutes(ctx context.Context, name string) ([]config.Route, bool, error) {
	var routes []config.Route
	err := s.DB.QueryRow(ctx, "SELECT routes FROM source_route_settings WHERE source=$1", name).Scan(&routes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return routes, err == nil, err
}

// SaveSourceRoutes removes the override when routes is nil. An empty array explicitly disables mappings.
func (s Store) SaveSourceRoutes(ctx context.Context, name string, routes []config.Route) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		action := "source_routes_updated:"
		if routes == nil {
			_, err = tx.Exec(ctx, "DELETE FROM source_route_settings WHERE source=$1", name)
			action = "source_routes_reset:"
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO source_route_settings(source,routes) VALUES($1,$2)
ON CONFLICT(source) DO UPDATE SET routes=excluded.routes`, name, routes)
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO audit_logs(action) VALUES($1)", action+name)
		return err
	})
}
