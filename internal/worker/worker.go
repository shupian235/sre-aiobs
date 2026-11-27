package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"sre-aiobs/internal/config"
	"sre-aiobs/internal/detect"
	"sre-aiobs/internal/platform"
	"sre-aiobs/internal/source"
	"sre-aiobs/internal/store"
)

type Schedule struct {
	Kind   string
	Period time.Duration
}

var Schedules = []Schedule{{"minute", time.Minute}, {"five_minutes", 5 * time.Minute}, {"hour", time.Hour}, {"day", 24 * time.Hour}}

type Notifier interface {
	Telegram(context.Context, config.Config, platform.Report, platform.AlertSettings) error
	Slack(context.Context, platform.Report, platform.AlertSettings) error
	Email(context.Context, platform.EmailDestination, platform.Report) error
}

type Runner struct {
	Store    store.Store
	HTTP     *http.Client
	Notifier Notifier
	Analyzer platform.Analyzer
	AWS      interface {
		Inspect(context.Context, config.Source) (platform.Snapshot, []platform.Event, error)
	}

	Geo                        detect.Geo
	Runs, Failures, AIFailures atomic.Uint64
}

func (r *Runner) Run(parent context.Context, c config.Config, s config.Source, schedule Schedule, end time.Time) error {
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	j := platform.Job{ID: store.Token(), CorrelationID: store.Token(), Source: s.Name, Kind: schedule.Kind, Window: platform.Window{Start: end.Add(-schedule.Period), End: end}}
	release, ok, err := r.Store.Lock(ctx, s.Name+":"+j.Kind+":"+end.Format(time.RFC3339), 2*time.Minute)
	if err != nil || !ok {
		return err
	}
	defer release()
	j, attempt, claimed, err := r.Store.Claim(ctx, j)
	if err != nil || !claimed {
		return err
	}
	r.Runs.Add(1)
	err = r.process(ctx, c, s, j)
	status := "done"
	if err != nil {
		status = "failed"
		r.Failures.Add(1)
	}
	finish, cancelFinish := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancelFinish()
	finishErr := r.Store.Finish(finish, j, attempt, status)
	slog.Info("run finished", "run_id", j.ID, "correlation_id", j.CorrelationID, "source", j.Source, "kind", j.Kind, "status", status, "attempt", attempt)
	if err = errors.Join(err, finishErr); err != nil {
		return fmt.Errorf("run %s: %w", j.ID, err)
	}
	return nil
}

func (r *Runner) process(ctx context.Context, c config.Config, s config.Source, j platform.Job) error {
	snapshot, events, analysis, saved, err := r.Store.Saved(ctx, j.ID)
	if err != nil {
		return err
	}
	report := platform.Report{
		RunID: j.ID, AWSRegion: s.Region, Source: s.Name, Index: s.Index, Environment: s.Environment, Window: j.Window,
		VulnerabilityScan: s.Fields["vulnerability"] != "",
		NginxSecurity:     s.Fields["uri"] != "" || s.Fields["waf"] != "" || s.Fields["wafrule"] != "",
	}
	if !saved {
		if s.Kind == "aws" {
			if r.AWS == nil {
				return errors.New("AWS inspector unavailable")
			}
			snapshot, events, err = r.AWS.Inspect(ctx, s)
			if err != nil {
				return err
			}
		} else {
			snapshot, err = (source.Client{Config: s, HTTP: r.HTTP}).Aggregate(ctx, j.Window)
			if err != nil {
				return err
			}
			history, err := r.Store.History(ctx, j)
			if err != nil {
				return err
			}
			if s.Fields["uri"] != "" {
				routes, overridden, err := r.Store.SourceRoutes(ctx, s.Name)
				if err != nil {
					return err
				}
				if overridden {
					if err := config.ValidateRoutes(routes); err != nil {
						return fmt.Errorf("stored source route settings invalid: %w", err)
					}
					s.Routes = routes
				}
			}
			events = detect.Evaluate(snapshot, history, c.Rules, s.Hosts, s.Routes, r.Geo, s.AllowedMethods...)
		}
		if r.Analyzer != nil && j.Kind != "five_minutes" {
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) > 25*time.Second {
				aiCtx, cancel := context.WithTimeout(ctx, min(15*time.Second, time.Until(deadline)-20*time.Second))
				analysis, err = r.Analyzer.Analyze(aiCtx, events)
				cancel()
			}
			if err != nil {
				r.AIFailures.Add(1)
				slog.Warn("AI analysis unavailable", "run_id", j.ID, "correlation_id", j.CorrelationID)
			}
		}
		for i := range snapshot.Buckets {
			snapshot.Buckets[i].Value = detect.BucketKey(snapshot.Buckets[i].Value)
		}
		latency := make(map[string]map[string]float64, len(snapshot.Latency))
		for uri, quantiles := range snapshot.Latency {
			latency[detect.BucketKey(uri)] = quantiles
		}
		snapshot.Latency = latency
		report.Snapshot, report.Events, report.Analysis = snapshot, events, analysis
		if err = r.Store.Save(ctx, j, report); err != nil {
			return err
		}
	}
	report.Snapshot, report.Events, report.Analysis = snapshot, events, analysis
	var failures error
	telegram := j.Kind == "minute" && c.TelegramChat != ""
	slack := j.Kind == "minute" && os.Getenv("SLACK_WEBHOOK_URL") != ""
	email := j.Kind == "hour" || j.Kind == "day"
	if !telegram && !slack && !email {
		return failures
	}
	settings, err := r.Store.AlertSettings(ctx)
	if err != nil {
		return errors.Join(failures, err)
	}
	if telegram && settings.Telegram {
		failures = errors.Join(failures, r.Store.Notification(ctx, j.ID, "telegram", func() error { return r.Notifier.Telegram(ctx, c, report, settings) }))
	}
	if slack && settings.Slack {
		failures = errors.Join(failures, r.Store.Notification(ctx, j.ID, "slack", func() error { return r.Notifier.Slack(ctx, report, settings) }))
	}
	if email && settings.Email && settings.EmailDestination.Configured() {
		return errors.Join(failures, r.Store.Notification(ctx, j.ID, "ses", func() error { return r.Notifier.Email(ctx, *settings.EmailDestination, report) }))
	}
	return failures
}

func (r *Runner) Cycle(ctx context.Context, c config.Config) {
	now := time.Now().UTC().Add(-15 * time.Second)
	slots := make(chan struct{}, 8)
	var tasks sync.WaitGroup
	for _, s := range c.Sources {
		for _, schedule := range Schedules {
			if s.Kind == "aws" && schedule.Kind == "minute" {
				continue
			}
			tasks.Add(1)
			go func() {
				defer tasks.Done()
				select {
				case <-ctx.Done():
					return
				case slots <- struct{}{}:
				}
				defer func() { <-slots }()
				probe, cancel := context.WithTimeout(ctx, 5*time.Second)
				pending, err := r.Store.Pending(probe, s.Name, schedule.Kind, now.Truncate(schedule.Period))
				cancel()
				if err != nil {
					slog.Error("schedule lookup failed", "source", s.Name, "kind", schedule.Kind)
					return
				}
				// Process at most two windows per scheduling pass.
				for i, end := range pending {
					if i == 2 || ctx.Err() != nil {
						return
					}
					if err = r.Run(ctx, c, s, schedule, end); err != nil {
						slog.Error("scheduled run failed", "source", s.Name, "kind", schedule.Kind, "error", err)
					}
				}
			}()
		}
	}
	tasks.Wait()
}
