// Command dispatch for `wera pipeline` and `wera worker`.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"wera/internal/config"
	"wera/internal/filter"
	"wera/internal/notify"
	"wera/internal/pipeline"
	"wera/internal/store"
)

// buildPipeline loads config, YAML, the filter engine and the pool, and
// assembles the shared pipeline runner.
func buildPipeline(ctx context.Context) (*pipeline.Pipeline, error) {
	env, err := config.LoadEnv()
	if err != nil {
		return nil, err
	}
	log := config.NewLogger(env)

	comps, err := config.LoadCompanies(config.DefaultCompaniesPath)
	if err != nil {
		return nil, err
	}
	roles, err := config.LoadRoles(config.DefaultRolesPath)
	if err != nil {
		return nil, err
	}
	eng, err := filter.New(roles)
	if err != nil {
		return nil, err
	}
	pool, err := store.Open(ctx, env.DatabaseURL)
	if err != nil {
		return nil, err
	}

	return &pipeline.Pipeline{
		Pool:      pool,
		Env:       env,
		Log:       log,
		Companies: comps.Companies,
		Engine:    eng,
	}, nil
}

// runPipeline implements `wera pipeline`: one full fetch + filter + score
// run under the Postgres advisory lock. If another pipeline is running,
// this process logs and exits cleanly.
func runPipeline(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("pipeline", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	p, err := buildPipeline(ctx)
	if err != nil {
		return err
	}
	defer p.Pool.Close()

	ran, err := p.RunOnce(ctx)
	if err != nil {
		return err
	}
	if !ran {
		return nil // another run held the lock; already logged
	}
	return nil
}

// runWorker implements `wera worker`: it runs the pipeline at each
// RUN_SCHEDULE time (or every RUN_INTERVAL when no schedule is set) and
// shuts down gracefully on SIGINT/SIGTERM.
func runWorker(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	p, err := buildPipeline(ctx)
	if err != nil {
		return err
	}
	defer p.Pool.Close()

	// Digests are queued here; the API process delivers the outbox.
	mailer, err := notify.New(p.Pool, p.Env.Mail, p.Log)
	if err != nil {
		return fmt.Errorf("email: %w", err)
	}

	// Graceful shutdown: a signal cancels ctx; an in-flight run finishes
	// its current step quickly (every HTTP call honors ctx) and the run
	// row is closed even then.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	sched := p.Env.RunSchedule
	if sched != nil {
		p.Log.Info("worker started", "schedule", sched.String())
	} else {
		p.Log.Info("worker started", "interval", p.Env.RunInterval.String())
	}
	for {
		// With a schedule, wait for the next slot first (a restart does
		// not trigger an extra run); with an interval, run immediately.
		if sched != nil {
			next := sched.Next(time.Now())
			// Companies skipped for job board maintenance get a catch-up
			// run right after the window instead of at the next slot.
			if catchUp := p.CatchUpAt.Add(2 * time.Minute); !p.CatchUpAt.IsZero() && catchUp.Before(next) {
				next = catchUp
				p.Log.Info("catch-up run after job board maintenance", "at", next.Format(time.RFC3339))
			}
			p.CatchUpAt = time.Time{}
			p.Log.Info("next run scheduled", "at", next.Format(time.RFC3339))
			select {
			case <-ctx.Done():
				p.Log.Info("shutdown signal received; worker stopping")
				return nil
			case <-time.After(time.Until(next)):
			}
		}
		ran, err := p.RunOnce(ctx)
		if err != nil {
			// Log and keep looping: a failed run (e.g. one flaky board)
			// must not kill the worker.
			p.Log.Error("pipeline run failed", "err", err)
		} else if ran && mailer.Enabled() {
			if n, err := mailer.QueueDigests(ctx, time.Now().In(p.Env.RunZone)); err != nil {
				p.Log.Error("queueing digests failed", "err", err)
			} else if n > 0 {
				p.Log.Info("digests queued", "count", n)
			}
		}
		if sched != nil {
			continue
		}
		select {
		case <-ctx.Done():
			p.Log.Info("shutdown signal received; worker stopping")
			return nil
		case <-time.After(p.Env.RunInterval):
		}
	}
}
