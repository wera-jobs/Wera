package pipeline

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/store"
)

// noticeEvery is how often notices are made between runs, so a reminder
// lands within the hour before its day starts.
const noticeEvery = time.Hour

// MakeNotices creates the in-app notices due now (see
// store.MakeNotifications); failures are logged, never fatal.
func MakeNotices(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) {
	n, err := store.MakeNotifications(ctx, pool, time.Now())
	if err != nil {
		log.Warn("making notifications failed", "err", err)
		return
	}
	if n > 0 {
		log.Info("notifications made", "count", n)
	}
}

// KeepNotices makes notices now and every noticeEvery until ctx ends.
func KeepNotices(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) {
	for {
		MakeNotices(ctx, pool, log)
		select {
		case <-ctx.Done():
			return
		case <-time.After(noticeEvery):
		}
	}
}
