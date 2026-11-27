package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"sre-aiobs/internal/platform"
)

func (s Store) AlertSettings(ctx context.Context) (platform.AlertSettings, error) {
	settings := platform.DefaultAlertSettings()
	err := s.DB.QueryRow(ctx, `SELECT payload FROM alert_settings WHERE singleton`).Scan(&settings)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return settings, err
}

func (s Store) SaveAlertSettings(ctx context.Context, settings platform.AlertSettings) error {
	if !settings.Valid() {
		return errors.New("invalid alert settings")
	}
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO alert_settings (singleton,payload) VALUES(true,$1)
   ON CONFLICT(singleton) DO UPDATE SET payload=alert_settings.payload || excluded.payload`, settings); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO audit_logs(action) VALUES('alert_settings_updated')`)
		return err
	})
}
