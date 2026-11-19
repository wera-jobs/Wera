package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxSavedSearches is how many searches one user may keep.
const MaxSavedSearches = 20

// ErrTooManySearches means the user already keeps MaxSavedSearches.
var ErrTooManySearches = errors.New("too many saved searches")

// SavedSearch is a named set of Jobs filters.
type SavedSearch struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Query     string    `json:"query"` // the Jobs page's query string, filter keys only
	CreatedAt time.Time `json:"created_at"`
}

// ListSearches returns the user's saved searches, oldest first.
func ListSearches(ctx context.Context, pool *pgxpool.Pool, userID int64) ([]SavedSearch, error) {
	rows, err := pool.Query(ctx, `SELECT id, name, query, created_at FROM saved_searches WHERE user_id = $1 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list searches: %w", err)
	}
	defer rows.Close()
	out := []SavedSearch{}
	for rows.Next() {
		var s SavedSearch
		if err := rows.Scan(&s.ID, &s.Name, &s.Query, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// AddSearch saves a search, unless the user already keeps the most allowed.
func AddSearch(ctx context.Context, pool *pgxpool.Pool, userID int64, name, query string) (SavedSearch, error) {
	s := SavedSearch{Name: name, Query: query}
	err := pool.QueryRow(ctx, `
		INSERT INTO saved_searches (user_id, name, query)
		SELECT $1, $2, $3 WHERE (SELECT count(*) FROM saved_searches WHERE user_id = $1) < $4
		RETURNING id, created_at`, userID, name, query, MaxSavedSearches).Scan(&s.ID, &s.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrTooManySearches
	}
	if err != nil {
		return s, fmt.Errorf("add search: %w", err)
	}
	return s, nil
}

// RenameSearch renames one of the user's searches; not theirs is pgx.ErrNoRows.
func RenameSearch(ctx context.Context, pool *pgxpool.Pool, userID, id int64, name string) error {
	tag, err := pool.Exec(ctx, `UPDATE saved_searches SET name = $3 WHERE id = $1 AND user_id = $2`, id, userID, name)
	if err != nil {
		return fmt.Errorf("rename search: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// DeleteSearch removes one of the user's searches; not theirs is pgx.ErrNoRows.
func DeleteSearch(ctx context.Context, pool *pgxpool.Pool, userID, id int64) error {
	tag, err := pool.Exec(ctx, `DELETE FROM saved_searches WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete search: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
