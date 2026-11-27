package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oschwald/maxminddb-golang"
	"github.com/redis/go-redis/v9"
	"sre-aiobs/internal/ai"
	"sre-aiobs/internal/config"
	"sre-aiobs/internal/dashboard"
	"sre-aiobs/internal/notify"
	"sre-aiobs/internal/store"
	"sre-aiobs/internal/worker"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	path := flag.String("config", "config.yaml", "YAML configuration file")
	migrate := flag.String("migrate", "", "apply SQL migration file or directory and exit")
	once := flag.Bool("once", false, "run the latest complete minute and exit")
	backfill := flag.String("backfill", "", "replay an aligned UTC window end (RFC3339)")
	kind := flag.String("kind", "minute", "backfill schedule")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL required")
	}
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return errors.New("invalid PostgreSQL configuration")
	}
	defer db.Close()
	if *migrate != "" {
		files, err := migrationFiles(*migrate)
		if err != nil {
			return err
		}
		tx, err := db.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(93829381)"); err != nil {
			return err
		}
		for _, file := range files {
			sql, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, string(sql)); err != nil {
				return fmt.Errorf("migration %s: %w", filepath.Base(file), err)
			}
		}
		return tx.Commit(ctx)
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	opts, err := redis.ParseURL(os.Getenv("REDIS_URL"))
	if err != nil {
		return errors.New("invalid REDIS_URL")
	}
	cache := redis.NewClient(opts)
	defer cache.Close()
	st := store.Store{DB: db, Redis: cache}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	aws, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return errors.New("failed to initialize AWS configuration")
	}
	r := &worker.Runner{Store: st, HTTP: client, Notifier: notify.Service{HTTP: client, Store: st, SES: sesv2.NewFromConfig(aws)}}
	var aiClient *ai.Client
	updateAI := func(c config.Config) {
		if c.AI.Provider == "" {
			r.Analyzer = nil
			aiClient = nil
			return
		}
		if aiClient == nil || aiClient.Config != c.AI {
			aiClient = &ai.Client{Config: c.AI, HTTP: client, Redis: cache}
		}
		r.Analyzer = aiClient
	}
	updateAI(c)
	if c.GeoDB != "" {
		geo, err := maxminddb.Open(c.GeoDB)
		if err != nil {
			return err
		}
		defer geo.Close()
		r.Geo = func(ip netip.Addr) (string, string) {
			var record struct {
				Country struct {
					Code string `maxminddb:"iso_code"`
				} `maxminddb:"country"`
				Subdivisions []struct {
					Code string `maxminddb:"iso_code"`
				} `maxminddb:"subdivisions"`
			}
			if err := geo.Lookup(ip.AsSlice(), &record); err != nil {
				return "", ""
			}
			region := ""
			if len(record.Subdivisions) > 0 {
				region = record.Subdivisions[0].Code
			}
			return record.Country.Code, region
		}
	}
	if *once || *backfill != "" {
		end := time.Now().UTC().Add(-15 * time.Second).Truncate(time.Minute)
		schedule := worker.Schedules[0]
		if *backfill != "" {
			end, err = time.Parse(time.RFC3339, *backfill)
			if err != nil {
				return err
			}
			found := false
			for _, entry := range worker.Schedules {
				if entry.Kind == *kind {
					schedule, found = entry, true
				}
			}
			if !found || !end.Equal(end.Truncate(schedule.Period)) || end.After(time.Now()) {
				return errors.New("invalid backfill window")
			}
		}
		for _, s := range c.Sources {
			err = errors.Join(err, r.Run(ctx, c, s, schedule, end))
		}
		return err
	}
	mux := http.NewServeMux()
	var activeConfig atomic.Pointer[config.Config]
	initial := c
	activeConfig.Store(&initial)
	access := dashboard.SourceAccess{
		Current: func() []config.Source { return activeConfig.Load().Sources },
		AlertDestinations: func() (bool, bool) {
			current := activeConfig.Load()
			return current.TelegramChat != "", current.EmailFrom != "" && len(current.EmailTo) > 0
		},
		HTTP: client,
	}
	dashboardPassword := os.Getenv("DASHBOARD_PASSWORD")
	login, err := dashboard.NewLogin(os.Getenv("DASHBOARD_TOTP_SECRET"), dashboardPassword, cache)
	if err != nil {
		return err
	}
	access.Login = login
	dashboard.Mount(mux, st, dashboardPassword, access)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		var migrated bool
		err := db.QueryRow(ctx, "SELECT to_regclass('runs') IS NOT NULL AND to_regclass('alert_settings') IS NOT NULL AND to_regclass('source_route_settings') IS NOT NULL").Scan(&migrated)
		if err != nil || !migrated || cache.Ping(ctx).Err() != nil {
			http.Error(w, "dependencies unavailable", 503)
			return
		}
		fmt.Fprintln(w, "ready")
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# TYPE sre_aiobs_runs_total counter\nsre_aiobs_runs_total %d\n# TYPE sre_aiobs_run_failures_total counter\nsre_aiobs_run_failures_total %d\n# TYPE sre_aiobs_ai_failures_total counter\nsre_aiobs_ai_failures_total %d\n", r.Runs.Load(), r.Failures.Load(), r.AIFailures.Load())
	})
	server := &http.Server{
		Addr:              c.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe() }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		if next, err := config.Load(*path); err == nil {
			next.Listen = c.Listen
			next.GeoDB = c.GeoDB
			activeConfig.Store(&next)
			c = next
			updateAI(c)
		} else {
			slog.Warn("configuration reload rejected")
		}
		r.Cycle(ctx, c)
		select {
		case err := <-serverErrors:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return errors.New(strings.TrimSpace(err.Error()))
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func migrationFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	files, err := filepath.Glob(filepath.Join(path, "*.sql"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("no SQL migrations found")
	}
	return files, nil
}
