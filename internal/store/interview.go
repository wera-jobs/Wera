package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Question is a multiple-choice interview question as served to a user:
// the answer and explanation are revealed only after they choose.
type Question struct {
	ID         int64    `json:"id"`
	Domain     string   `json:"domain"`
	Kind       string   `json:"kind"`
	Difficulty string   `json:"difficulty"`
	Question   string   `json:"question"`
	Choices    []string `json:"choices"`
	Tags       []string `json:"tags"`
}

// Answer is the result of answering one question.
type Answer struct {
	Correct     bool   `json:"correct"`
	Answer      int    `json:"answer"`
	Explanation string `json:"explanation"`
}

// DomainStat counts questions and the user's progress in one domain.
type DomainStat struct {
	Domain    string `json:"domain"`
	Kind      string `json:"kind"`
	Questions int64  `json:"questions"`
	Answered  int64  `json:"answered"`
	Correct   int64  `json:"correct"`
}

// InterviewDomains lists domains with question counts and the user's
// answered/correct counts (latest attempt per question).
func InterviewDomains(ctx context.Context, pool *pgxpool.Pool, userID int64) ([]DomainStat, error) {
	rows, err := pool.Query(ctx, `
		WITH last AS (
		  SELECT DISTINCT ON (question_id) question_id, correct
		  FROM interview_attempts WHERE user_id = $1
		  ORDER BY question_id, created_at DESC)
		SELECT q.domain, min(q.kind), count(*), count(l.question_id), count(*) FILTER (WHERE l.correct)
		FROM interview_questions q LEFT JOIN last l ON l.question_id = q.id
		GROUP BY q.domain ORDER BY min(q.kind) DESC, q.domain`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DomainStat{}
	for rows.Next() {
		var d DomainStat
		if err := rows.Scan(&d.Domain, &d.Kind, &d.Questions, &d.Answered, &d.Correct); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Quiz says which questions a round draws from. Empty fields mean any.
type Quiz struct {
	Domain     string
	Domains    []string // a track's domains, when Domain is empty
	Difficulty string
	Stack      []string // tags a round prefers (go, react, ...)
	Missed     bool     // only questions last answered wrong
	Limit      int
}

// QuizQuestions picks up to q.Limit questions: unanswered (or last answered
// wrong) first, then those tagged with the user's stack, then random.
func QuizQuestions(ctx context.Context, pool *pgxpool.Pool, userID int64, q Quiz) ([]Question, error) {
	domains := q.Domains
	if q.Domain != "" {
		domains = []string{q.Domain}
	}
	rows, err := pool.Query(ctx, `
		WITH last AS (
		  SELECT DISTINCT ON (question_id) question_id, correct
		  FROM interview_attempts WHERE user_id = $1
		  ORDER BY question_id, created_at DESC)
		SELECT q.id, q.domain, q.kind, q.difficulty, q.question, q.choices, q.tags
		FROM interview_questions q LEFT JOIN last l ON l.question_id = q.id
		WHERE (cardinality($2::text[]) = 0 OR q.domain = ANY($2)) AND ($3 = '' OR q.difficulty = $3)
		  AND (NOT $5 OR l.correct = false)
		ORDER BY CASE WHEN l.question_id IS NULL THEN 0 WHEN NOT l.correct THEN 1 ELSE 2 END,
		  q.tags && $6::text[] DESC, random()
		LIMIT $4`, userID, nonNil(domains), q.Difficulty, normalizeLimit(q.Limit, 10, 25), q.Missed, nonNil(q.Stack))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Question{}
	for rows.Next() {
		var qu Question
		if err := rows.Scan(&qu.ID, &qu.Domain, &qu.Kind, &qu.Difficulty, &qu.Question, &qu.Choices, &qu.Tags); err != nil {
			return nil, err
		}
		out = append(out, qu)
	}
	return out, rows.Err()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// InterviewTags counts the stack tags of the questions in some domains (all when none).
func InterviewTags(ctx context.Context, pool *pgxpool.Pool, domains []string) (map[string]int, error) {
	rows, err := pool.Query(ctx, `
		SELECT t, count(*) FROM interview_questions, unnest(tags) t
		WHERE cardinality($1::text[]) = 0 OR domain = ANY($1)
		GROUP BY t`, nonNil(domains))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var t string
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, rows.Err()
}

// Practice sums up a user's practice: a streak of days in a row with any
// answer (today or yesterday counting as unbroken), and this week's answers.
type Practice struct {
	Streak  int    `json:"streak"`
	Week    int64  `json:"week"`
	Missed  int64  `json:"missed"`
	LastDay string `json:"last_day,omitempty"`
}

// InterviewPractice reads the streak and counts, with days in the given time zone.
func InterviewPractice(ctx context.Context, pool *pgxpool.Pool, userID int64, tz string) (Practice, error) {
	var p Practice
	var last *string
	err := pool.QueryRow(ctx, `
		WITH a AS (
		  SELECT created_at FROM interview_attempts WHERE user_id = $1
		  UNION ALL SELECT created_at FROM interview_scenario_answers WHERE user_id = $1),
		days AS (SELECT DISTINCT (created_at AT TIME ZONE $2)::date d FROM a),
		today AS (SELECT (now() AT TIME ZONE $2)::date t),
		runs AS (SELECT d, d - (row_number() OVER (ORDER BY d))::int g FROM days),
		latest AS (SELECT g, max(d) top, count(*) n FROM runs GROUP BY g ORDER BY max(d) DESC LIMIT 1),
		missed AS (
		  SELECT count(*) n FROM (
		    SELECT DISTINCT ON (question_id) correct FROM interview_attempts
		    WHERE user_id = $1 ORDER BY question_id, created_at DESC) l WHERE NOT correct)
		SELECT
		  COALESCE((SELECT n FROM latest WHERE top >= (SELECT t FROM today) - 1), 0),
		  (SELECT count(*) FROM a WHERE created_at > now() - interval '7 days'),
		  (SELECT n FROM missed),
		  (SELECT to_char(max(d), 'YYYY-MM-DD') FROM days)`, userID, tz).Scan(&p.Streak, &p.Week, &p.Missed, &last)
	if last != nil {
		p.LastDay = *last
	}
	return p, err
}

// ScenarioProgress is how far a user got in a simulation, on their latest run.
type ScenarioProgress struct {
	Answered int `json:"answered"`
	Points   int `json:"points"`
	// Best is the best finished run's points, or -1 when none finished.
	Best int `json:"best"`
	// Choices on the current run, by step (-1 not answered yet).
	Choices []int `json:"choices,omitempty"`
}

// ScenarioAnswer stores a reply.
func ScenarioAnswer(ctx context.Context, pool *pgxpool.Pool, userID int64, scenario string, step, choice, points int) error {
	_, err := pool.Exec(ctx, `INSERT INTO interview_scenario_answers (user_id, scenario, step, choice, points) VALUES ($1, $2, $3, $4, $5)`,
		userID, scenario, step, choice, points)
	return err
}

// ScenarioRuns reads a user's replies for every simulation and folds them
// into runs: a reply to step 0 starts a new run. steps maps a simulation
// to its number of steps, so a run is finished when its last step is in.
func ScenarioRuns(ctx context.Context, pool *pgxpool.Pool, userID int64, steps map[string]int) (map[string]ScenarioProgress, error) {
	rows, err := pool.Query(ctx, `SELECT scenario, step, choice, points FROM interview_scenario_answers WHERE user_id = $1 ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ScenarioProgress{}
	for rows.Next() {
		var sc string
		var step, choice, points int
		if err := rows.Scan(&sc, &step, &choice, &points); err != nil {
			return nil, err
		}
		n, ok := steps[sc]
		if !ok || step >= n {
			continue
		}
		p, seen := out[sc]
		if !seen {
			p.Best = -1
		}
		if step == 0 || p.Choices == nil {
			p.Choices = make([]int, n)
			for i := range p.Choices {
				p.Choices[i] = -1
			}
			p.Answered, p.Points = 0, 0
		}
		if p.Choices[step] < 0 {
			p.Answered++
			p.Points += points
		}
		p.Choices[step] = choice
		if p.Answered == n && p.Points > p.Best {
			p.Best = p.Points
		}
		out[sc] = p
	}
	return out, rows.Err()
}

// AnswerQuestion records the user's choice and reveals the answer.
func AnswerQuestion(ctx context.Context, pool *pgxpool.Pool, userID, questionID int64, choice int) (*Answer, error) {
	if choice < 0 || choice > 3 {
		return nil, fmt.Errorf("choice must be 0-3")
	}
	var a Answer
	err := pool.QueryRow(ctx, `
		WITH q AS (SELECT id, answer, explanation FROM interview_questions WHERE id = $2),
		ins AS (
		  INSERT INTO interview_attempts (user_id, question_id, choice, correct)
		  SELECT $1, q.id, $3, q.answer = $3 FROM q)
		SELECT answer = $3, answer, explanation FROM q`, userID, questionID, choice).Scan(&a.Correct, &a.Answer, &a.Explanation)
	if err != nil {
		return nil, err
	}
	return &a, nil
}
