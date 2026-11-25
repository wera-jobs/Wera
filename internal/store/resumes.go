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
	// IsDefault: the base resume tailoring, the extension and autofill use
	// unless told otherwise (one per user).
	IsDefault bool `json:"is_default"`
	// BaseID is the base resume a tailored copy was made from (nil for
	// base resumes, and for copies made before there were several).
	BaseID *int64 `json:"base_id"`
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
	IsDefault  bool      `json:"is_default"`
	BaseID     *int64    `json:"base_id"`
}

const resumeCols = `r.id, r.title, r.job_id, j.title, c.name, r.data, r.layout, r.fit, r.notes, r.updated_at, r.is_default, r.base_id`
const resumeFrom = ` FROM resumes r LEFT JOIN jobs j ON j.id = r.job_id LEFT JOIN companies c ON c.id = j.company_id `

func scanResume(row pgx.Row) (*ResumeDoc, error) {
	var d ResumeDoc
	var data, layout, fit, notes []byte
	if err := row.Scan(&d.ID, &d.Title, &d.JobID, &d.JobTitle, &d.JobCompany, &data, &layout, &fit, &notes, &d.UpdatedAt, &d.IsDefault, &d.BaseID); err != nil {
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

// ListResumes lists the user's resumes: the default base first, the other
// bases, then tailored copies, newest first.
func ListResumes(ctx context.Context, pool *pgxpool.Pool, userID int64) ([]ResumeSummary, error) {
	rows, err := pool.Query(ctx, `SELECT r.id, r.title, r.job_id, j.title, c.name, (r.fit->>'one_page')::boolean, r.updated_at, r.is_default, r.base_id`+
		resumeFrom+`WHERE r.user_id = $1 ORDER BY r.is_default DESC, r.job_id IS NOT NULL, r.updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ResumeSummary{}
	for rows.Next() {
		var s ResumeSummary
		if err := rows.Scan(&s.ID, &s.Title, &s.JobID, &s.JobTitle, &s.JobCompany, &s.OnePage, &s.UpdatedAt, &s.IsDefault, &s.BaseID); err != nil {
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

// BaseResume returns the user's default base resume (pgx.ErrNoRows if none).
func BaseResume(ctx context.Context, pool *pgxpool.Pool, userID int64) (*ResumeDoc, error) {
	return scanResume(pool.QueryRow(ctx, `SELECT `+resumeCols+resumeFrom+`WHERE r.user_id = $1 AND r.is_default`, userID))
}

// BaseResumeByID returns one of the user's base resumes (pgx.ErrNoRows
// when it isn't theirs or is a tailored copy).
func BaseResumeByID(ctx context.Context, pool *pgxpool.Pool, userID, id int64) (*ResumeDoc, error) {
	return scanResume(pool.QueryRow(ctx, `SELECT `+resumeCols+resumeFrom+`WHERE r.user_id = $1 AND r.id = $2 AND r.job_id IS NULL`, userID, id))
}

// MaxBaseResumes bounds how many base resumes a user keeps.
const MaxBaseResumes = 10

// ErrTooManyResumes is returned when a user already has MaxBaseResumes.
var ErrTooManyResumes = errors.New("you have the most base resumes Wera keeps; delete one first")

// AddBaseResume stores a new base resume next to the user's others. The
// first one becomes the default.
func AddBaseResume(ctx context.Context, pool *pgxpool.Pool, userID int64, doc *ResumeDoc) (int64, error) {
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
	var id int64
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		// Two creates at once for one user wait for each other here.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('resumes'), $1::int)`, userID); err != nil {
			return err
		}
		var n int
		var hasDefault bool
		if err := tx.QueryRow(ctx, `SELECT count(*), bool_or(is_default) IS TRUE FROM resumes WHERE user_id = $1 AND job_id IS NULL`, userID).Scan(&n, &hasDefault); err != nil {
			return err
		}
		if n >= MaxBaseResumes {
			return ErrTooManyResumes
		}
		return tx.QueryRow(ctx, `
			INSERT INTO resumes (user_id, title, data, layout, fit, notes, is_default) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			userID, title, data, layout, fit, notes, !hasDefault).Scan(&id)
	})
	return id, err
}

// SetDefaultResume makes one of the user's base resumes the default
// (pgx.ErrNoRows when it isn't theirs or is a tailored copy).
func SetDefaultResume(ctx context.Context, pool *pgxpool.Pool, userID, id int64) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM resumes WHERE user_id = $1 AND id = $2 AND job_id IS NULL)`, userID, id).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return pgx.ErrNoRows
		}
		if _, err := tx.Exec(ctx, `UPDATE resumes SET is_default = false WHERE user_id = $1 AND is_default AND id <> $2`, userID, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE resumes SET is_default = true WHERE user_id = $1 AND id = $2`, userID, id)
		return err
	})
}

// RenameResume changes the title of one of the user's resumes.
func RenameResume(ctx context.Context, pool *pgxpool.Pool, userID, id int64, title string) error {
	tag, err := pool.Exec(ctx, `UPDATE resumes SET title = $3, updated_at = now() WHERE user_id = $1 AND id = $2`, userID, id, title)
	if err == nil && tag.RowsAffected() == 0 {
		err = pgx.ErrNoRows
	}
	return err
}

// JobResume returns the user's resume tailored for a job (pgx.ErrNoRows if none).
func JobResume(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64) (*ResumeDoc, error) {
	return scanResume(pool.QueryRow(ctx, `SELECT `+resumeCols+resumeFrom+`WHERE r.user_id = $1 AND r.job_id = $2`, userID, jobID))
}

// ErrResumeExists is returned when SaveResume would create a second
// default base resume (an import when the user has one).
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
			INSERT INTO resumes (user_id, job_id, title, data, layout, fit, notes, base_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (user_id, job_id) WHERE job_id IS NOT NULL
			DO UPDATE SET title = EXCLUDED.title, data = EXCLUDED.data, layout = EXCLUDED.layout, fit = EXCLUDED.fit,
			              notes = EXCLUDED.notes, base_id = EXCLUDED.base_id, updated_at = now()
			RETURNING id`, userID, *doc.JobID, title, data, layout, fit, notes, doc.BaseID).Scan(&id)
		return id, err
	}
	err = pool.QueryRow(ctx, `
		INSERT INTO resumes (user_id, title, data, layout, fit, notes, is_default) VALUES ($1, $2, $3, $4, $5, $6, true)
		ON CONFLICT (user_id) WHERE is_default DO NOTHING RETURNING id`, userID, title, data, layout, fit, notes).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrResumeExists
	}
	return id, err
}

// DeleteResume removes one of the user's resumes. When it was the default,
// the most recently edited of their other base resumes takes its place.
func DeleteResume(ctx context.Context, pool *pgxpool.Pool, userID, id int64) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var wasDefault bool
		err := tx.QueryRow(ctx, `DELETE FROM resumes WHERE user_id = $1 AND id = $2 RETURNING is_default`, userID, id).Scan(&wasDefault)
		if err != nil || !wasDefault {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE resumes SET is_default = true WHERE id = (
			  SELECT id FROM resumes WHERE user_id = $1 AND job_id IS NULL ORDER BY updated_at DESC, id DESC LIMIT 1)`, userID)
		return err
	})
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
		LEFT JOIN job_facts f ON f.job_id = j.id AND f.content_hash = j.content_hash
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
		FROM user_jobs uj JOIN jobs j ON j.id = uj.job_id
		LEFT JOIN analyses a ON a.id = uj.analysis_id
		LEFT JOIN job_facts f ON f.job_id = j.id AND f.content_hash = j.content_hash
		WHERE uj.user_id = $1 AND uj.job_id = $2`, userID, jobID).Scan(&score, &raw, &facts)
	if err == nil && len(facts) > 0 {
		_ = json.Unmarshal(facts, &required)
	}
	return score, raw, required, err
}
