package pipeline

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/config"
	"wera/internal/sources"
	"wera/internal/store"
)

// WithAdded returns comps plus the boards found through jobs users added
// (store.WatchAddedBoard) that comps does not already list by name or
// board, so those are fetched every run too.
func WithAdded(ctx context.Context, pool *pgxpool.Pool, comps []config.Company) ([]config.Company, error) {
	added, err := store.AddedCompanies(ctx, pool)
	if err != nil {
		return nil, fmt.Errorf("load added boards: %w", err)
	}
	names := make(map[string]bool, len(comps))
	boards := make(map[string]bool, len(comps))
	for _, c := range comps {
		names[c.Name] = true
		boards[c.ATS+"/"+c.Token] = true
	}
	out := append([]config.Company(nil), comps...)
	for _, c := range added {
		if !names[c.Name] && !boards[c.ATS+"/"+c.Token] {
			out = append(out, c)
		}
	}
	return out, nil
}

// CheckBoard lists a board once and returns how many open jobs it has
// (an error when the board does not exist or the ATS is unknown).
func CheckBoard(ctx context.Context, registry map[string]sources.Source, ats, token string) (int, error) {
	src, err := sources.Get(registry, ats)
	if err != nil {
		return 0, err
	}
	var jobs []sources.RawJob
	if ds, ok := src.(sources.DetailSource); ok {
		jobs, err = ds.List(ctx, token)
		if errors.Is(err, sources.ErrPartialList) && len(jobs) > 0 {
			err = nil // the board exists; the count is a floor
		}
	} else {
		jobs, err = src.Fetch(ctx, token)
	}
	return len(jobs), err
}
