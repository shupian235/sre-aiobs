package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"sre-aiobs/internal/config"
)

const runtimeSettingsID = "runtime_settings"

var ErrSettingsConflict = errors.New("settings were changed by another operator")

func (s Store) RuntimeSettings(ctx context.Context) (config.RuntimeSettings, int64, error) {
	var settings config.RuntimeSettings
	var ciphertext []byte
	var version int64
	err := s.DB.QueryRow(ctx, "SELECT ciphertext,version FROM encrypted_settings WHERE id=$1", runtimeSettingsID).Scan(&ciphertext, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return settings, 0, nil
	}
	if err != nil {
		return settings, 0, err
	}
	data, err := unseal(s.SettingsCipher, runtimeSettingsID, ciphertext)
	if err != nil {
		return settings, 0, err
	}
	err = json.Unmarshal(data, &settings)
	return settings, version, err
}

func (s Store) SaveRuntimeSettings(ctx context.Context, settings config.RuntimeSettings, version int64) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	ciphertext, err := seal(s.SettingsCipher, runtimeSettingsID, raw)
	if err != nil {
		return err
	}
	return s.saveEncryptedSettings(ctx, runtimeSettingsID, ciphertext, version, "runtime_settings_updated")
}

func (s Store) saveEncryptedSettings(ctx context.Context, id string, ciphertext []byte, version int64, action string) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		stmt := "UPDATE encrypted_settings SET ciphertext=$2,version=version+1 WHERE id=$1 AND version=$3"
		args := []any{id, ciphertext, version}
		if version == 0 {
			stmt = "INSERT INTO encrypted_settings(id,ciphertext) VALUES($1,$2) ON CONFLICT DO NOTHING"
			args = args[:2]
		}
		tag, err := tx.Exec(ctx, stmt, args...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrSettingsConflict
		}
		_, err = tx.Exec(ctx, "INSERT INTO audit_logs(action) VALUES($1)", action)
		return err
	})
}
