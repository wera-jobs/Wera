// Command dispatch for `wera rescore` and `wera refilter`: maintenance
// tools that requeue jobs without new LLM cost for the refilter path.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/config"
	"wera/internal/filter"
	"wera/internal/pipeline"
	"wera/internal/store"
)

// userIDFor resolves --user (an email) to an id; "" means every user (0).
func userIDFor(ctx context.Context, pool *pgxpool.Pool, email string) (int64, error) {
	if email == "" {
		return 0, nil
	}
	u, err := store.UserByEmail(ctx, pool, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("no user with email %s", email)
	}
	if err != nil {
		return 0, err
	}
	return u.ID, nil
}

// runRescore implements `wera rescore --all [--user EMAIL]`: re-queues
// open scored jobs back to pending_score so the next run scores them
// against each user's current profile (old analyses are kept; jobs whose
// profile text is unchanged reuse their scores).
func runRescore(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("rescore", flag.ContinueOnError)
	all := fs.Bool("all", false, "requeue all open scored jobs (required)")
	email := fs.String("user", "", "only this user's jobs (email); default every user")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*all {
		return fmt.Errorf("rescore requires --all")
	}

	env, err := config.LoadEnv()
	if err != nil {
		return err
	}
	log := config.NewLogger(env)
	pool, err := store.Open(ctx, env.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	userID, err := userIDFor(ctx, pool, *email)
	if err != nil {
		return err
	}
	n, err := store.RequeueScored(ctx, pool, userID)
	if err != nil {
		return err
	}
	log.Info("requeued scored jobs for rescoring", "jobs", n)
	return nil
}

// runRefilter implements `wera refilter --all [--user EMAIL]`: it
// re-applies the roles.yaml rules with each user's preferences to every
// open job (no LLM cost). Jobs that pass land in pending_score and are
// scored on the next run, reusing existing scores where possible.
func runRefilter(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("refilter", flag.ContinueOnError)
	all := fs.Bool("all", false, "reapply rules to all open jobs (required)")
	email := fs.String("user", "", "only this user's jobs (email); default every user")
	rolesPath := fs.String("roles", config.DefaultRolesPath, "path to roles.yaml")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*all {
		return fmt.Errorf("refilter requires --all")
	}

	env, err := config.LoadEnv()
	if err != nil {
		return err
	}
	log := config.NewLogger(env)
	roles, err := config.LoadRoles(*rolesPath)
	if err != nil {
		return err
	}
	eng, err := filter.New(roles)
	if err != nil {
		return err
	}
	pool, err := store.Open(ctx, env.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	userID, err := userIDFor(ctx, pool, *email)
	if err != nil {
		return err
	}
	n, err := store.RequeueForRefilter(ctx, pool, userID)
	if err != nil {
		return err
	}
	profiles, err := userProfiles(ctx, pool, *email)
	if err != nil {
		return err
	}
	var excluded, pending int
	for _, prof := range profiles {
		st, err := pipeline.FilterUser(ctx, pool, eng, pipeline.NewEmbedder(env, log), prof, nil)
		if err != nil {
			return fmt.Errorf("marked %d users but refiltering failed: %w", n, err)
		}
		excluded += st.Excluded
		pending += st.Pending
	}
	log.Info("refiltered all open jobs", "marked", n, "users", len(profiles),
		"excluded", excluded, "pending_score", pending)
	return nil
}
