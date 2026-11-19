package main

import (
	"context"
	"flag"

	"wera/internal/config"
	"wera/internal/logos"
	"wera/internal/pipeline"
	"wera/internal/store"
)

// runLogos implements `wera logos`: one pass looking for the logo of every
// company due a look (the worker does the same daily).
func runLogos(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("logos", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	env, err := config.LoadEnv()
	if err != nil {
		return err
	}
	pool, err := store.Open(ctx, env.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	pipeline.FillLogos(ctx, pool, config.NewLogger(env), logos.New(env.UserAgent))
	return nil
}
