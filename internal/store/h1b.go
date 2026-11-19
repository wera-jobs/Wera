package store

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/h1b"
)

var h1bColumns = []string{"case_number", "fiscal_year", "decision_date", "employer_name", "employer_key", "job_title",
	"soc_code", "soc_title", "wage_from", "wage_to", "wage_level", "worksite_city", "worksite_state", "positions",
	"new_employment", "full_time"}

// H1BWriter batches cases into h1b_lca (a case already stored is kept).
type H1BWriter struct {
	pool  *pgxpool.Pool
	batch [][]any
	first time.Time
	last  time.Time
	Saved int64
}

// NewH1BWriter returns a writer that loads H-1B disclosure rows.
func NewH1BWriter(pool *pgxpool.Pool) *H1BWriter { return &H1BWriter{pool: pool} }

// Add queues a case, writing a batch every 5,000.
func (w *H1BWriter) Add(ctx context.Context, c h1b.Case) error {
	if w.first.IsZero() || c.DecisionDate.Before(w.first) {
		w.first = c.DecisionDate
	}
	if c.DecisionDate.After(w.last) {
		w.last = c.DecisionDate
	}
	w.batch = append(w.batch, []any{c.CaseNumber, c.FiscalYear, c.DecisionDate, c.EmployerName, c.EmployerKey, c.JobTitle,
		c.SOCCode, c.SOCTitle, c.WageFrom, c.WageTo, c.WageLevel, c.WorksiteCity, c.WorksiteState, c.Positions,
		c.NewEmployment, c.FullTime})
	if len(w.batch) >= 5000 {
		return w.Flush(ctx)
	}
	return nil
}

// Flush writes queued cases: copied into a temporary table, then inserted
// skipping case numbers already stored (re-importing a file is harmless).
func (w *H1BWriter) Flush(ctx context.Context) error {
	if len(w.batch) == 0 {
		return nil
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE h1b_in (LIKE h1b_lca INCLUDING DEFAULTS) ON COMMIT DROP`); err != nil {
		return err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"h1b_in"}, h1bColumns, pgx.CopyFromRows(w.batch)); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO h1b_lca SELECT DISTINCT ON (case_number) * FROM h1b_in ON CONFLICT (case_number) DO NOTHING`)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	w.Saved += tag.RowsAffected()
	w.batch = w.batch[:0]
	return nil
}

// Done flushes and records the import.
func (w *H1BWriter) Done(ctx context.Context, source string, read, kept int) error {
	if err := w.Flush(ctx); err != nil {
		return err
	}
	var first, last *time.Time
	if !w.first.IsZero() {
		first, last = &w.first, &w.last
	}
	_, err := w.pool.Exec(ctx, `INSERT INTO h1b_imports (source, rows_read, rows_kept, first_date, last_date) VALUES ($1,$2,$3,$4,$5)`,
		source, read, kept, first, last)
	return err
}

// rekeyH1B recomputes employer keys from the stored names, so changes to
// the normalization apply to data already loaded.
func rekeyH1B(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `SELECT DISTINCT employer_name, employer_key FROM h1b_lca`)
	if err != nil {
		return err
	}
	var changed [][]any
	for rows.Next() {
		var name, key string
		if err := rows.Scan(&name, &key); err != nil {
			rows.Close()
			return err
		}
		if k := h1b.EmployerKey(name); k != key {
			changed = append(changed, []any{name, k})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(changed) == 0 {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE h1b_rekey (employer_name TEXT, employer_key TEXT) ON COMMIT DROP`); err != nil {
		return err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"h1b_rekey"}, []string{"employer_name", "employer_key"}, pgx.CopyFromRows(changed)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE h1b_lca l SET employer_key = r.employer_key FROM h1b_rekey r WHERE l.employer_name = r.employer_name`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MatchH1BEmployers rebuilds which filing employers belong to which
// tracked company (aliases: company name -> other filing names). It
// returns the number of companies matched.
func MatchH1BEmployers(ctx context.Context, pool *pgxpool.Pool, aliases map[string][]string) (int, error) {
	if err := rekeyH1B(ctx, pool); err != nil {
		return 0, err
	}
	rows, err := pool.Query(ctx, `SELECT DISTINCT employer_key FROM h1b_lca`)
	if err != nil {
		return 0, err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	rows, err = pool.Query(ctx, `SELECT id, name FROM companies`)
	if err != nil {
		return 0, err
	}
	type company struct {
		ID   int64
		Name string
	}
	companies, err := pgx.CollectRows(rows, pgx.RowToStructByPos[company])
	if err != nil {
		return 0, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM company_h1b_employers`); err != nil {
		return 0, err
	}
	var batch [][]any
	matched := 0
	m := h1b.NewMatcher(keys)
	for _, c := range companies {
		ks := m.Match(c.Name, aliases[c.Name]...)
		if len(ks) > 0 {
			matched++
		}
		for _, k := range ks {
			batch = append(batch, []any{c.ID, k})
		}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"company_h1b_employers"}, []string{"company_id", "employer_key"}, pgx.CopyFromRows(batch)); err != nil {
		return 0, fmt.Errorf("save matches: %w", err)
	}
	return matched, tx.Commit(ctx)
}

// H1BPeriod is the span of decision dates loaded.
type H1BPeriod struct {
	From *time.Time `json:"from"`
	To   *time.Time `json:"to"`
}

// H1BDataPeriod returns the date range the loaded H-1B data covers.
func H1BDataPeriod(ctx context.Context, pool *pgxpool.Pool) (H1BPeriod, error) {
	var p H1BPeriod
	err := pool.QueryRow(ctx, `SELECT min(decision_date), max(decision_date) FROM h1b_lca`).Scan(&p.From, &p.To)
	return p, err
}

// H1BCount is one row of a breakdown (a title, a place, a wage level).
type H1BCount struct {
	Label      string   `json:"label"`
	Filings    int64    `json:"filings"`
	MedianWage *float64 `json:"median_wage"`
	MinWage    *float64 `json:"min_wage,omitempty"`
	MaxWage    *float64 `json:"max_wage,omitempty"`
}

// H1BDetail describes an employer's certified H-1B applications.
type H1BDetail struct {
	Names      []string   `json:"names"` // as filed
	Filings    int64      `json:"filings"`
	NewHires   int64      `json:"new_hires"` // applications for new employment
	Positions  int64      `json:"positions"`
	WageP25    *float64   `json:"wage_p25"`
	WageMedian *float64   `json:"wage_median"`
	WageP75    *float64   `json:"wage_p75"`
	Titles     []H1BCount `json:"titles"`
	Places     []H1BCount `json:"places"`
	Levels     []H1BCount `json:"levels"`
	Period     H1BPeriod  `json:"period"`
}

// H1BEmployerDetail summarizes the applications filed by these employer keys.
func H1BEmployerDetail(ctx context.Context, pool *pgxpool.Pool, keys []string) (*H1BDetail, error) {
	d := &H1BDetail{Names: []string{}, Titles: []H1BCount{}, Places: []H1BCount{}, Levels: []H1BCount{}}
	err := pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE new_employment), coalesce(sum(positions), 0),
		       percentile_cont(0.25) WITHIN GROUP (ORDER BY wage_from),
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY wage_from),
		       percentile_cont(0.75) WITHIN GROUP (ORDER BY wage_from),
		       coalesce((SELECT array_agg(n ORDER BY c DESC) FROM (
		         SELECT employer_name n, count(*) c FROM h1b_lca WHERE employer_key = ANY($1) GROUP BY 1 ORDER BY 2 DESC LIMIT 5) x), '{}')
		FROM h1b_lca WHERE employer_key = ANY($1)`, keys).
		Scan(&d.Filings, &d.NewHires, &d.Positions, &d.WageP25, &d.WageMedian, &d.WageP75, &d.Names)
	if err != nil || d.Filings == 0 {
		return d, err
	}
	breakdown := func(expr string, limit int) ([]H1BCount, error) {
		rows, err := pool.Query(ctx, `
			SELECT `+expr+` AS label, count(*), percentile_cont(0.5) WITHIN GROUP (ORDER BY wage_from), min(wage_from), max(coalesce(wage_to, wage_from))
			FROM h1b_lca WHERE employer_key = ANY($1) AND `+expr+` <> ''
			GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT $2`, keys, limit)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByPos[H1BCount])
	}
	if d.Titles, err = breakdown(`initcap(lower(job_title))`, 12); err != nil {
		return nil, err
	}
	if d.Places, err = breakdown(`CASE WHEN worksite_city = '' THEN worksite_state ELSE worksite_city || ', ' || worksite_state END`, 8); err != nil {
		return nil, err
	}
	if d.Levels, err = breakdown(`wage_level`, 4); err != nil {
		return nil, err
	}
	d.Period, err = H1BDataPeriod(ctx, pool)
	return d, err
}

// CompanyH1BKeys are the filing employers matched to a company.
func CompanyH1BKeys(ctx context.Context, pool *pgxpool.Pool, companyID int64) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT employer_key FROM company_h1b_employers WHERE company_id = $1`, companyID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// H1BEmployer is a filing employer found by name.
type H1BEmployer struct {
	Key        string   `json:"key"`
	Name       string   `json:"name"`
	Filings    int64    `json:"filings"`
	MedianWage *float64 `json:"median_wage"`
	CompanyID  *int64   `json:"company_id"` // when Wera tracks it
}

// SearchH1BEmployers finds employers by name (any employer in the data,
// tracked or not), largest filers first.
func SearchH1BEmployers(ctx context.Context, pool *pgxpool.Pool, query string, limit int) ([]H1BEmployer, error) {
	key := h1b.EmployerKey(query)
	if key == "" {
		return []H1BEmployer{}, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT l.employer_key, mode() WITHIN GROUP (ORDER BY l.employer_name), count(*),
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY l.wage_from),
		       (SELECT min(company_id) FROM company_h1b_employers m WHERE m.employer_key = l.employer_key)
		FROM h1b_lca l
		WHERE l.employer_key LIKE '%' || $1 || '%'
		GROUP BY l.employer_key
		ORDER BY (l.employer_key = $1) DESC, (l.employer_key LIKE $1 || '%') DESC, count(*) DESC
		LIMIT $2`, key, normalizeLimit(limit, 20, 50))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[H1BEmployer])
}

// H1BSimilar is a filing for a job title like the one being viewed.
type H1BSimilar struct {
	Title    string    `json:"title"`
	WageFrom *float64  `json:"wage_from"`
	WageTo   *float64  `json:"wage_to"`
	Level    string    `json:"level"`
	Place    string    `json:"place"`
	Date     time.Time `json:"date"`
}

// JobH1B is what a job's company filed, and for roles like this one.
type JobH1B struct {
	Matched    bool         `json:"matched"`
	CompanyID  int64        `json:"company_id"`
	Filings    int64        `json:"filings"`
	NewHires   int64        `json:"new_hires"`
	WageMedian *float64     `json:"wage_median"`
	Similar    []H1BSimilar `json:"similar"` // closest titles, most similar first
	SimilarLow *float64     `json:"similar_low"`
	SimilarMid *float64     `json:"similar_median"`
	SimilarHi  *float64     `json:"similar_high"`
	// SimilarCount is how many filings the range rests on.
	SimilarCount int `json:"similar_count"`
	// Level is the wage level the range is for ("" when all levels).
	Level  string    `json:"level"`
	Period H1BPeriod `json:"period"`
}

// JobH1BFor summarizes a job's company filings and those for similar
// titles. With a wage level (I to IV, from the job's seniority), filings
// at that level come first and set the range when there are 3 or more.
func JobH1BFor(ctx context.Context, pool *pgxpool.Pool, companyID int64, title, level string) (*JobH1B, error) {
	out := &JobH1B{CompanyID: companyID, Similar: []H1BSimilar{}}
	keys, err := CompanyH1BKeys(ctx, pool, companyID)
	if err != nil || len(keys) == 0 {
		return out, err
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE new_employment), percentile_cont(0.5) WITHIN GROUP (ORDER BY wage_from)
		FROM h1b_lca WHERE employer_key = ANY($1)`, keys).Scan(&out.Filings, &out.NewHires, &out.WageMedian); err != nil {
		return nil, err
	}
	out.Matched = out.Filings > 0
	rows, err := pool.Query(ctx, `
		SELECT job_title, wage_from, wage_to, wage_level,
		       CASE WHEN worksite_city = '' THEN worksite_state ELSE worksite_city || ', ' || worksite_state END, decision_date
		FROM h1b_lca
		WHERE employer_key = ANY($1) AND similarity(lower(job_title), lower($2)) >= 0.35
		ORDER BY (wage_level = $3) DESC, similarity(lower(job_title), lower($2)) DESC, decision_date DESC
		LIMIT 40`, keys, title, level)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, pgx.RowToStructByPos[H1BSimilar])
	if err != nil {
		return nil, err
	}
	var wages, atLevel []float64
	for _, s := range all {
		if s.WageFrom != nil {
			wages = append(wages, *s.WageFrom)
			if s.Level == level {
				atLevel = append(atLevel, *s.WageFrom)
			}
		}
	}
	if level != "" && len(atLevel) >= 3 {
		wages, out.Level = atLevel, level
	}
	if len(wages) > 0 {
		lo, mid, hi := quantiles(wages)
		out.SimilarLow, out.SimilarMid, out.SimilarHi = &lo, &mid, &hi
		out.SimilarCount = len(wages)
	}
	if len(all) > 8 {
		all = all[:8]
	}
	out.Similar = all
	out.Period, err = H1BDataPeriod(ctx, pool)
	return out, err
}

// quantiles returns the 10th, 50th and 90th percentiles.
func quantiles(v []float64) (float64, float64, float64) {
	s := append([]float64(nil), v...)
	slices.Sort(s)
	at := func(q float64) float64 { return s[int(q*float64(len(s)-1)+0.5)] }
	return at(0.1), at(0.5), at(0.9)
}

// JobCompanyTitle returns a job's company and title, if the user can see
// it: any fetched posting, or an added one they have.
func JobCompanyTitle(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64) (int64, string, error) {
	var cid int64
	var title string
	err := pool.QueryRow(ctx, `
		SELECT company_id, title FROM jobs j WHERE id = $1
		AND (source <> 'manual' OR EXISTS (SELECT 1 FROM user_jobs WHERE job_id = j.id AND user_id = $2))`,
		jobID, userID).Scan(&cid, &title)
	return cid, title, err
}

// PlaceWages is what employers filed for titles like one, in one state.
type PlaceWages struct {
	State  string  `json:"state"`
	Low    float64 `json:"low"`    // 10th percentile
	Median float64 `json:"median"` //
	High   float64 `json:"high"`   // 90th percentile
	Count  int     `json:"count"`
}

// H1BPlaceWages summarizes H-1B filings for titles close to title in a
// state, across all employers; nil with fewer than 5 filings.
func H1BPlaceWages(ctx context.Context, pool *pgxpool.Pool, title, state string) (*PlaceWages, error) {
	p := &PlaceWages{State: state}
	var lo, mid, hi *float64
	err := pool.QueryRow(ctx, `
		SELECT count(*), percentile_cont(0.1) WITHIN GROUP (ORDER BY wage_from),
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY wage_from), percentile_cont(0.9) WITHIN GROUP (ORDER BY wage_from)
		FROM h1b_lca
		WHERE job_title % $1 AND similarity(job_title, $1) >= 0.5 AND worksite_state = $2 AND wage_from > 0`,
		title, state).Scan(&p.Count, &lo, &mid, &hi)
	if err != nil {
		return nil, err
	}
	if p.Count < 5 || lo == nil || mid == nil || hi == nil {
		return nil, nil
	}
	p.Low, p.Median, p.High = *lo, *mid, *hi
	return p, nil
}
