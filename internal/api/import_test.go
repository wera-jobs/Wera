package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"wera/internal/store"
)

func TestImportWords(t *testing.T) {
	for in, want := range map[string]string{
		"Wishlist": "saved", "": "saved", "Applied": "applied", "In Review": "applied", "Phone Screen": "interviewing",
		"2nd Interview": "interviewing", "Offer received": "offer", "Rejected": "rejected", "withdrawn": "not_interested",
		"not_interested": "not_interested", "maybe later": "",
	} {
		if got := importStatus(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"2026-10-02": "2026-10-02", "10/2/2026": "2026-10-02", "Oct 2, 2026": "2026-10-02", "soon": "0001-01-01"} {
		if got := importDate(in).Format("2006-01-02"); got != want {
			t.Errorf("date %q: %s", in, got)
		}
	}
	if got := importText(" '=SUM(A1) "); got != "=SUM(A1)" {
		t.Errorf("formula guard: %q", got)
	}
}

func TestImportApplications(t *testing.T) {
	ts, pool := testServer(t, nil)
	ctx := context.Background()
	rows := `[
	  {"company":"Import Co","title":"Platform Engineer","url":"https://jobs.example.com/import-co/1","status":"Phone screen","notes":"Met at a meetup","applied":"2026-10-02"},
	  {"company":"Import Co","title":"Data Engineer","url":"https://jobs.example.com/import-co/2","status":"Wishlist"},
	  {"company":"","title":"No Company","url":"https://jobs.example.com/x","status":"applied"},
	  {"company":"Import Co","title":"Bad Link","url":"jobs.example.com/3","status":"applied"},
	  {"company":"Import Co","title":"Odd Status","url":"https://jobs.example.com/import-co/4","status":"maybe later"}
	]`
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM jobs WHERE url LIKE 'https://jobs.example.com/import-co/%'`)
		pool.Exec(context.Background(), `DELETE FROM companies WHERE name = 'Import Co' AND ats = 'manual'`)
	})
	post := func(body string) (out struct {
		Results  []importResult
		Imported int
	}) {
		t.Helper()
		resp, err := client.Post(ts.URL+"/api/applications/import", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("import: %d", resp.StatusCode)
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	dry := post(`{"dry_run":true,"rows":` + rows + `}`)
	ok := 0
	for _, r := range dry.Results {
		if r.OK {
			ok++
		}
	}
	if ok != 2 || dry.Imported != 0 || dry.Results[0].Status != "interviewing" || !strings.Contains(dry.Results[4].Error, "maybe later") {
		t.Errorf("dry run: %+v", dry)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE url LIKE 'https://jobs.example.com/import-co/%'`).Scan(&n)
	if n != 0 {
		t.Fatal("a dry run saved jobs")
	}

	got := post(`{"rows":` + rows + `}`)
	if got.Imported != 2 || !got.Results[0].Created {
		t.Fatalf("import: %+v", got)
	}
	job, err := store.GetJob(ctx, pool, testUserID, got.Results[0].JobID)
	if err != nil || job.AppStatus == nil || *job.AppStatus != "interviewing" || job.AppNotes == nil || *job.AppNotes != "Met at a meetup" {
		t.Fatalf("imported job: %+v %v", job, err)
	}
	var applied time.Time
	_ = pool.QueryRow(ctx, `SELECT at FROM application_changes WHERE user_id = $1 AND job_id = $2 AND status = 'applied'`, testUserID, job.ID).Scan(&applied)
	if applied.UTC().Format("2006-01-02") != "2026-10-02" {
		t.Errorf("applied step at %v", applied)
	}
	// Importing the same sheet again finds the jobs already there.
	if again := post(`{"rows":` + rows + `}`); again.Imported != 2 || again.Results[0].Created {
		t.Errorf("reimport: %+v", again)
	}
	if code, _ := do(t, client, http.MethodPost, ts.URL+"/api/applications/import", `{"rows":[]}`); code != 400 {
		t.Errorf("empty import: %d", code)
	}
}
