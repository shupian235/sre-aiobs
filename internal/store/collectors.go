package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"sre-aiobs/internal/collector"
)

const collectorSettingsID = "collector_settings"

type CollectorRun struct {
	ID         string     `json:"id"`
	Action     string     `json:"action"`
	Status     string     `json:"status"`
	HostCount  int        `json:"host_count"`
	Version    int64      `json:"version"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

func (s Store) CollectorSettings(ctx context.Context) (collector.Settings, int64, error) {
	var settings collector.Settings
	var ciphertext []byte
	var version int64
	err := s.DB.QueryRow(ctx, "SELECT ciphertext,version FROM encrypted_settings WHERE id=$1", collectorSettingsID).Scan(&ciphertext, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return settings, 0, nil
	}
	if err != nil {
		return settings, 0, err
	}
	data, err := unseal(s.SettingsCipher, collectorSettingsID, ciphertext)
	if err != nil {
		return settings, 0, err
	}
	err = json.Unmarshal(data, &settings)
	return settings, version, err
}

func (s Store) SaveCollectorSettings(ctx context.Context, settings collector.Settings, version int64) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	ciphertext, err := seal(s.SettingsCipher, collectorSettingsID, raw)
	if err != nil {
		return err
	}
	return s.saveEncryptedSettings(ctx, collectorSettingsID, ciphertext, version, "collector_settings_updated")
}

var ErrCollectorBusy = errors.New("collector run already active")

func (s Store) BeginCollectorRun(ctx context.Context, action string, version int64, count int) (string, error) {
	var id string
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		// Expire runs abandoned by a crashed controller after the six-minute command deadline.
		if _, err := tx.Exec(ctx, "UPDATE collector_runs SET status='failed',finished_at=now() WHERE status='running' AND started_at < now()-interval '8 minutes'"); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `INSERT INTO collector_runs(action,status,settings_version,host_count)
			VALUES($1,'running',$2,$3) ON CONFLICT DO NOTHING RETURNING id::text`, action, version, count).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCollectorBusy
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO audit_logs(action) VALUES($1)", "collector_"+action+"_requested")
		return err
	})
	return id, err
}

func (s Store) FinishCollectorRun(ctx context.Context, id, status string) error {
	_, err := s.DB.Exec(ctx, "UPDATE collector_runs SET status=$2,finished_at=now() WHERE id=$1::uuid AND status='running'", id, status)
	return err
}

func (s Store) CollectorRuns(ctx context.Context) ([]CollectorRun, error) {
	rows, err := s.DB.Query(ctx, `SELECT id::text,action, CASE WHEN status='running' AND started_at < now()-interval '8 minutes'
		THEN 'failed' ELSE status END,host_count,settings_version,started_at,finished_at
		FROM collector_runs ORDER BY started_at DESC LIMIT 30`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []CollectorRun{}
	for rows.Next() {
		var r CollectorRun
		if err := rows.Scan(&r.ID, &r.Action, &r.Status, &r.HostCount, &r.Version, &r.StartedAt, &r.FinishedAt); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}
