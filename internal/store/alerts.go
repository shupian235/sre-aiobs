package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"sre-aiobs/internal/platform"
)

const emailSettingsID = "alert_email_destination"

func (s Store) AlertSettings(ctx context.Context) (platform.AlertSettings, error) {
	settings := platform.DefaultAlertSettings()
	var ciphertext []byte
	err := s.DB.QueryRow(ctx, `SELECT payload,email_ciphertext FROM alert_settings WHERE singleton`).Scan(&settings, &ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return settings, nil
	}
	if err == nil && ciphertext != nil {
		var raw []byte
		raw, err = unseal(s.SettingsCipher, emailSettingsID, ciphertext)
		if err == nil {
			err = json.Unmarshal(raw, &settings.EmailDestination)
		}
	}
	return settings, err
}

func (s Store) SaveAlertSettings(ctx context.Context, settings platform.AlertSettings) error {
	if !settings.Valid() {
		return errors.New("invalid alert settings")
	}
	var ciphertext []byte
	if settings.EmailDestination != nil {
		var err error
		ciphertext, err = s.encryptEmail(settings.EmailDestination)
		if err != nil {
			return err
		}
		settings.EmailDestination = nil
	}
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO alert_settings (singleton,payload,email_ciphertext) VALUES(true,$1,$2)
   ON CONFLICT(singleton) DO UPDATE SET
   payload=CASE WHEN excluded.email_ciphertext IS NULL THEN alert_settings.payload || excluded.payload
     ELSE (alert_settings.payload || excluded.payload) - 'email_destination' END,
   email_ciphertext=COALESCE(excluded.email_ciphertext,alert_settings.email_ciphertext)`, settings, ciphertext); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO audit_logs(action) VALUES('alert_settings_updated')`)
		return err
	})
}

func (s Store) encryptEmail(destination *platform.EmailDestination) ([]byte, error) {
	raw, err := json.Marshal(destination)
	if err != nil {
		return nil, err
	}
	return seal(s.SettingsCipher, emailSettingsID, raw)
}

// EncryptLegacyEmail moves pre-upgrade recipients out of the plaintext JSON payload.
func (s Store) EncryptLegacyEmail(ctx context.Context) error {
	if s.SettingsCipher == nil {
		return nil
	}
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var settings platform.AlertSettings
		err := tx.QueryRow(ctx, `SELECT payload FROM alert_settings
WHERE singleton AND email_ciphertext IS NULL AND payload ? 'email_destination' FOR UPDATE`).Scan(&settings)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		ciphertext, err := s.encryptEmail(settings.EmailDestination)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE alert_settings SET payload=payload-'email_destination',email_ciphertext=$1 WHERE singleton`, ciphertext)
		return err
	})
}
