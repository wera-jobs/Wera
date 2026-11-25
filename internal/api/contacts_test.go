package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"wera/internal/auth"
	"wera/internal/store"
	"wera/internal/testutil"
)

func TestJobContacts(t *testing.T) {
	ts, pool := testServer(t, nil)
	ctx := context.Background()
	job := testJobIDs[0]
	base := jobURL(ts, job, "/contacts")

	if code, body := do(t, client, http.MethodGet, base, ""); code != 200 || !strings.Contains(body, `"contacts":[]`) {
		t.Fatalf("empty list: %d %s", code, body)
	}
	for _, bad := range []string{
		`{}`,
		`{"name":"   "}`,
		`{"name":"Ada","email":"not an email"}`,
		`{"name":"Ada","linkedin_url":"javascript:alert(1)"}`,
		`{"name":"Ada","linkedin_url":"https://x.com/` + strings.Repeat("a", 500) + `"}`,
		`{"name":"` + strings.Repeat("n", 201) + `"}`,
		`{"name":"Ada","last_contacted_at":"yesterday"}`,
	} {
		if code, body := do(t, client, http.MethodPost, base, bad); code != 400 {
			t.Errorf("POST %s: want 400, got %d %s", bad, code, body)
		}
	}
	code, body := do(t, client, http.MethodPost, base,
		`{"name":" Ada Lovelace ","role":"Recruiter","email":"Ada@Example.com","linkedin_url":"https://www.linkedin.com/in/example","notes":"Met at a meetup","last_contacted_at":"2026-11-20T10:00:00Z","follow_up_at":"2026-11-27T10:00:00Z"}`)
	var c store.JobContact
	if code != 201 || json.Unmarshal([]byte(body), &c) != nil || c.ID == 0 || c.Name != "Ada Lovelace" || c.Email != "ada@example.com" ||
		c.LastContactedAt == nil || c.FollowUpAt == nil || c.JobID != job {
		t.Fatalf("add: %d %s", code, body)
	}
	one := fmt.Sprintf("%s/%d", base, c.ID)

	// A patch changes only what it names; null clears a date.
	code, body = do(t, client, http.MethodPatch, one, `{"notes":"Sent a thank-you","follow_up_at":null}`)
	var p store.JobContact
	if code != 200 || json.Unmarshal([]byte(body), &p) != nil || p.Notes != "Sent a thank-you" || p.FollowUpAt != nil ||
		p.LastContactedAt == nil || p.Role != "Recruiter" || p.Name != "Ada Lovelace" {
		t.Fatalf("patch: %d %s", code, body)
	}
	if code, _ := do(t, client, http.MethodPatch, one, `{"name":""}`); code != 400 {
		t.Errorf("emptying the name: want 400, got %d", code)
	}

	// The global list carries the job.
	code, body = do(t, client, http.MethodGet, ts.URL+"/api/contacts", "")
	var all struct {
		Contacts []store.JobContact `json:"contacts"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &all) != nil || len(all.Contacts) != 1 || all.Contacts[0].JobTitle == "" || all.Contacts[0].Company == "" {
		t.Fatalf("all contacts: %d %s", code, body)
	}

	// Another user sees none of it and can't touch it.
	other := testutil.User(t, pool, false)
	token, hash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession(ctx, pool, other.ID, hash, time.Hour); err != nil {
		t.Fatal(err)
	}
	them := &http.Client{Transport: cookieTransport{token}}
	if code, _ := do(t, them, http.MethodGet, base, ""); code != 404 {
		t.Errorf("other user's job list: want 404, got %d", code)
	}
	if code, _ := do(t, them, http.MethodPost, base, `{"name":"Mallory"}`); code != 404 {
		t.Errorf("other user adding: want 404, got %d", code)
	}
	// Even on a job both of them track.
	if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage) VALUES ($1, $2, 'h', 'scored')`, other.ID, job); err != nil {
		t.Fatal(err)
	}
	if code, body := do(t, them, http.MethodGet, base, ""); code != 200 || !strings.Contains(body, `"contacts":[]`) {
		t.Errorf("shared job, other user: %d %s", code, body)
	}
	if code, _ := do(t, them, http.MethodPatch, one, `{"notes":"mine now"}`); code != 404 {
		t.Errorf("other user patching: want 404, got %d", code)
	}
	if code, _ := do(t, them, http.MethodDelete, one, ""); code != 404 {
		t.Errorf("other user deleting: want 404, got %d", code)
	}
	if _, body := do(t, them, http.MethodGet, ts.URL+"/api/contacts", ""); !strings.Contains(body, `"contacts":[]`) {
		t.Errorf("other user's global list: %s", body)
	}
	// The wrong job in the path doesn't reach it either.
	if code, _ := do(t, client, http.MethodDelete, fmt.Sprintf("%s/%d", jobURL(ts, testJobIDs[1], "/contacts"), c.ID), ""); code != 404 {
		t.Errorf("delete through another job: want 404, got %d", code)
	}

	if code, _ := do(t, client, http.MethodDelete, one, ""); code != 204 {
		t.Errorf("delete: %d", code)
	}
	if code, _ := do(t, client, http.MethodDelete, one, ""); code != 404 {
		t.Errorf("delete again: %d", code)
	}

	// The cap per job.
	if _, err := pool.Exec(ctx, `INSERT INTO user_job_contacts (user_id, job_id, name) SELECT $1, $2, 'n' || g FROM generate_series(1, $3) g`,
		testUserID, job, store.MaxContactsPerJob); err != nil {
		t.Fatal(err)
	}
	if code, _ := do(t, client, http.MethodPost, base, `{"name":"One too many"}`); code != 409 {
		t.Errorf("over the cap: want 409, got %d", code)
	}
}

func TestJobContactsCascade(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := testutil.User(t, pool, false)
	companyID, _ := testutil.Company(t, pool, "other")
	jobs := testutil.Jobs(t, pool, companyID, testutil.Job{Title: "Cascade A"}, testutil.Job{Title: "Cascade B"})
	if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage) SELECT $1, unnest($2::bigint[]), 'h', 'scored'`, user.ID, jobs); err != nil {
		t.Fatal(err)
	}
	name := "Grace"
	for _, j := range jobs {
		if _, err := store.AddContact(ctx, pool, user.ID, j, store.ContactInput{Name: &name}); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		var n int
		pool.QueryRow(ctx, `SELECT count(*) FROM user_job_contacts WHERE user_id = $1`, user.ID).Scan(&n)
		return n
	}
	if _, err := pool.Exec(ctx, `DELETE FROM jobs WHERE id = $1`, jobs[0]); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Errorf("after deleting a job: %d contacts, want 1", n)
	}
	if err := store.DeleteUser(ctx, pool, user.ID); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Errorf("after deleting the user: %d contacts, want 0", n)
	}
}
