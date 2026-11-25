package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InterviewPrep is a user's stored prep for one job. Prep is the JSON
// written by profile.ParseInterviewPrep, kept as is.
type InterviewPrep struct {
	Prep      json.RawMessage `json:"prep"`
	Model     string          `json:"model"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// GetInterviewPrep returns the user's prep for a job, or nil when there is none.
func GetInterviewPrep(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64) (*InterviewPrep, error) {
	var p InterviewPrep
	err := pool.QueryRow(ctx, `
		SELECT prep, model, created_at, updated_at FROM interview_preps
		WHERE user_id = $1 AND job_id = $2`, userID, jobID).
		Scan(&p.Prep, &p.Model, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// SaveInterviewPrep stores (or replaces) the user's prep for a job.
func SaveInterviewPrep(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64, prep any, model string) (*InterviewPrep, error) {
	b, err := json.Marshal(prep)
	if err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO interview_preps (user_id, job_id, prep, model) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, job_id) DO UPDATE
		SET prep = EXCLUDED.prep, model = EXCLUDED.model, updated_at = now()`, userID, jobID, b, model); err != nil {
		return nil, err
	}
	return GetInterviewPrep(ctx, pool, userID, jobID)
}

// PracticeTurn is one answer in a practice thread and its feedback.
type PracticeTurn struct {
	Question string          `json:"question"` // the thread's question, or the follow-up answered
	Answer   string          `json:"answer"`
	Feedback json.RawMessage `json:"feedback"`
	At       time.Time       `json:"at"`
}

// PracticeThread is a user's practice of one interview question for one job.
type PracticeThread struct {
	Question  string         `json:"question"`
	Turns     []PracticeTurn `json:"turns"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// Practice limits: the turns a thread keeps (the latest), and the threads
// one job keeps (the most recently practiced).
const (
	MaxPracticeTurns   = 8
	MaxPracticeThreads = 30
)

// PracticeKey identifies a thread by its question, ignoring case and spacing.
func PracticeKey(question string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.Join(strings.Fields(question), " "))))
	return hex.EncodeToString(sum[:16])
}

// PracticeThreads returns the user's practice threads for a job, most
// recently practiced first.
func PracticeThreads(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64) ([]PracticeThread, error) {
	rows, err := pool.Query(ctx, `
		SELECT question, turns, updated_at FROM interview_practice
		WHERE user_id = $1 AND job_id = $2 ORDER BY updated_at DESC`, userID, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PracticeThread{}
	for rows.Next() {
		var t PracticeThread
		var raw []byte
		if err := rows.Scan(&t.Question, &raw, &t.UpdatedAt); err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &t.Turns) != nil || t.Turns == nil {
			t.Turns = []PracticeTurn{}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PracticeThreadFor returns the user's thread for one question (nil when none).
func PracticeThreadFor(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64, question string) (*PracticeThread, error) {
	var t PracticeThread
	var raw []byte
	err := pool.QueryRow(ctx, `
		SELECT question, turns, updated_at FROM interview_practice
		WHERE user_id = $1 AND job_id = $2 AND question_hash = $3`, userID, jobID, PracticeKey(question)).Scan(&t.Question, &raw, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(raw, &t.Turns) != nil || t.Turns == nil {
		t.Turns = []PracticeTurn{}
	}
	return &t, nil
}

// AddPracticeTurn adds a turn to the thread for question, or starts the
// thread over with it when fresh is set. The thread keeps its latest
// MaxPracticeTurns turns, the job its MaxPracticeThreads latest threads.
func AddPracticeTurn(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64, question string, turn PracticeTurn, fresh bool) (*PracticeThread, error) {
	key := PracticeKey(question)
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT turns FROM interview_practice WHERE user_id = $1 AND job_id = $2 AND question_hash = $3 FOR UPDATE`,
			userID, jobID, key).Scan(&raw)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var turns []PracticeTurn
		if !fresh && raw != nil {
			_ = json.Unmarshal(raw, &turns)
		}
		turns = append(turns, turn)
		if len(turns) > MaxPracticeTurns {
			turns = turns[len(turns)-MaxPracticeTurns:]
		}
		b, err := json.Marshal(turns)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO interview_practice (user_id, job_id, question_hash, question, turns) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (user_id, job_id, question_hash) DO UPDATE SET question = EXCLUDED.question, turns = EXCLUDED.turns, updated_at = now()`,
			userID, jobID, key, question, b); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			DELETE FROM interview_practice WHERE user_id = $1 AND job_id = $2 AND question_hash IN (
			  SELECT question_hash FROM interview_practice WHERE user_id = $1 AND job_id = $2
			  ORDER BY updated_at DESC OFFSET $3)`, userID, jobID, MaxPracticeThreads)
		return err
	})
	if err != nil {
		return nil, err
	}
	return PracticeThreadFor(ctx, pool, userID, jobID, question)
}
