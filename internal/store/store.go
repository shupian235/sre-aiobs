package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"sre-aiobs/internal/platform"
)

type Store struct {
	DB    *pgxpool.Pool
	Redis *redis.Client
}

func Token() string {
	var data [16]byte
	_, err := rand.Read(data[:])
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(data[:])
}

func (s Store) Lock(ctx context.Context, key string, ttl time.Duration) (func(), bool, error) {
	token := Token()
	key = "sre-aiobs:lock:" + key
	ok, err := s.Redis.SetNX(ctx, key, token, ttl).Result()
	return func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.Redis.Eval(cleanup, `if redis.call('get',KEYS[1]) == ARGV[1] then return redis.call('del',KEYS[1]) end return 0`, []string{key}, token).Err()
	}, ok, err
}

func (s Store) Claim(ctx context.Context, j platform.Job) (platform.Job, int, bool, error) {
	var attempt int
	err := s.DB.QueryRow(ctx, `INSERT INTO runs(id,correlation_id,source,kind,window_start,window_end,status)
 VALUES($1,$2,$3,$4,$5,$6,'running')
 ON CONFLICT(source,kind,window_end) DO UPDATE SET status='running',attempts=runs.attempts+1,updated_at=now()
 WHERE runs.attempts<8 AND (
  (runs.status='failed' AND runs.updated_at + make_interval(secs => 15 * power(2,runs.attempts-1)) <= now())
  OR (runs.status='running' AND runs.updated_at <= now()-interval '2 minutes')
 )
 RETURNING id,correlation_id,attempts`, j.ID, j.CorrelationID, j.Source, j.Kind, j.Window.Start, j.Window.End).Scan(&j.ID, &j.CorrelationID, &attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return j, 0, false, nil
	}
	return j, attempt, err == nil, err
}

func (s Store) History(ctx context.Context, j platform.Job) ([]platform.Snapshot, error) {
	rows, err := s.DB.Query(ctx, `SELECT payload FROM snapshots JOIN runs ON runs.id=snapshots.run_id
 WHERE source=$1 AND kind=$2 AND window_end<$3 AND status='done' ORDER BY window_end DESC LIMIT 30`, j.Source, j.Kind, j.Window.End)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snapshots []platform.Snapshot
	for rows.Next() {
		var payload []byte
		var value platform.Snapshot
		if err = rows.Scan(&payload); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, value)
	}
	return snapshots, rows.Err()
}

func (s Store) Save(ctx context.Context, j platform.Job, report platform.Report) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		snapshot, err := json.Marshal(report.Snapshot)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO snapshots VALUES($1,$2) ON CONFLICT(run_id) DO UPDATE SET payload=excluded.payload`, j.ID, snapshot); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM risk_events WHERE run_id=$1`, j.ID); err != nil {
			return err
		}
		for i, event := range report.Events {
			if _, err = tx.Exec(ctx, `INSERT INTO risk_events VALUES($1,$2,$3)`, j.ID, i, event); err != nil {
				return err
			}
		}
		if report.Analysis != nil {
			if _, err = tx.Exec(ctx, `INSERT INTO ai_analyses VALUES($1,$2) ON CONFLICT(run_id) DO UPDATE SET payload=excluded.payload`, j.ID, report.Analysis); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_logs(run_id,action) VALUES($1,'report_saved')`, j.ID)
		return err
	})
}

func (s Store) Saved(ctx context.Context, id string) (platform.Snapshot, []platform.Event, *platform.Analysis, bool, error) {
	var snapshot platform.Snapshot
	var events []platform.Event
	var analysis *platform.Analysis
	err := s.DB.QueryRow(ctx, `SELECT s.payload, COALESCE((SELECT jsonb_agg(payload ORDER BY ordinal) FROM risk_events WHERE run_id=s.run_id),'[]'::jsonb),
 (SELECT payload FROM ai_analyses WHERE run_id=s.run_id) FROM snapshots s WHERE s.run_id=$1`, id).Scan(&snapshot, &events, &analysis)
	if errors.Is(err, pgx.ErrNoRows) {
		return snapshot, nil, nil, false, nil
	}
	return snapshot, events, analysis, err == nil, err
}

func (s Store) Finish(ctx context.Context, j platform.Job, attempt int, status string) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs SET status=$2,updated_at=now() WHERE id=$1 AND attempts=$3`, j.ID, status, attempt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("run ownership lost")
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_logs(run_id,action) VALUES($1,$2)`, j.ID, status)
		return err
	})
}

func (s Store) Notification(ctx context.Context, id, channel string, send func() error) error {
	var status string
	err := s.DB.QueryRow(ctx, `INSERT INTO notifications(run_id,channel,status) VALUES($1,$2,'sending')
 ON CONFLICT(run_id,channel) DO UPDATE SET status='sending',attempts=notifications.attempts+1,updated_at=now()
 WHERE notifications.status NOT IN ('sent','deduplicated') RETURNING status`, id, channel).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	sendErr := send()
	status = "sent"
	if errors.Is(sendErr, ErrDeduplicated) {
		status = "deduplicated"
		sendErr = nil
	} else if sendErr != nil {
		status = "failed"
	}
	_, err = s.DB.Exec(ctx, `UPDATE notifications SET status=$3,updated_at=now() WHERE run_id=$1 AND channel=$2`, id, channel, status)
	return errors.Join(err, sendErr)
}

var ErrDeduplicated = errors.New("notification deduplicated")

func (s Store) Pending(ctx context.Context, source, kind string, end time.Time) ([]time.Time, error) {
	rows, err := s.DB.Query(ctx, `SELECT ends FROM generate_series($3::timestamptz - $4::text::interval * 12, $3::timestamptz, $4::text::interval) ends
 LEFT JOIN runs ON source=$1 AND kind=$2 AND window_end=ends
 WHERE (id IS NULL OR (attempts<8 AND status<>'done'))
 AND ends >= COALESCE((SELECT min(window_end) FROM runs WHERE source=$1 AND kind=$2),$3)
 ORDER BY ends DESC`, source, kind, end, scheduleInterval(kind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var windows []time.Time
	for rows.Next() {
		var window time.Time
		if err = rows.Scan(&window); err != nil {
			return nil, err
		}
		windows = append(windows, window.UTC())
	}
	return windows, rows.Err()
}

func scheduleInterval(kind string) string {
	switch kind {
	case "minute":
		return "1 minute"
	case "five_minutes":
		return "5 minutes"
	case "hour":
		return "1 hour"
	default:
		return "24 hours"
	}
}
