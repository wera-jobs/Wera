package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/scoring"
)

// EmbedCandidate is a posting with current facts but no current embedding.
type EmbedCandidate struct {
	ID    int64
	Title string
	Facts scoring.Facts
}

// JobsToEmbed lists which of these jobs have facts for their current
// content but no embedding from this model for it.
func JobsToEmbed(ctx context.Context, pool *pgxpool.Pool, jobIDs []int64, model string) ([]EmbedCandidate, error) {
	if len(jobIDs) == 0 {
		return nil, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT j.id, j.title, f.facts
		FROM jobs j
		JOIN job_facts f ON f.job_id = j.id AND f.content_hash = j.content_hash
		LEFT JOIN job_embeddings e ON e.job_id = j.id
		WHERE j.id = ANY($1::bigint[])
		  AND (e.job_id IS NULL OR e.content_hash <> j.content_hash OR e.model <> $2)`, jobIDs, model)
	if err != nil {
		return nil, fmt.Errorf("find jobs to embed: %w", err)
	}
	defer rows.Close()
	var out []EmbedCandidate
	for rows.Next() {
		var c EmbedCandidate
		var raw []byte
		if err := rows.Scan(&c.ID, &c.Title, &raw); err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &c.Facts) == nil {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

// SaveJobEmbeddings stores embeddings for the jobs' current content.
func SaveJobEmbeddings(ctx context.Context, pool *pgxpool.Pool, model string, vecs map[int64][]float32) error {
	if len(vecs) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for id, v := range vecs {
		b.Queue(`
			INSERT INTO job_embeddings (job_id, content_hash, model, embedding)
			SELECT id, content_hash, $2, $3 FROM jobs WHERE id = $1
			ON CONFLICT (job_id) DO UPDATE
			SET content_hash = EXCLUDED.content_hash, model = EXCLUDED.model,
			    embedding = EXCLUDED.embedding, created_at = now()`, id, model, v)
	}
	if err := pool.SendBatch(ctx, b).Close(); err != nil {
		return fmt.Errorf("save job embeddings: %w", err)
	}
	return nil
}

// JobEmbeddings returns the current embeddings (this model, this content)
// of the given jobs.
func JobEmbeddings(ctx context.Context, pool *pgxpool.Pool, jobIDs []int64, model string) (map[int64][]float32, error) {
	out := map[int64][]float32{}
	if len(jobIDs) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT e.job_id, e.embedding FROM job_embeddings e JOIN jobs j ON j.id = e.job_id
		WHERE e.job_id = ANY($1::bigint[]) AND e.model = $2 AND e.content_hash = j.content_hash`, jobIDs, model)
	if err != nil {
		return nil, fmt.Errorf("load job embeddings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var v []float32
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out[id] = v
	}
	return out, rows.Err()
}

// ScoredVector is a job the AI scored for a user, with the job's embedding.
type ScoredVector struct {
	Score  float64
	Vector []float32
}

// ScoredVectors returns the user's newest AI score of each job among those
// stored under keys (one profile text may have scores from several scoring
// methods), with the job's embedding from this model: the limit most recent,
// newest first. Jobs without such an embedding are left out.
func ScoredVectors(ctx context.Context, pool *pgxpool.Pool, userID int64, keys []string, model string, limit int) ([]ScoredVector, error) {
	rows, err := pool.Query(ctx, `
		SELECT fit_score, embedding FROM (
			SELECT DISTINCT ON (a.job_id) a.fit_score, e.embedding, a.created_at
			FROM analyses a JOIN job_embeddings e ON e.job_id = a.job_id AND e.model = $3
			WHERE a.user_id = $1 AND a.kind = 'score' AND a.fit_score IS NOT NULL AND a.profile_hash = ANY($2::text[])
			ORDER BY a.job_id, a.created_at DESC
		) newest ORDER BY created_at DESC LIMIT $4`, userID, keys, model, limit)
	if err != nil {
		return nil, fmt.Errorf("load scored vectors: %w", err)
	}
	defer rows.Close()
	var out []ScoredVector
	for rows.Next() {
		var score int
		var v []float32
		if err := rows.Scan(&score, &v); err != nil {
			return nil, err
		}
		out = append(out, ScoredVector{Score: float64(score), Vector: v})
	}
	return out, rows.Err()
}

// ProfileEmbedding returns the stored embedding of this exact profile text
// from this model, or nil.
func ProfileEmbedding(ctx context.Context, pool *pgxpool.Pool, userID int64, profileHash, model string) ([]float32, error) {
	var v []float32
	err := pool.QueryRow(ctx, `
		SELECT embedding FROM profile_embeddings
		WHERE user_id = $1 AND profile_hash = $2 AND model = $3`, userID, profileHash, model).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

// SaveProfileEmbedding stores the embedding of the user's profile text.
func SaveProfileEmbedding(ctx context.Context, pool *pgxpool.Pool, userID int64, profileHash, model string, v []float32) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO profile_embeddings (user_id, profile_hash, model, embedding) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id) DO UPDATE
		SET profile_hash = EXCLUDED.profile_hash, model = EXCLUDED.model,
		    embedding = EXCLUDED.embedding, created_at = now()`, userID, profileHash, model, v)
	return err
}

// ScoreActivity is what the daily score limit needs to know about a user.
type ScoreActivity struct {
	ScoredLastDay   int        // AI scores in the last 24 hours
	ScoredProfile   bool       // any AI score exists for the current profile text
	LastSeen        *time.Time // last use of the dashboard or extension
	DigestRequested bool       // they get digests: on in their settings, address confirmed
}

// UserScoreActivity loads a user's recent scoring and activity.
func UserScoreActivity(ctx context.Context, pool *pgxpool.Pool, userID int64, profileHash string) (ScoreActivity, error) {
	var a ScoreActivity
	var confirmed *time.Time
	err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM analyses a
		        WHERE a.user_id = $1 AND a.kind = 'score' AND a.created_at > now() - interval '24 hours'
		          -- first scores only: scoring a job again (a new scoring method) is not a new job
		          AND NOT EXISTS (SELECT 1 FROM analyses b WHERE b.job_id = a.job_id AND b.kind = 'score'
		                          AND b.user_id = $1 AND b.created_at < a.created_at)),
		       EXISTS (SELECT 1 FROM analyses WHERE user_id = $1 AND kind = 'score' AND profile_hash = $2),
		       u.last_seen_at, u.email_verified_at
		FROM users u WHERE u.id = $1`, userID, profileHash).Scan(&a.ScoredLastDay, &a.ScoredProfile, &a.LastSeen, &confirmed)
	if err != nil {
		return a, fmt.Errorf("load score activity: %w", err)
	}
	s, err := GetSettings(ctx, pool, userID)
	if err != nil {
		return a, err
	}
	a.DigestRequested = s.Notifications.EmailDigest && confirmed != nil
	return a, nil
}

// TouchUser records that the user is using Wera, at most once an hour.
func TouchUser(ctx context.Context, pool *pgxpool.Pool, userID int64) error {
	_, err := pool.Exec(ctx, `
		UPDATE users SET last_seen_at = now()
		WHERE id = $1 AND (last_seen_at IS NULL OR last_seen_at < now() - interval '1 hour')`, userID)
	return err
}

// OpenJobsWithFacts lists open postings with facts for their current
// content (the ones that can be embedded).
func OpenJobsWithFacts(ctx context.Context, pool *pgxpool.Pool) ([]int64, error) {
	rows, err := pool.Query(ctx, `
		SELECT j.id FROM jobs j JOIN job_facts f ON f.job_id = j.id AND f.content_hash = j.content_hash
		WHERE j.closed_at IS NULL ORDER BY j.first_seen_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
