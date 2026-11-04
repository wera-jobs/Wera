package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/config"
)

// SyncCompanies upserts every company from companies.yaml into the
// companies table (keyed by name) and returns name -> id.
func SyncCompanies(ctx context.Context, pool *pgxpool.Pool, comps []config.Company) (map[string]int64, error) {
	if len(comps) == 0 {
		return map[string]int64{}, nil
	}
	if err := reconcileBoards(ctx, pool, comps); err != nil {
		return nil, fmt.Errorf("reconcile boards: %w", err)
	}
	b := &pgx.Batch{}
	for _, c := range comps {
		b.Queue(`INSERT INTO companies (name, ats, token, industry, enabled)
		         VALUES ($1, $2, $3, $4, $5)
		         ON CONFLICT (name) DO UPDATE
		         SET ats = EXCLUDED.ats,
		               token = EXCLUDED.token,
		               industry = EXCLUDED.industry,
		               enabled = EXCLUDED.enabled`,
			c.Name, c.ATS, c.Token, c.Industry, c.IsEnabled())
	}
	br := pool.SendBatch(ctx, b)
	for range comps {
		if _, err := br.Exec(); err != nil {
			br.Close()
			return nil, fmt.Errorf("upsert company: %w", err)
		}
	}
	if err := br.Close(); err != nil {
		return nil, err
	}

	names := make([]string, len(comps))
	for i, c := range comps {
		names[i] = c.Name
	}
	rows, err := pool.Query(ctx, `SELECT id, name FROM companies WHERE name = ANY($1)`, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make(map[string]int64, len(comps))
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		ids[name] = id
	}
	return ids, rows.Err()
}

// reconcileBoards hands each board to the company that now lists it when
// the database holds that board under another name (an entry renamed in
// companies.yaml, or a board first found through an added job), so the
// upsert below never trips over the unique board. The old row is renamed;
// when a row by the new name exists too, its jobs move there and the old
// row lets go of the board.
func reconcileBoards(ctx context.Context, pool *pgxpool.Pool, comps []config.Company) error {
	type row struct {
		id   int64
		name string
	}
	rows, err := pool.Query(ctx, `SELECT id, name, ats, token FROM companies`)
	if err != nil {
		return err
	}
	byBoard := map[string]row{}
	byName := map[string]int64{}
	for rows.Next() {
		var r row
		var ats, token string
		if err := rows.Scan(&r.id, &r.name, &ats, &token); err != nil {
			rows.Close()
			return err
		}
		byBoard[ats+"/"+token] = r
		byName[r.name] = r.id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range comps {
		old, ok := byBoard[c.ATS+"/"+c.Token]
		if !ok || old.name == c.Name {
			continue
		}
		if _, taken := byName[c.Name]; !taken {
			if _, err := pool.Exec(ctx, `UPDATE companies SET name = $2 WHERE id = $1`, old.id, c.Name); err != nil {
				return err
			}
			delete(byName, old.name)
			byName[c.Name] = old.id
			continue
		}
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE jobs SET company_id = $2 WHERE company_id = $1`, old.id, byName[c.Name]); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE companies SET ats = 'manual', token = 'manual:' || id, enabled = false WHERE id = $1`, old.id)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// WatchAddedBoard starts watching the job board (ats, token) that a job a
// user added came from: the "manual" company the job was filed under
// becomes that board's company and is fetched every run from then on. It
// returns the company's name and whether it is newly watched; nothing
// changes when the board is already known or the job's company is a
// watched one.
func WatchAddedBoard(ctx context.Context, pool *pgxpool.Pool, userID, jobID int64, ats, token string) (string, bool, error) {
	var name string
	tag, err := pool.Exec(ctx, `
		UPDATE companies c
		SET ats = $3, token = $4, enabled = true, added_at = now(), added_by = $1
		FROM jobs j
		WHERE j.id = $2 AND c.id = j.company_id AND c.ats = 'manual'
		  AND NOT EXISTS (SELECT 1 FROM companies k WHERE k.ats = $3 AND lower(k.token) = lower($4))`,
		userID, jobID, ats, token)
	if err != nil {
		return "", false, fmt.Errorf("watch board: %w", err)
	}
	if err := pool.QueryRow(ctx, `SELECT c.name FROM jobs j JOIN companies c ON c.id = j.company_id WHERE j.id = $1`,
		jobID).Scan(&name); err != nil {
		return "", false, err
	}
	return name, tag.RowsAffected() == 1, nil
}

// CanWatchBoard reports whether WatchAddedBoard would watch this board for
// the job: its company is a "manual" one and no company has the board yet.
func CanWatchBoard(ctx context.Context, pool *pgxpool.Pool, jobID int64, ats, token string) (bool, error) {
	var ok bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM jobs j JOIN companies c ON c.id = j.company_id WHERE j.id = $1 AND c.ats = 'manual')
		   AND NOT EXISTS (SELECT 1 FROM companies k WHERE k.ats = $2 AND lower(k.token) = lower($3))`,
		jobID, ats, token).Scan(&ok)
	return ok, err
}

// AddedCompanies returns the enabled boards found through added jobs
// (see WatchAddedBoard), to fetch alongside companies.yaml.
func AddedCompanies(ctx context.Context, pool *pgxpool.Pool) ([]config.Company, error) {
	rows, err := pool.Query(ctx, `
		SELECT name, ats, token, industry FROM companies
		WHERE added_at IS NOT NULL AND enabled AND ats <> 'manual'
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []config.Company
	for rows.Next() {
		var c config.Company
		if err := rows.Scan(&c.Name, &c.ATS, &c.Token, &c.Industry); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateFetchStatus records the outcome of one company's fetch in the
// companies table.
func UpdateFetchStatus(ctx context.Context, pool *pgxpool.Pool, companyID int64, ok bool, errMsg string) error {
	_, err := pool.Exec(ctx, `
		UPDATE companies
		SET last_fetch_at = now(),
		    last_fetch_ok = $2,
		    last_fetch_error = NULLIF($3, ''),
		    last_ok_at = CASE WHEN $2 THEN now() ELSE last_ok_at END
		WHERE id = $1`, companyID, ok, errMsg)
	if err != nil {
		return fmt.Errorf("update fetch status for company %d: %w", companyID, err)
	}
	return nil
}

// CloseUnreadBoards closes the open postings of boards not read
// successfully since before, whether they fail (the company moved to
// another job system or took its board down) or are no longer fetched
// (disabled or removed). Nothing else would close those postings. The
// board's ETag and last full sweep are cleared, so when it is read again
// every posting it still lists reopens, even if the board itself did not
// change. Added jobs are left to CloseStaleManualJobs. It returns how many
// boards and postings were closed.
func CloseUnreadBoards(ctx context.Context, pool *pgxpool.Pool, before time.Time) (boards, jobs int64, err error) {
	err = pool.QueryRow(ctx, `
		WITH unread AS (
		  UPDATE companies c SET etag = NULL, last_full_fetch_at = NULL
		  WHERE c.last_ok_at < $1
		    AND EXISTS (SELECT 1 FROM jobs j WHERE j.company_id = c.id AND j.closed_at IS NULL AND j.source <> 'manual')
		  RETURNING c.id
		), closed AS (
		  UPDATE jobs j SET closed_at = now()
		  WHERE j.company_id IN (SELECT id FROM unread) AND j.closed_at IS NULL AND j.source <> 'manual'
		  RETURNING 1
		)
		SELECT (SELECT count(*) FROM unread), (SELECT count(*) FROM closed)`, before).Scan(&boards, &jobs)
	if err != nil {
		return 0, 0, fmt.Errorf("close postings of unread boards: %w", err)
	}
	return boards, jobs, nil
}

// CompanyETag returns the ETag of a company's last successful fetch ("" if none).
func CompanyETag(ctx context.Context, pool *pgxpool.Pool, companyID int64) (string, error) {
	var etag *string
	if err := pool.QueryRow(ctx, `SELECT etag FROM companies WHERE id = $1`, companyID).Scan(&etag); err != nil {
		return "", err
	}
	if etag == nil {
		return "", nil
	}
	return *etag, nil
}

// SetCompanyETag records the ETag of a fetch whose jobs were saved.
func SetCompanyETag(ctx context.Context, pool *pgxpool.Pool, companyID int64, etag string) error {
	_, err := pool.Exec(ctx, `UPDATE companies SET etag = NULLIF($2, '') WHERE id = $1`, companyID, etag)
	return err
}

// LastFullFetch returns when a company's whole board was last listed
// (zero time if never).
func LastFullFetch(ctx context.Context, pool *pgxpool.Pool, companyID int64) (time.Time, error) {
	var t *time.Time
	if err := pool.QueryRow(ctx, `SELECT last_full_fetch_at FROM companies WHERE id = $1`, companyID).Scan(&t); err != nil {
		return time.Time{}, err
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}

// MarkFullFetch records a complete listing of a company's board.
func MarkFullFetch(ctx context.Context, pool *pgxpool.Pool, companyID int64) error {
	_, err := pool.Exec(ctx, `UPDATE companies SET last_full_fetch_at = now() WHERE id = $1`, companyID)
	return err
}
