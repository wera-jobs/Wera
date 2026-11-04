package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wera/internal/config"
	"wera/internal/sources"
	"wera/internal/store"
	"wera/internal/testutil"
)

// etagBoard is a ConditionalSource whose postings the test can change; it
// answers 304 while its version matches the caller's ETag.
type etagBoard struct {
	mu      sync.Mutex
	version string
	jobs    []sources.RawJob
	calls   atomic.Int64
}

func (b *etagBoard) Name() string { return "fakeboard" }
func (b *etagBoard) Fetch(ctx context.Context, token string) ([]sources.RawJob, error) {
	jobs, _, err := b.FetchIfChanged(ctx, token, "")
	return jobs, err
}
func (b *etagBoard) FetchIfChanged(_ context.Context, _, etag string) ([]sources.RawJob, string, error) {
	b.calls.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	if etag != "" && etag == b.version {
		return nil, "", sources.ErrNotModified
	}
	return append([]sources.RawJob(nil), b.jobs...), b.version, nil
}

func (b *etagBoard) set(version string, jobs ...sources.RawJob) {
	b.mu.Lock()
	b.version, b.jobs = version, jobs
	b.mu.Unlock()
}

func raw(id, title string) sources.RawJob {
	return sources.RawJob{ExtID: id, Title: title, LocationRaw: "Remote, US", URL: "https://example.com/" + id,
		DescriptionText: "About the role: " + title}
}

func newFetcher(t *testing.T, srcs map[string]sources.Source) *Fetcher {
	return &Fetcher{
		Pool:    testutil.Pool(t),
		Env:     &config.Env{FetchConcurrency: 2},
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sources: srcs,
	}
}

func TestFetchConditionalBoard(t *testing.T) {
	board := &etagBoard{}
	f := newFetcher(t, map[string]sources.Source{"fakeboard": board})
	ctx := context.Background()
	_, name := testutil.Company(t, f.Pool, "ai_ml")
	cos := []config.Company{{Name: name, ATS: "fakeboard", Token: name, Industry: "ai_ml"}}

	board.set("v1", raw("a", "Site Reliability Engineer"), raw("b", "Platform Engineer"))
	st, err := f.Run(ctx, cos, name)
	if err != nil || st.CompaniesOK != 1 || st.JobsNew != 2 {
		t.Fatalf("first fetch: %+v %v", st, err)
	}

	// Same version: a 304, nothing written.
	st, _ = f.Run(ctx, cos, name)
	if st.CompaniesUnchanged != 1 || st.JobsNew != 0 {
		t.Errorf("unchanged board: %+v", st)
	}

	// "b" closes, "a" changes, "c" is new.
	changed := raw("a", "Site Reliability Engineer II")
	board.set("v2", changed, raw("c", "DevOps Engineer"))
	st, _ = f.Run(ctx, cos, name)
	if st.JobsNew != 1 || st.CompaniesUnchanged != 0 {
		t.Errorf("changed board: %+v", st)
	}
	var open, closed int
	f.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE closed_at IS NULL), count(*) FILTER (WHERE closed_at IS NOT NULL)
		FROM jobs j JOIN companies c ON c.id = j.company_id WHERE c.name = $1`, name).Scan(&open, &closed)
	if open != 2 || closed != 1 {
		t.Errorf("want 2 open and 1 closed, got %d and %d", open, closed)
	}
	var title string
	f.Pool.QueryRow(ctx, `SELECT title FROM jobs j JOIN companies c ON c.id = j.company_id
		WHERE c.name = $1 AND ext_id = 'a'`, name).Scan(&title)
	if title != changed.Title {
		t.Errorf("changed posting not updated: %q", title)
	}
}

func TestUnreadBoardsCloseAndReopen(t *testing.T) {
	board := &etagBoard{}
	f := newFetcher(t, map[string]sources.Source{"fakeboard": board})
	ctx := context.Background()
	_, name := testutil.Company(t, f.Pool, "ai_ml")
	freshID, fresh := testutil.Company(t, f.Pool, "ai_ml")
	testutil.Jobs(t, f.Pool, freshID, testutil.Job{Title: "Data Engineer"}, testutil.Job{Title: "ML Engineer"})
	f.Pool.Exec(ctx, `UPDATE companies SET last_ok_at = now() WHERE id = $1`, freshID)
	cos := []config.Company{{Name: name, ATS: "fakeboard", Token: name, Industry: "ai_ml"}}
	board.set("v1", raw("a", "Site Reliability Engineer"), raw("b", "Platform Engineer"))
	if _, err := f.Run(ctx, cos, ""); err != nil {
		t.Fatal(err)
	}
	open := func(company string) (n int) {
		f.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs j JOIN companies c ON c.id = j.company_id
			WHERE c.name = $1 AND j.closed_at IS NULL`, company).Scan(&n)
		return n
	}

	// One board has not been read for eight days; the other was just read.
	f.Pool.Exec(ctx, `UPDATE companies SET last_ok_at = now() - interval '8 days' WHERE name = $1`, name)
	boards, jobs, err := store.CloseUnreadBoards(ctx, f.Pool, time.Now().Add(-7*24*time.Hour))
	if err != nil || boards != 1 || jobs != 2 {
		t.Fatalf("closed %d boards and %d jobs, %v; want 1 and 2", boards, jobs, err)
	}
	if open(name) != 0 || open(fresh) != 2 {
		t.Errorf("open after closing: unread %d, fresh %d", open(name), open(fresh))
	}

	// The board answers again, unchanged: its postings reopen all the same.
	st, err := f.Run(ctx, cos, name)
	if err != nil || st.CompaniesUnchanged != 0 {
		t.Fatalf("read again: %+v %v", st, err)
	}
	if open(name) != 2 {
		t.Errorf("postings did not reopen: %d open", open(name))
	}
	if boards, _, _ := store.CloseUnreadBoards(ctx, f.Pool, time.Now().Add(-7*24*time.Hour)); boards != 0 {
		t.Errorf("a board read just now was closed")
	}
}

func TestUnreadBoardsWaitForAHealthyRun(t *testing.T) {
	f := newFetcher(t, nil)
	ctx := context.Background()
	id, name := testutil.Company(t, f.Pool, "ai_ml")
	testutil.Jobs(t, f.Pool, id, testutil.Job{Title: "Platform Engineer"})
	f.Pool.Exec(ctx, `UPDATE companies SET last_ok_at = now() - interval '30 days' WHERE name = $1`, name)
	p := &Pipeline{Pool: f.Pool, Log: f.Log}
	p.closeUnreadBoards(ctx, &FetchStats{CompaniesOK: 3, CompaniesFailed: 3})
	var n int
	f.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE company_id = $1 AND closed_at IS NULL`, id).Scan(&n)
	if n != 1 {
		t.Fatalf("a run where half the boards failed closed postings")
	}
	p.closeUnreadBoards(ctx, &FetchStats{CompaniesOK: 4, CompaniesFailed: 3})
	f.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE company_id = $1 AND closed_at IS NULL`, id).Scan(&n)
	if n != 0 {
		t.Errorf("a healthy run left %d postings of a board unread for 30 days open", n)
	}
}

// failingBoard fails every fetch with err.
type failingBoard struct{ err error }

func (b failingBoard) Name() string { return "failboard" }
func (b failingBoard) Fetch(context.Context, string) ([]sources.RawJob, error) {
	return nil, b.err
}

func TestFetchFailuresDoNotFailTheRun(t *testing.T) {
	f := newFetcher(t, map[string]sources.Source{
		"down":  failingBoard{errors.New("boom")},
		"maint": failingBoard{sources.ErrUnavailable},
	})
	ctx := context.Background()
	_, broken := testutil.Company(t, f.Pool, "ai_ml")
	_, maint := testutil.Company(t, f.Pool, "ai_ml")
	st, err := f.Run(ctx, []config.Company{
		{Name: broken, ATS: "down", Token: broken, Industry: "ai_ml"},
		{Name: maint, ATS: "maint", Token: maint, Industry: "ai_ml"},
	}, "")
	if err != nil {
		t.Fatalf("a failing board failed the run: %v", err)
	}
	if st.CompaniesFailed != 1 || st.CompaniesDeferred != 1 || st.RetryAt.IsZero() {
		t.Errorf("stats: %+v", st)
	}
	var ok *bool
	var msg *string
	f.Pool.QueryRow(ctx, `SELECT last_fetch_ok, last_fetch_error FROM companies WHERE name = $1`, broken).Scan(&ok, &msg)
	if ok == nil || *ok || msg == nil || *msg != "boom" {
		t.Errorf("failure not recorded: %v %v", ok, msg)
	}
	if deferredOf(st) != 1 {
		t.Errorf("deferredOf: %d", deferredOf(st))
	}
}

func TestFetchUnknownCompany(t *testing.T) {
	f := newFetcher(t, nil)
	if _, err := f.Run(context.Background(), nil, "no-such-company"); err == nil {
		t.Error("want an error for an unknown company")
	}
}

// detailBoard is a DetailSource: the list has no descriptions and every
// detail is its own request (counted).
type detailBoard struct {
	jobs    []sources.RawJob
	details atomic.Int64
}

func (b *detailBoard) Name() string { return "detailboard" }
func (b *detailBoard) Fetch(ctx context.Context, token string) ([]sources.RawJob, error) {
	return sources.FetchAll(ctx, b, token)
}
func (b *detailBoard) List(context.Context, string) ([]sources.RawJob, error) {
	out := make([]sources.RawJob, len(b.jobs))
	for i, j := range b.jobs {
		out[i] = sources.RawJob{ExtID: j.ExtID, Title: j.Title, LocationRaw: j.LocationRaw, URL: j.URL}
	}
	return out, nil
}
func (b *detailBoard) Detail(_ context.Context, _ string, j *sources.RawJob) error {
	b.details.Add(1)
	j.DescriptionText = "Details for " + j.Title
	return nil
}

func TestFetchDetailsOnlyForNewPostings(t *testing.T) {
	board := &detailBoard{jobs: []sources.RawJob{raw("1", "SRE"), raw("2", "Platform Engineer")}}
	f := newFetcher(t, map[string]sources.Source{"detailboard": board})
	ctx := context.Background()
	_, name := testutil.Company(t, f.Pool, "ai_ml")
	cos := []config.Company{{Name: name, ATS: "detailboard", Token: name, Industry: "ai_ml"}}

	if st, err := f.Run(ctx, cos, name); err != nil || st.JobsNew != 2 {
		t.Fatalf("first fetch: %+v %v", st, err)
	}
	if board.details.Load() != 2 {
		t.Fatalf("first fetch details: %d", board.details.Load())
	}
	board.jobs = append(board.jobs, raw("3", "DevOps Engineer"))
	if st, _ := f.Run(ctx, cos, name); st.JobsNew != 1 {
		t.Errorf("second fetch: %+v", st)
	}
	if board.details.Load() != 3 {
		t.Errorf("known postings were fetched again: %d detail requests", board.details.Load())
	}

	// Past the run's detail budget the board is still listed, but new
	// postings wait for a later run.
	board.jobs = append(board.jobs, raw("4", "Data Engineer"))
	f.DetailBudget = -time.Second
	if st, err := f.Run(ctx, cos, name); err != nil || st.JobsNew != 0 || st.CompaniesOK != 1 {
		t.Errorf("fetch past the budget: %+v %v", st, err)
	}
	if board.details.Load() != 3 {
		t.Errorf("details requested past the budget: %d", board.details.Load())
	}
	f.DetailBudget = 0
	if st, _ := f.Run(ctx, cos, name); st.JobsNew != 1 {
		t.Errorf("the next run did not catch up: %+v", st)
	}
}

func TestFetchLeavesAddedJobsOpen(t *testing.T) {
	ctx := context.Background()
	board := &etagBoard{}
	board.set("v1", raw("1", "Engineer"), raw("2", "Analyst"))
	f := newFetcher(t, map[string]sources.Source{"fakeboard": board})
	companyID, name := testutil.Company(t, f.Pool, "ai_ml")
	companies := []config.Company{{Name: name, ATS: "fakeboard", Token: name, Industry: "ai_ml"}}
	if _, err := f.Run(ctx, companies, name); err != nil {
		t.Fatal(err)
	}
	// Saved from the company's own careers site, so the board does not list it.
	user := testutil.User(t, f.Pool, false)
	added, _, err := store.AddManualJob(ctx, f.Pool, user.ID, store.ManualJob{URL: "https://careers.added.example/" + name, Company: name, Title: "Recruiter"})
	if err != nil {
		t.Fatal(err)
	}
	board.set("v2", raw("1", "Engineer"))
	if _, err := f.Run(ctx, companies, name); err != nil {
		t.Fatal(err)
	}
	if isClosed(t, f, added) {
		t.Error("a fetch closed a job the user added: the board never listed it")
	}
	var gone int64
	if err := f.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE company_id = $1 AND ext_id = '2' AND closed_at IS NOT NULL`, companyID).Scan(&gone); err != nil || gone != 1 {
		t.Errorf("the board's removed posting: closed %d, %v", gone, err)
	}
}

func TestAddingAClosedJobAgainReopensIt(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	user := testutil.User(t, pool, false)
	_, name := testutil.Company(t, pool, "ai_ml")
	job := store.ManualJob{URL: "https://careers.again.example/" + name, Company: name, Title: "Recruiter"}
	id, _, err := store.AddManualJob(ctx, pool, user.ID, job)
	if err != nil {
		t.Fatal(err)
	}
	// Added long ago and past its lifetime.
	if _, err := pool.Exec(ctx, `UPDATE jobs SET first_seen_at = now() - interval '90 days', last_seen_at = now() - interval '90 days' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if n, err := store.CloseStaleManualJobs(ctx, pool); err != nil || n < 1 {
		t.Fatalf("closed %d, %v", n, err)
	}
	again, _, err := store.AddManualJob(ctx, pool, user.ID, job)
	if err != nil || again != id {
		t.Fatalf("job %d, want %d: %v", again, id, err)
	}
	f := &Fetcher{Pool: pool}
	if isClosed(t, f, id) {
		t.Fatal("the user added it again and it stayed closed")
	}
	// Its lifetime starts over, or the next run would close it again.
	if _, err := store.CloseStaleManualJobs(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if isClosed(t, f, id) {
		t.Error("closed again: its lifetime did not restart")
	}
}

func isClosed(t *testing.T, f *Fetcher, id int64) bool {
	t.Helper()
	var closed bool
	if err := f.Pool.QueryRow(context.Background(), `SELECT closed_at IS NOT NULL FROM jobs WHERE id = $1`, id).Scan(&closed); err != nil {
		t.Fatal(err)
	}
	return closed
}

func TestFetchSavesPostingsWithBytesPostgresRefuses(t *testing.T) {
	ctx := context.Background()
	board := &etagBoard{}
	bad := raw("9", "Engineer\x00 II")
	bad.DescriptionText = "Line one\x00\nLine \xff two"
	bad.LocationRaw = "Remote\x00"
	bad.Department = "Eng\x00"
	board.set("v1", raw("8", "Fine"), bad)
	f := newFetcher(t, map[string]sources.Source{"fakeboard": board})
	companyID, name := testutil.Company(t, f.Pool, "ai_ml")
	st, err := f.Run(ctx, []config.Company{{Name: name, ATS: "fakeboard", Token: name, Industry: "ai_ml"}}, name)
	if err != nil {
		t.Fatal(err)
	}
	if st.CompaniesFailed != 0 || st.JobsNew != 2 {
		t.Fatalf("one odd posting cost the company its jobs: %+v", st)
	}
	var title, desc string
	if err := f.Pool.QueryRow(ctx, `SELECT title, description FROM jobs WHERE company_id = $1 AND ext_id = '9'`, companyID).Scan(&title, &desc); err != nil {
		t.Fatal(err)
	}
	if title != "Engineer II" || desc != "Line one\nLine  two" {
		t.Errorf("%q %q", title, desc)
	}
}
