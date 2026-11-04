package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/resume"
)

// ResumeDoc is a stored resume.
type ResumeDoc struct {
	ID         int64             `json:"id"`
	Title      string            `json:"title"`
	JobID      *int64            `json:"job_id"`
	JobTitle   *string           `json:"job_title"`
	JobCompany *string           `json:"job_company"`
	Data       resume.Resume     `json:"data"`
	Layout     resume.Layout     `json:"layout"`
	Fit        *resume.FitResult `json:"fit"`
	Notes      []string          `json:"notes"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// ResumeSummary is a resume in a list.
type ResumeSummary struct {
	ID         int64     `json:"id"`
	Title      string    `json:"title"`
	JobID      *int64    `json:"job_id"`
	JobTitle   *string   `json:"job_title"`
	JobCompany *string   `json:"job_company"`
	OnePage    *bool     `json:"one_page"`
	UpdatedAt  time.Time `json:"updated_at"`
}

const resumeCols = `r.id, r.title, r.job_id, j.title, c.name, r.data, r.layout, r.fit, r.notes, r.updated_at`
const resumeFrom = ` FROM resumes r LEFT JOIN jobs j ON j.id = r.job_id LEFT JOIN companies c ON c.id = j.company_id `

func scanResume(row pgx.Row) (*ResumeDoc, error) {
	var d ResumeDoc
	var data, layout, fit, notes []byte
	if err := row.Scan(&d.ID, &d.Title, &d.JobID, &d.JobTitle, &d.JobCompany, &data, &layout, &fit, &notes, &d.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &d.Data); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(layout, &d.Layout)
	d.Layout = d.Layout.Clamp()
	if len(fit) > 0 {
		d.Fit = &resume.FitResult{}
		_ = json.Unmarshal(fit, d.Fit)
	}
	d.Notes = []string{}
	_ = json.Unmarshal(notes, &d.Notes)
	return &d, nil
}

// ListResumes lists the user's resumes: the base one first, then tailored
// copies, newest first.
func ListResumes(ctx context.Context, pool *pgxpool.Pool, userID int64) ([]ResumeSummary, error) {
	rows, err := pool.Query(ctx, `SELECT r.id, r.title, r.job_id, j.title, c.name, (r.fit->>'one_page')::boolean, r.updated_at`+
		resumeFrom+`WHERE r.user_id = $1 ORDER BY r.job_id IS NOT NULL, r.updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ResumeSummary{}
	for rows.Next() {
		var s ResumeSummary
		if err := rows.Scan(&s.ID, &s.Title, &s.JobID, &s.JobTitle, &s.JobCompany, &s.OnePage, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetResume returns one of the user's resumes (pgx.ErrNoRows if not theirs).
func GetResume(ctx context.Context, pool *pgxpool.Pool, userID, id int64) (*ResumeDoc, error) {
	return scanResume(pool.QueryRow(ctx, `SELECT `+resumeCols+resumeFrom+`WHERE r.user_id = $1 AND r.id = $2`, userID, id))
}

// BaseResume returns the user's base resume (pgx.ErrNoRows if none).
func BaseResume(ctx context.Context, pool *pgxpool.Pool, userID int64) (*ResumeDoc, error) {
	return scanResume(pool.QueryRow(ctx, `SELECT `+resumeCols+resumeFrom+`WHERE r.user_id = $1 AND r.job_id IS NULL`, userID))
}

// JobResume returns the user's resume tailored for a job (pgx.ErrNoRows if none).
func JobResume(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64) (*ResumeDoc, error) {
	return scanResume(pool.QueryRow(ctx, `SELECT `+resumeCols+resumeFrom+`WHERE r.user_id = $1 AND r.job_id = $2`, userID, jobID))
}

// ErrResumeExists is returned when creating a second base resume.
var ErrResumeExists = errors.New("you already have a resume")

// SaveResume inserts (doc.ID == 0) or updates one of the user's resumes;
// with jobID set and no id, it replaces the job's tailored copy.
func SaveResume(ctx context.Context, pool *pgxpool.Pool, userID int64, doc *ResumeDoc) (int64, error) {
	data, err := json.Marshal(doc.Data)
	if err != nil {
		return 0, err
	}
	layout, _ := json.Marshal(doc.Layout.Clamp())
	var fit []byte
	if doc.Fit != nil {
		fit, _ = json.Marshal(doc.Fit)
	}
	notes, _ := json.Marshal(doc.Notes)
	if doc.Notes == nil {
		notes = []byte("[]")
	}
	title := strings.TrimSpace(doc.Title)
	if title == "" {
		title = "Resume"
	}
	if doc.ID != 0 {
		tag, err := pool.Exec(ctx, `UPDATE resumes SET title = $3, data = $4, layout = $5, fit = $6, notes = $7, updated_at = now()
			WHERE user_id = $1 AND id = $2`, userID, doc.ID, title, data, layout, fit, notes)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() == 0 {
			return 0, pgx.ErrNoRows
		}
		return doc.ID, nil
	}
	var id int64
	if doc.JobID != nil {
		err = pool.QueryRow(ctx, `
			INSERT INTO resumes (user_id, job_id, title, data, layout, fit, notes) VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (user_id, job_id) WHERE job_id IS NOT NULL
			DO UPDATE SET title = EXCLUDED.title, data = EXCLUDED.data, layout = EXCLUDED.layout, fit = EXCLUDED.fit,
			              notes = EXCLUDED.notes, updated_at = now()
			RETURNING id`, userID, *doc.JobID, title, data, layout, fit, notes).Scan(&id)
		return id, err
	}
	err = pool.QueryRow(ctx, `
		INSERT INTO resumes (user_id, title, data, layout, fit, notes) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id) WHERE job_id IS NULL DO NOTHING RETURNING id`, userID, title, data, layout, fit, notes).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrResumeExists
	}
	return id, err
}

// DeleteResume removes one of the user's resumes.
func DeleteResume(ctx context.Context, pool *pgxpool.Pool, userID, id int64) error {
	tag, err := pool.Exec(ctx, `DELETE FROM resumes WHERE user_id = $1 AND id = $2`, userID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// JobKeywords is what a posting asks for: the skills extracted into its
// shared facts and the user's analysis, else common skills named in the
// description (free either way: no AI call here).
func JobKeywords(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64) ([]string, error) {
	var facts []byte
	var matched, missing []string
	var desc string
	err := pool.QueryRow(ctx, `
		SELECT f.facts->'skills_required', a.skills_matched, a.skills_missing, COALESCE(j.description, '')
		FROM user_jobs uj JOIN jobs j ON j.id = uj.job_id
		LEFT JOIN job_facts f ON f.job_id = j.id
		LEFT JOIN analyses a ON a.id = uj.analysis_id
		WHERE uj.user_id = $1 AND uj.job_id = $2`, userID, jobID).Scan(&facts, &matched, &missing, &desc)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	add := func(list []string) {
		for _, k := range list {
			k = strings.TrimSpace(k)
			if k != "" && len(k) <= 60 && !seen[strings.ToLower(k)] {
				seen[strings.ToLower(k)] = true
				out = append(out, k)
			}
		}
	}
	var required []string
	if len(facts) > 0 {
		_ = json.Unmarshal(facts, &required)
	}
	add(required)
	add(matched)
	add(missing)
	if len(out) == 0 {
		add(resume.KnownTerms(desc))
	}
	if len(out) > 30 {
		out = out[:30]
	}
	return out, nil
}

// ResumeImport is the state of a background import of the uploaded resume.
type ResumeImport struct {
	Status     string     `json:"status"` // running | done | failed
	Error      string     `json:"error"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// staleImport is how long a running import may take before another can
// start (the process running it may have stopped).
const staleImport = 10 * time.Minute

// StartResumeImport records a background import for the user and reports
// whether the caller should run it (false while another is running).
func StartResumeImport(ctx context.Context, pool *pgxpool.Pool, userID int64) (bool, error) {
	tag, err := pool.Exec(ctx, `
		INSERT INTO resume_imports (user_id, status) VALUES ($1, 'running')
		ON CONFLICT (user_id) DO UPDATE SET status = 'running', error = '', started_at = now(), finished_at = NULL
		WHERE resume_imports.status <> 'running' OR resume_imports.started_at < $2`,
		userID, time.Now().Add(-staleImport))
	return tag.RowsAffected() == 1, err
}

// FinishResumeImport records how a background import ended ("" for done).
func FinishResumeImport(ctx context.Context, pool *pgxpool.Pool, userID int64, failure string) error {
	status := "done"
	if failure != "" {
		status = "failed"
	}
	_, err := pool.Exec(ctx, `UPDATE resume_imports SET status = $2, error = $3, finished_at = now() WHERE user_id = $1`,
		userID, status, failure)
	return err
}

// GetResumeImport returns the user's latest background import, or nil.
func GetResumeImport(ctx context.Context, pool *pgxpool.Pool, userID int64) (*ResumeImport, error) {
	var ri ResumeImport
	err := pool.QueryRow(ctx, `SELECT status, error, started_at, finished_at FROM resume_imports WHERE user_id = $1`, userID).
		Scan(&ri.Status, &ri.Error, &ri.StartedAt, &ri.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ri.Status == "running" && time.Since(ri.StartedAt) > staleImport {
		ri.Status, ri.Error = "failed", "the import did not finish"
	}
	return &ri, nil
}

// JobFitInputs is what a job's fit score was computed from for a user:
// the score, the model's reply (its ratings) and the job's required skills.
// Score is nil and raw empty when the job has no score yet.
func JobFitInputs(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64) (score *int, raw string, required []string, err error) {
	var facts []byte
	err = pool.QueryRow(ctx, `
		SELECT a.fit_score, COALESCE(a.raw #>> '{}', ''), f.facts->'skills_required'
		FROM user_jobs uj
		LEFT JOIN analyses a ON a.id = uj.analysis_id
		LEFT JOIN job_facts f ON f.job_id = uj.job_id
		WHERE uj.user_id = $1 AND uj.job_id = $2`, userID, jobID).Scan(&score, &raw, &facts)
	if err == nil && len(facts) > 0 {
		_ = json.Unmarshal(facts, &required)
	}
	return score, raw, required, err
}
