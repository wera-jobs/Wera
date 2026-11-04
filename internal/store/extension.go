package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// APIToken is a connected browser extension (the token itself is never
// stored or returned again).
type APIToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// CreateAPIToken stores a token hash for the user.
func CreateAPIToken(ctx context.Context, pool *pgxpool.Pool, userID int64, tokenHash, name string) (*APIToken, error) {
	t := &APIToken{Name: name}
	err := pool.QueryRow(ctx, `
		INSERT INTO api_tokens (user_id, token_hash, name) VALUES ($1, $2, $3)
		RETURNING id, created_at`, userID, tokenHash, name).Scan(&t.ID, &t.CreatedAt)
	return t, err
}

// APITokenUser returns the token's user and records its use (at most once
// a minute, to keep lookups read-mostly). pgx.ErrNoRows when unknown.
func APITokenUser(ctx context.Context, pool *pgxpool.Pool, tokenHash string) (*User, error) {
	var id int64
	var last *time.Time
	if err := pool.QueryRow(ctx, `SELECT user_id, last_used_at FROM api_tokens WHERE token_hash = $1`,
		tokenHash).Scan(&id, &last); err != nil {
		return nil, err
	}
	if last == nil || time.Since(*last) > time.Minute {
		if _, err := pool.Exec(ctx, `UPDATE api_tokens SET last_used_at = now() WHERE token_hash = $1`, tokenHash); err != nil {
			return nil, err
		}
	}
	return scanUser(pool.QueryRow(ctx, `SELECT id, email, name, is_admin, created_at FROM users WHERE id = $1`, id))
}

// ListAPITokens lists the user's connected extensions, newest first.
func ListAPITokens(ctx context.Context, pool *pgxpool.Pool, userID int64) ([]APIToken, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, name, created_at, last_used_at FROM api_tokens
		WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAPIToken revokes one of the user's tokens (pgx.ErrNoRows if none).
func DeleteAPIToken(ctx context.Context, pool *pgxpool.Pool, userID, id int64) error {
	tag, err := pool.Exec(ctx, `DELETE FROM api_tokens WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// DeleteAPITokenByHash revokes the token making the request (sign out).
func DeleteAPITokenByHash(ctx context.Context, pool *pgxpool.Pool, tokenHash string) error {
	_, err := pool.Exec(ctx, `DELETE FROM api_tokens WHERE token_hash = $1`, tokenHash)
	return err
}

// Applicant holds what application forms ask beyond the profile. Every
// field is optional; the extension fills only what is known.
type Applicant struct {
	FirstName       string `json:"first_name"`
	MiddleName      string `json:"middle_name"`
	LastName        string `json:"last_name"`
	PreferredName   string `json:"preferred_name"`
	Email           string `json:"email"`
	Phone           string `json:"phone"`
	Address         string `json:"address"`
	Address2        string `json:"address_line2"`
	City            string `json:"city"`
	State           string `json:"state"`
	PostalCode      string `json:"postal_code"`
	Country         string `json:"country"`
	LinkedIn        string `json:"linkedin"`
	GitHub          string `json:"github"`
	Portfolio       string `json:"portfolio"`
	Twitter         string `json:"twitter"`
	OtherLink       string `json:"other_link"`
	CurrentCompany  string `json:"current_company"`
	CurrentTitle    string `json:"current_title"`
	YearsExperience string `json:"years_experience"`
	School          string `json:"school"`
	Degree          string `json:"degree"`
	Major           string `json:"major"`
	GraduationYear  string `json:"graduation_year"`
	GPA             string `json:"gpa"`
	// Work authorization answers, as forms phrase them: "yes" or "no".
	AuthorizedToWork  string `json:"authorized_to_work"`
	NeedsSponsorship  string `json:"needs_sponsorship"`
	WillingToRelocate string `json:"willing_to_relocate"`
	WillingToTravel   string `json:"willing_to_travel"`
	Over18            string `json:"over_18"`
	Citizenship       string `json:"citizenship"`
	VisaStatus        string `json:"visa_status"`
	Clearance         string `json:"security_clearance"`
	WorkPreference    string `json:"work_preference"`
	SalaryExpectation string `json:"salary_expectation"`
	StartDate         string `json:"start_date"`
	NoticePeriod      string `json:"notice_period"`
	Languages         string `json:"languages"`
	HowHeard          string `json:"how_heard"`
	// Voluntary self-identification: "" until the user answers (on a form
	// or in Application details), "decline" only when they choose it.
	Gender            string `json:"gender"`
	Pronouns          string `json:"pronouns"`
	Transgender       string `json:"transgender"`
	SexualOrientation string `json:"sexual_orientation"`
	Race              string `json:"race"`
	Hispanic          string `json:"hispanic"`
	Veteran           string `json:"veteran"`
	Disability        string `json:"disability"`
}

// yesNo are the answers stored as "yes" or "no".
func (a *Applicant) yesNo() map[string]string {
	return map[string]string{
		"authorized_to_work": a.AuthorizedToWork, "needs_sponsorship": a.NeedsSponsorship,
		"willing_to_relocate": a.WillingToRelocate, "willing_to_travel": a.WillingToTravel, "over_18": a.Over18,
	}
}

// workPreferences are the allowed work_preference values.
var workPreferences = map[string]bool{"": true, "remote": true, "hybrid": true, "onsite": true, "any": true}

// Validate bounds the lengths and the yes/no answers.
func (a *Applicant) Validate() error {
	raw, _ := json.Marshal(a)
	var m map[string]string
	_ = json.Unmarshal(raw, &m)
	for k, v := range m {
		if len(v) > 300 {
			return fmt.Errorf("%s is too long", k)
		}
	}
	for k, v := range a.yesNo() {
		if v != "" && v != "yes" && v != "no" {
			return fmt.Errorf("%s must be yes or no", k)
		}
	}
	if !workPreferences[a.WorkPreference] {
		return errors.New("work_preference must be remote, hybrid, onsite or any")
	}
	if v := a.Transgender; v != "" && v != "yes" && v != "no" && v != "decline" {
		return errors.New("transgender must be yes, no or decline")
	}
	return nil
}

// GetApplicant returns the saved details, or defaults from the account
// and profile (name, email, title, work authorization) when none are saved.
func GetApplicant(ctx context.Context, pool *pgxpool.Pool, user *User) (*Applicant, bool, error) {
	var raw []byte
	err := pool.QueryRow(ctx, `SELECT details FROM user_applicant WHERE user_id = $1`, user.ID).Scan(&raw)
	if err == nil {
		a := &Applicant{}
		if err := json.Unmarshal(raw, a); err != nil {
			return nil, false, err
		}
		return a, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	// Self-identification starts unanswered: the extension asks on the
	// first form and remembers the answer; it never declines for the user.
	a := &Applicant{Email: user.Email, HowHeard: "Company website", Over18: "yes"}
	if f := strings.Fields(user.Name); len(f) > 0 {
		a.FirstName, a.LastName = f[0], strings.Join(f[1:], " ")
	}
	var answers []byte
	if err := pool.QueryRow(ctx, `SELECT answers FROM profiles WHERE user_id = $1`, user.ID).Scan(&answers); err == nil {
		var ans struct {
			CurrentTitle      string `json:"current_title"`
			WorkAuthorization string `json:"work_authorization"`
		}
		_ = json.Unmarshal(answers, &ans)
		a.CurrentTitle = ans.CurrentTitle
		switch ans.WorkAuthorization {
		case "citizen_or_resident":
			a.AuthorizedToWork, a.NeedsSponsorship = "yes", "no"
		case "sponsorship_future":
			a.AuthorizedToWork, a.NeedsSponsorship = "yes", "yes"
		case "sponsorship_now":
			a.AuthorizedToWork, a.NeedsSponsorship = "no", "yes"
		}
	}
	return a, false, nil
}

// SaveApplicant stores the user's details.
func SaveApplicant(ctx context.Context, pool *pgxpool.Pool, userID int64, a *Applicant) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO user_applicant (user_id, details) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET details = EXCLUDED.details, updated_at = now()`, userID, raw)
	return err
}

// ManualJob is a job a user adds from a web page.
type ManualJob struct {
	URL         string
	Company     string
	Title       string
	Location    string
	Remote      *bool
	Department  string
	Description string
	Industry    string
	PostedAt    *time.Time // when the page says the posting went up; nil when it does not
}

// NormalizeJobURL drops the fragment, tracking parameters, a trailing
// slash and the case of the host, so the same posting matches itself.
func NormalizeJobURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	q := u.Query()
	for k := range q {
		if strings.HasPrefix(k, "utm_") || k == "ref" || k == "source" || k == "src" || k == "gh_src" || k == "lever-source" {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u.String()
}

// AddManualJob creates (or finds) the job for this URL, links it to the
// user as a pending match saved in their tracker, and returns its id and
// whether the user did not have it before. A posting someone else already
// added is shared, not duplicated. The company is matched by name to a
// watched company when one exists; otherwise a "manual" company is created.
func AddManualJob(ctx context.Context, pool *pgxpool.Pool, userID int64, in ManualJob) (int64, bool, error) {
	in.URL = NormalizeJobURL(in.URL)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx)
	if id, err := LookupJobByURL(ctx, pool, userID, in.URL); err == nil {
		changed, err := linkAddedJob(ctx, tx, userID, id)
		if err != nil {
			return 0, false, err
		}
		// Added again: the user has just seen it up, so its lifetime restarts.
		if _, err := tx.Exec(ctx, `
			UPDATE jobs SET closed_at = NULL, last_seen_at = now()
			WHERE id = $1 AND source = 'manual'`, id); err != nil {
			return 0, false, fmt.Errorf("reopen job: %w", err)
		}
		return id, changed, tx.Commit(ctx)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}

	// Someone else added this posting already: share it.
	var shared int64
	err = tx.QueryRow(ctx, `
		SELECT id FROM jobs WHERE rtrim(url, '/') = rtrim($1, '/') AND closed_at IS NULL
		ORDER BY source = 'manual', id LIMIT 1`, in.URL).Scan(&shared)
	switch {
	case err == nil:
		if _, err := linkAddedJob(ctx, tx, userID, shared); err != nil {
			return 0, false, err
		}
		return shared, true, tx.Commit(ctx)
	case !errors.Is(err, pgx.ErrNoRows):
		return 0, false, err
	}

	industry := in.Industry
	if industry == "" {
		industry = "other"
	}
	var companyID int64
	err = tx.QueryRow(ctx, `SELECT id FROM companies WHERE lower(name) = lower($1) ORDER BY ats = 'manual', id LIMIT 1`,
		in.Company).Scan(&companyID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `
			INSERT INTO companies (name, ats, token, industry, enabled)
			VALUES ($1, 'manual', 'manual:' || lower($1), $2, false)
			ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
			RETURNING id`, in.Company, industry).Scan(&companyID)
	}
	if err != nil {
		return 0, false, fmt.Errorf("company: %w", err)
	}

	sum := sha256.Sum256([]byte(in.URL))
	extID := fmt.Sprintf("u%d-%s", userID, hex.EncodeToString(sum[:8]))
	h := sha256.Sum256([]byte(in.Title + "\x00" + in.Location + "\x00" + in.Description))
	contentHash := hex.EncodeToString(h[:])
	var jobID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO jobs (company_id, source, ext_id, title, location_raw, is_remote, url, department,
		                  description, content_hash, added_by, posted_at)
		VALUES ($1, 'manual', $2, $3, NULLIF($4, ''), $5, $6, NULLIF($7, ''), $8, $9, $10, $11)
		RETURNING id`, companyID, extID, in.Title, in.Location, in.Remote, in.URL, in.Department,
		in.Description, contentHash, userID, in.PostedAt).Scan(&jobID); err != nil {
		return 0, false, fmt.Errorf("insert job: %w", err)
	}
	if _, err := linkAddedJob(ctx, tx, userID, jobID); err != nil {
		return 0, false, err
	}
	return jobID, true, tx.Commit(ctx)
}

// linkAddedJob gives the user a job they added on purpose: never
// rule-filtered, straight to the scoring queue and into the tracker as
// saved. A rule exclusion they had for it is lifted. It reports whether
// the job is newly in their scoring queue.
func linkAddedJob(ctx context.Context, tx pgx.Tx, userID, jobID int64) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO user_jobs (user_id, job_id, content_hash, stage)
		SELECT $1, id, content_hash, 'pending_score' FROM jobs WHERE id = $2
		ON CONFLICT (user_id, job_id) DO UPDATE
		SET stage = 'pending_score', exclude_reason = NULL, exclude_evidence = NULL, updated_at = now()
		WHERE user_jobs.stage = 'excluded' AND user_jobs.analysis_id IS NULL`, userID, jobID)
	if err != nil {
		return false, fmt.Errorf("link job: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO applications (user_id, job_id, status, notes) VALUES ($1, $2, 'saved', '')
		ON CONFLICT DO NOTHING`, userID, jobID); err != nil {
		return false, fmt.Errorf("save to tracker: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// manualJobLifetime is how long an added posting stays open after it was
// last added: nothing re-checks a page someone pasted, so it is assumed
// filled after this.
const manualJobLifetime = 45 * 24 * time.Hour

// CloseStaleManualJobs closes added postings past their lifetime.
func CloseStaleManualJobs(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE jobs SET closed_at = now()
		WHERE source = 'manual' AND closed_at IS NULL AND last_seen_at < $1`, time.Now().Add(-manualJobLifetime))
	return tag.RowsAffected(), err
}

var applySuffix = regexp.MustCompile(`/(apply|application)(/.*)?$`)

// boardJobParams maps query parameters that carry a job board's id to
// that board's source.
var boardJobParams = map[string]string{"gh_jid": "greenhouse", "ashby_jid": "ashby"}

var trailingID = regexp.MustCompile(`([0-9]{5,}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})/?(?:apply|application)?/?$`)

// LookupJobByURL finds one of the user's jobs for a page URL: the same
// normalized URL, or a posting whose board id ends the URL path (apply
// pages often live on a different host than the listing).
func LookupJobByURL(ctx context.Context, pool *pgxpool.Pool, userID int64, raw string) (int64, error) {
	norm := NormalizeJobURL(raw)
	// An application form often lives at the posting's URL plus /apply.
	parent := applySuffix.ReplaceAllString(norm, "")
	addrs := make([]string, 0, 3)
	for _, a := range []string{norm, strings.TrimSpace(raw), parent} {
		addrs = append(addrs, strings.TrimRight(a, "/"))
	}
	var id int64
	err := pool.QueryRow(ctx, `
		SELECT j.id FROM jobs j
		WHERE rtrim(j.url, '/') = ANY($2::text[]) AND EXISTS (SELECT 1 FROM user_jobs uj WHERE uj.job_id = j.id AND uj.user_id = $1)
		ORDER BY j.closed_at IS NULL DESC LIMIT 1`, userID, addrs).Scan(&id)
	if !errors.Is(err, pgx.ErrNoRows) {
		return id, err
	}
	u, perr := url.Parse(norm)
	if perr != nil {
		return 0, pgx.ErrNoRows
	}
	// A company's own careers page names the board's job in the query
	// (?gh_jid=4019309 for Greenhouse, ?ashby_jid=<uuid> for Ashby).
	for param, source := range boardJobParams {
		if v := u.Query().Get(param); v != "" {
			err = pool.QueryRow(ctx, `
				SELECT j.id FROM jobs j
				WHERE j.source = $2 AND j.ext_id = $3 AND EXISTS (SELECT 1 FROM user_jobs uj WHERE uj.job_id = j.id AND uj.user_id = $1)
				ORDER BY j.closed_at IS NULL DESC LIMIT 1`, userID, source, v).Scan(&id)
			if !errors.Is(err, pgx.ErrNoRows) {
				return id, err
			}
		}
	}
	m := trailingID.FindStringSubmatch(u.Path)
	if m == nil {
		return 0, pgx.ErrNoRows
	}
	err = pool.QueryRow(ctx, `
		SELECT j.id FROM jobs j
		WHERE (j.ext_id = $2 OR j.url LIKE '%' || $2 || '%')
		  AND EXISTS (SELECT 1 FROM user_jobs uj WHERE uj.job_id = j.id AND uj.user_id = $1)
		ORDER BY j.closed_at IS NULL DESC LIMIT 1`, userID, m[1]).Scan(&id)
	return id, err
}
