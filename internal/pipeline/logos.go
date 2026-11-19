package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/logos"
	"wera/internal/store"
)

const (
	logoWorkers = 4                // boards read at once
	logoBatch   = 200              // companies per pass
	logoEvery   = 24 * time.Hour   // how often due logos are looked at again
	logoPerLook = 45 * time.Second // one company's page and image
)

// KeepLogos fills in company logos now and then once a day (each is looked
// at again after store.LogoRecheck), until ctx ends.
func KeepLogos(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, f *logos.Finder) {
	for {
		FillLogos(ctx, pool, log, f)
		select {
		case <-ctx.Done():
			return
		case <-time.After(logoEvery):
		}
	}
}

// FillLogos looks for the logo of every company due a look. A company
// whose board shows none is recorded as such; one whose look failed for a
// passing reason (a timeout, a 5xx) is tried again a day later.
func FillLogos(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, f *logos.Finder) {
	f.Reset()
	found, none, failed := 0, 0, 0
	for ctx.Err() == nil {
		before := found + none + failed
		due, err := store.LogoBacklog(ctx, pool, logoBatch)
		if err != nil {
			log.Warn("logo backlog failed", "err", err)
			return
		}
		if len(due) == 0 {
			break
		}
		var mu sync.Mutex
		var wg sync.WaitGroup
		work := make(chan store.LogoTarget)
		for range logoWorkers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for t := range work {
					res := lookForLogo(ctx, pool, f, t)
					mu.Lock()
					switch res {
					case logoFound:
						found++
					case logoNone:
						none++
					case logoFailed:
						failed++
					case logoUnsaved:
					}
					mu.Unlock()
				}
			}()
		}
		for _, t := range due {
			work <- t
		}
		close(work)
		wg.Wait()
		// Every look is recorded, so each batch moves on; one where none
		// could be recorded (the database is away) ends the pass.
		if found+none+failed == before {
			break
		}
		if len(due) < logoBatch {
			break
		}
	}
	if found+none+failed > 0 {
		log.Info("company logos looked for", "found", found, "none", none, "failed", failed)
	}
}

type logoResult int

const (
	logoFound logoResult = iota
	logoNone
	logoFailed  // recorded, retried after store.LogoRetry
	logoUnsaved // not even recorded
)

func lookForLogo(ctx context.Context, pool *pgxpool.Pool, f *logos.Finder, t store.LogoTarget) logoResult {
	ctx, cancel := context.WithTimeout(ctx, logoPerLook)
	defer cancel()
	if !logos.Supported(t.ATS) {
		return saveLogo(ctx, pool, t.CompanyID, logos.Logo{}, "")
	}
	src, err := f.Find(ctx, t.ATS, t.Token)
	if errors.Is(err, logos.ErrNone) {
		return saveLogo(ctx, pool, t.CompanyID, logos.Logo{}, "")
	}
	if err != nil {
		return failedLook(pool, t.CompanyID)
	}
	logo, err := f.Fetch(ctx, src)
	if err != nil {
		// The board named an image that isn't one Wera can use (broken,
		// tiny, too big): as good as none until the next look.
		if ctx.Err() != nil {
			return failedLook(pool, t.CompanyID)
		}
		return saveLogo(ctx, pool, t.CompanyID, logos.Logo{}, src)
	}
	return saveLogo(ctx, pool, t.CompanyID, logo, src)
}

// failedLook records a failed look with a fresh context: the look's own
// may be what ran out.
func failedLook(pool *pgxpool.Pool, id int64) logoResult {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := store.SaveLogoFailed(ctx, pool, id); err != nil {
		return logoUnsaved
	}
	return logoFailed
}

func saveLogo(ctx context.Context, pool *pgxpool.Pool, id int64, l logos.Logo, src string) logoResult {
	if err := store.SaveLogo(ctx, pool, id, l.ContentType, l.Data, src); err != nil {
		return logoUnsaved
	}
	if len(l.Data) == 0 {
		return logoNone
	}
	return logoFound
}
