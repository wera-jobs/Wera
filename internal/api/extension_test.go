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
	"wera/internal/config"
	"wera/internal/resume"
	"wera/internal/store"
	"wera/internal/testutil"
)

// extClient is how the extension calls the API: a bearer token and the
// chrome-extension:// origin (which the cookie origin check would refuse).
type extTransport struct{ token string }

func (t extTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Origin", "chrome-extension://abcdefghijklmnop")
	if t.token != "" {
		r.Header.Set("Authorization", "Bearer "+t.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// connectExtension signs a fresh user up and connects an extension.
func connectExtension(t *testing.T, base string) (*http.Client, string) {
	t.Helper()
	email := testutil.Unique("ext") + "@example.com"
	if code, body := do(t, &http.Client{}, http.MethodPost, base+"/api/auth/signup", `{"email":"`+email+`","password":"extension pass 1","name":"Ext User"}`); code != 201 {
		t.Fatalf("signup: %d %s", code, body)
	}
	anonExt := &http.Client{Transport: extTransport{}}
	if code, _ := do(t, anonExt, http.MethodPost, base+"/api/ext/tokens", `{"email":"`+email+`","password":"wrong password"}`); code != 401 {
		t.Errorf("wrong password: want 401, got %d", code)
	}
	code, body := do(t, anonExt, http.MethodPost, base+"/api/ext/tokens", `{"email":"`+email+`","password":"extension pass 1","name":"Chrome on laptop"}`)
	if code != 201 {
		t.Fatalf("connect: %d %s", code, body)
	}
	var out struct{ Token string }
	json.Unmarshal([]byte(body), &out)
	if !strings.HasPrefix(out.Token, "wera_ext_") {
		t.Fatalf("token: %s", body)
	}
	return &http.Client{Transport: extTransport{out.Token}}, email
}

func TestExtensionTokensAndAddJob(t *testing.T) {
	ts, pool := testServer(t, nil)
	ext, email := connectExtension(t, ts.URL)
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM users WHERE email = $1`, email) })

	if code, body := do(t, ext, http.MethodGet, ts.URL+"/api/auth/me", ""); code != 200 || !strings.Contains(body, email) {
		t.Fatalf("me with token: %d %s", code, body)
	}

	add := `{"url":"https://jobs.example.com/acme/123?utm_source=linkedin","company":"Acme Robotics E2E","title":"Site Reliability Engineer",
	        "location":"Austin, TX","work_mode":"onsite","description":"Keep robots online.","industry":"robotics"}`
	code, body := do(t, ext, http.MethodPost, ts.URL+"/api/ext/jobs", add)
	if code != 201 || !strings.Contains(body, `"source":"manual"`) || !strings.Contains(body, `"application_status":"saved"`) {
		t.Fatalf("add job: %d %s", code, body)
	}
	var job struct{ ID, CompanyID int64 }
	json.Unmarshal([]byte(body), &job)
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM companies WHERE id = $1 AND ats = 'manual'`, job.CompanyID)
	})
	if code, again := do(t, ext, http.MethodPost, ts.URL+"/api/ext/jobs", add); code != 200 || !strings.Contains(again, fmt.Sprintf(`"id":%d`, job.ID)) {
		t.Errorf("same URL again should return the same job: %d", code)
	}
	// A posting without a location (stored as NULL) still saves and reads back.
	if code, body := do(t, ext, http.MethodPost, ts.URL+"/api/ext/jobs",
		`{"url":"https://jobs.example.com/acme/124","company":"Acme Robotics E2E","title":"Platform Engineer"}`); code != 201 || !strings.Contains(body, `"location_raw":""`) {
		t.Errorf("add job without a location: %d %s", code, body)
	}
	// The posting date the page gives is kept; one in the future is not.
	if code, body := do(t, ext, http.MethodPost, ts.URL+"/api/ext/jobs",
		`{"url":"https://jobs.example.com/acme/125","company":"Acme Robotics E2E","title":"Data Engineer","posted":"2026-09-01T00:00:00Z"}`); code != 201 {
		t.Errorf("add job with a posting date: %d %s", code, body)
	} else {
		var got struct {
			PostedAt *time.Time `json:"posted_at"`
		}
		json.Unmarshal([]byte(body), &got)
		if got.PostedAt == nil || !got.PostedAt.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("posting date not kept: %v", got.PostedAt)
		}
	}
	if code, body := do(t, ext, http.MethodPost, ts.URL+"/api/ext/jobs",
		`{"url":"https://jobs.example.com/acme/126","company":"Acme Robotics E2E","title":"Data Engineer II","posted":"2999-01-01"}`); code != 201 || !strings.Contains(body, `"posted_at":null`) {
		t.Errorf("a posting date in the future should be dropped: %d %s", code, body)
	}
	if code, _ := do(t, ext, http.MethodPost, ts.URL+"/api/ext/jobs", `{"url":"javascript:alert(1)","company":"A","title":"B"}`); code != 400 {
		t.Errorf("non-http URL: want 400, got %d", code)
	}
	_, body = do(t, ext, http.MethodGet, ts.URL+"/api/ext/jobs/lookup?url="+
		"https%3A%2F%2Fjobs.example.com%2Facme%2F123%2F%23apply", "")
	if !strings.Contains(body, fmt.Sprintf(`"id":%d`, job.ID)) {
		t.Errorf("lookup by URL failed: %s", body)
	}
	_, body = do(t, ext, http.MethodGet, ts.URL+"/api/ext/jobs/lookup?url="+
		"https%3A%2F%2Fjobs.example.com%2Facme%2F123%2Fapply", "")
	if !strings.Contains(body, fmt.Sprintf(`"id":%d`, job.ID)) {
		t.Errorf("lookup from the /apply page failed: %s", body)
	}
	if _, body = do(t, ext, http.MethodGet, ts.URL+"/api/ext/jobs/lookup?url=https%3A%2F%2Fnowhere.example%2Fx", ""); body != "{\"job\":null}\n" {
		t.Errorf("unknown URL: %s", body)
	}

	// A board job opened on the company's own careers page (?gh_jid=...).
	ctx := context.Background()
	ghExt := testutil.Unique("9")
	var ghCompany, ghJob int64
	if err := pool.QueryRow(ctx, `INSERT INTO companies (name, ats, token, industry, enabled)
		VALUES ($1, 'greenhouse', $1, 'robotics', false) RETURNING id`, "GH Robotics "+ghExt).Scan(&ghCompany); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM user_jobs WHERE job_id IN (SELECT id FROM jobs WHERE company_id = $1)`, ghCompany)
		pool.Exec(ctx, `DELETE FROM jobs WHERE company_id = $1`, ghCompany)
		pool.Exec(ctx, `DELETE FROM companies WHERE id = $1`, ghCompany)
	})
	if err := pool.QueryRow(ctx, `INSERT INTO jobs (company_id, source, ext_id, title, url, content_hash)
		VALUES ($1, 'greenhouse', $2, 'Platform Engineer', 'https://job-boards.greenhouse.io/ghrobotics/jobs/' || $2, 'h') RETURNING id`,
		ghCompany, ghExt).Scan(&ghJob); err != nil {
		t.Fatal(err)
	}
	if tag, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage)
		SELECT id, $1, 'h', 'pending_score' FROM users WHERE email = $2`, ghJob, email); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("link job: %v (%d rows)", err, tag.RowsAffected())
	}
	_, body = do(t, ext, http.MethodGet, ts.URL+"/api/ext/jobs/lookup?url="+
		"https%3A%2F%2Fwww.ghrobotics.example%2Fcareers%2Fopen-roles%3Fgh_jid%3D"+ghExt, "")
	if !strings.Contains(body, fmt.Sprintf(`"id":%d`, ghJob)) {
		t.Errorf("lookup by gh_jid failed: %s", body)
	}

	// Another user adding the same posting from the dashboard shares it.
	if code, body := do(t, client, http.MethodPost, ts.URL+"/api/jobs", add); code != 201 || !strings.Contains(body, fmt.Sprintf(`"id":%d`, job.ID)) {
		t.Errorf("the same posting added by another user: %d %s", code, body)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM user_jobs WHERE job_id = $1`, job.ID) })
	if _, body := do(t, client, http.MethodGet, ts.URL+"/api/companies", ""); strings.Contains(body, "Acme Robotics E2E") {
		t.Error("a manual company leaked into the shared company list")
	}

	// The dashboard lists connections; the extension can sign itself out.
	_, body = do(t, ext, http.MethodGet, ts.URL+"/api/ext/tokens", "")
	if !strings.Contains(body, "Chrome on laptop") {
		t.Errorf("connections: %s", body)
	}
	if code, _ := do(t, ext, http.MethodDelete, ts.URL+"/api/ext/tokens/current", ""); code != 204 {
		t.Errorf("sign out: %d", code)
	}
	if code, _ := do(t, ext, http.MethodGet, ts.URL+"/api/auth/me", ""); code != 401 {
		t.Errorf("revoked token still works: %d", code)
	}
}

func TestBearerTokensAreNotCookies(t *testing.T) {
	ts, _ := testServer(t, nil)
	bogus := &http.Client{Transport: extTransport{"wera_ext_not-a-real-token"}}
	if code, _ := do(t, bogus, http.MethodGet, ts.URL+"/api/auth/me", ""); code != 401 {
		t.Errorf("unknown token: want 401, got %d", code)
	}
	// A cookie session cannot be used from the extension origin without a token.
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/settings", strings.NewReader(`{}`))
	req.Header.Set("Origin", "chrome-extension://abcdefghijklmnop")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("cookie write from another origin: want 403, got %d", resp.StatusCode)
	}
}

func TestApplicantDetails(t *testing.T) {
	ts, _ := testServer(t, nil)
	code, body := do(t, client, http.MethodGet, ts.URL+"/api/applicant", "")
	if code != 200 || !strings.Contains(body, `"saved":false`) || !strings.Contains(body, `"gender":""`) || !strings.Contains(body, `"first_name":"API"`) {
		t.Fatalf("defaults: %d %s", code, body)
	}
	if code, _ := do(t, client, http.MethodPut, ts.URL+"/api/applicant", `{"needs_sponsorship":"maybe"}`); code != 400 {
		t.Errorf("bad answer: want 400, got %d", code)
	}
	code, body = do(t, client, http.MethodPut, ts.URL+"/api/applicant", `{"first_name":"Ada","phone":"512 555 0142","needs_sponsorship":"yes"}`)
	if code != 200 || !strings.Contains(body, `"saved":true`) || !strings.Contains(body, `"phone":"512 555 0142"`) {
		t.Errorf("save: %d %s", code, body)
	}
}

// Details the user never typed come from their profile answers and their
// main resume; what they saved always wins.
func TestApplicantFilledFromResumeAndProfile(t *testing.T) {
	ts, pool := testServer(t, nil)
	ctx := context.Background()
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM resumes WHERE user_id = $1`, testUserID)
		pool.Exec(ctx, `DELETE FROM user_applicant WHERE user_id = $1`, testUserID)
		pool.Exec(ctx, `UPDATE profiles SET answers = '{}' WHERE user_id = $1`, testUserID)
	})
	testutil.Profile(t, pool, testUserID, "# Candidate Profile", config.Preferences{RoleFamilies: []string{"data"}, Levels: []string{"entry"}})
	pool.Exec(ctx, `UPDATE profiles SET answers = '{"current_title":"Data Scientist","years_experience":3,"work_authorization":"sponsorship_future","work_modes":["remote"]}' WHERE user_id = $1`, testUserID)
	doc := &store.ResumeDoc{Title: "My resume", Layout: resume.DefaultLayout, Data: resume.Resume{
		Name: "Jesse Chumo", Email: "jesse@example.com", Phone: "469 555 0101", Location: "Dallas, TX",
		Links: []resume.Link{{Label: "linkedin.com/in/jesse", URL: "https://linkedin.com/in/jesse"}},
		Sections: []resume.Section{
			{ID: "e", Kind: resume.KindEntries, Title: "Education", Entries: []resume.Entry{{ID: "e1", Heading: "UT Dallas", Subheading: "Master of Science in Business Analytics", Dates: "2022 – 2024"}}},
			{ID: "x", Kind: resume.KindEntries, Title: "Experience", Entries: []resume.Entry{{ID: "x1", Heading: "Globex", Subheading: "Analyst", Dates: "Jun 2024 – Present", Bullets: []resume.Bullet{{ID: "b", Text: "Built models"}}}}},
		},
	}}
	if _, err := store.SaveResume(ctx, pool, testUserID, doc); err != nil {
		t.Fatal(err)
	}
	var v applicantView
	code, body := do(t, client, http.MethodGet, ts.URL+"/api/applicant", "")
	if err := json.Unmarshal([]byte(body), &v); code != 200 || err != nil {
		t.Fatalf("get: %d %s", code, body)
	}
	a := v.Applicant
	if a.School != "UT Dallas" || a.Degree != "Master of Science" || a.Major != "Business Analytics" || a.GraduationYear != "2024" ||
		a.CurrentCompany != "Globex" || a.LinkedIn != "https://linkedin.com/in/jesse" || a.City != "Dallas" || a.State != "TX" {
		t.Errorf("from the resume: %+v", a)
	}
	// The profile answers come first: its title, years and work authorization.
	if a.CurrentTitle != "Data Scientist" || a.YearsExperience != "3" || a.AuthorizedToWork != "yes" || a.NeedsSponsorship != "yes" || a.WorkPreference != "remote" {
		t.Errorf("from the profile: %+v", a)
	}
	if v.Sources["school"] != "resume" || v.Sources["work_preference"] != "profile" {
		t.Errorf("sources: %v", v.Sources)
	}
	if len(v.Experience) != 1 || v.Experience[0].Description != "Built models" || !v.Experience[0].Current || len(v.Education) != 1 {
		t.Errorf("history: %+v %+v", v.Experience, v.Education)
	}
	// Saved details win, and are not marked as filled in.
	do(t, client, http.MethodPut, ts.URL+"/api/applicant", `{"school":"SMU","gender":"Male"}`)
	_, body = do(t, client, http.MethodGet, ts.URL+"/api/applicant", "")
	v = applicantView{}
	json.Unmarshal([]byte(body), &v)
	if v.Applicant.School != "SMU" || v.Sources["school"] != "" || v.Applicant.Degree != "Master of Science" || v.Applicant.Gender != "Male" {
		t.Errorf("saved wins: %+v %v", v.Applicant, v.Sources)
	}
}

func TestExtensionAIFeatures(t *testing.T) {
	ts, llm := llmServer(t)
	pool := testPool(t)
	ctx := context.Background()
	testutil.Profile(t, pool, testUserID, "# Candidate Profile\n## Target roles\nSRE.", config.Preferences{RoleFamilies: []string{"sre"}, Levels: []string{"entry"}})
	pool.Exec(ctx, `UPDATE profiles SET resume_text = 'SRE intern at Acme 2024. Go, Kubernetes. (512) 555-0142' WHERE user_id = $1`, testUserID)

	page := "Menu Jobs\nAbout the role: keep our robot fleet online and fast. You will use Kubernetes and Go every day.\nApply"
	b, _ := json.Marshal(map[string]string{"url": "https://acme.example/jobs/1", "title": "SRE at Acme", "text": page + strings.Repeat(" More details about benefits.", 10)})
	code, body := do(t, client, http.MethodPost, ts.URL+"/api/ext/extract", string(b))
	if code != 200 || !strings.Contains(body, `"company":"Acme Robotics"`) || !strings.Contains(body, "About the role: keep our robot fleet online and fast. You will use Kubernetes and Go every day.") {
		t.Errorf("extract: %d %s", code, body)
	}
	if code, _ := do(t, client, http.MethodPost, ts.URL+"/api/ext/extract", `{"text":"too short"}`); code != 422 {
		t.Errorf("short page: want 422, got %d", code)
	}

	code, body = do(t, client, http.MethodPost, ts.URL+"/api/ext/answer", `{"question":"Why do you want to work at Acme?","company":"Acme","title":"SRE","description":"Robots"}`)
	if code != 200 || !strings.Contains(body, "keep robots online") || strings.Contains(body, "—") {
		t.Errorf("answer: %d %s", code, body)
	}
	if _, body = do(t, client, http.MethodPost, ts.URL+"/api/ext/answer", `{"question":"What are your salary expectations?"}`); !strings.Contains(body, `"needs_input"`) {
		t.Errorf("salary should ask the user: %s", body)
	}
	// A rework: the draft and the user's steer are taken; a steer past the limit is refused.
	if code, _ = do(t, client, http.MethodPost, ts.URL+"/api/ext/answer", `{"question":"Why do you want to work at Acme?","company":"Acme","title":"SRE","draft":"Robots.","instructions":"shorter"}`); code != http.StatusOK {
		t.Errorf("rework: %d", code)
	}
	if code, _ = do(t, client, http.MethodPost, ts.URL+"/api/ext/answer", `{"question":"Why do you want to work at Acme?","instructions":"`+strings.Repeat("x", 700)+`"}`); code != http.StatusBadRequest {
		t.Errorf("long steer: %d", code)
	}

	do(t, client, http.MethodPut, ts.URL+"/api/applicant", `{"city":"Austin","gender":"Female"}`)
	do(t, client, http.MethodPut, ts.URL+"/api/applicant/answers", `{"answers":[{"question":"Notice period?","answer":"Two weeks"}]}`)
	before := llm.calls.Load()
	code, body = do(t, client, http.MethodPost, ts.URL+"/api/ext/autofill", `{"company":"Acme","title":"SRE","fields":[{"id":"w1","label":"Notice period","kind":"text"},{"id":"w2","label":"Clearance","kind":"select","options":["Yes","No"]}]}`)
	if code != 200 || !strings.Contains(body, `"value":"Two weeks"`) || strings.Contains(body, "Maybe") || strings.Contains(body, `"zz"`) {
		t.Errorf("autofill: %d %s", code, body)
	}
	if !strings.Contains(llm.lastUser.Load().(string), "Two weeks") || !strings.Contains(llm.lastUser.Load().(string), "Austin") || strings.Contains(llm.lastUser.Load().(string), "Female") {
		t.Errorf("autofill prompt must carry details and earlier answers but no self-identification: %s", llm.lastUser.Load())
	}
	if llm.calls.Load() != before+1 {
		t.Errorf("one call per page, got %d", llm.calls.Load()-before)
	}
	for _, bad := range []string{`{"fields":[]}`, `{"fields":[{"id":"","label":"x"}]}`} {
		if code, _ := do(t, client, http.MethodPost, ts.URL+"/api/ext/autofill", bad); code != 400 {
			t.Errorf("%s: want 400, got %d", bad, code)
		}
	}

	_, body = do(t, client, http.MethodPost, ts.URL+"/api/applicant/suggest", "")
	if !strings.Contains(body, `"school":"UT Austin"`) || !strings.Contains(body, `"phone":"(512) 555-0142"`) {
		t.Errorf("suggest (phone must come from the resume): %s", body)
	}
}

func TestPasswordChangeDisconnectsExtensions(t *testing.T) {
	ts, pool := testServer(t, nil)
	ext, email := connectExtension(t, ts.URL)
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM users WHERE email = $1`, email) })
	var uid int64
	pool.QueryRow(context.Background(), `SELECT id FROM users WHERE email = $1`, email).Scan(&uid)
	hash, err := auth.HashPassword("a brand new password")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPassword(context.Background(), pool, uid, hash, ""); err != nil {
		t.Fatal(err)
	}
	if code, _ := do(t, ext, http.MethodGet, ts.URL+"/api/auth/me", ""); code != 401 {
		t.Errorf("extension still connected after a password change: %d", code)
	}
}

func TestApplicantAnswersBank(t *testing.T) {
	ts, _ := testServer(t, nil)
	url := ts.URL + "/api/applicant/answers"
	if code, body := do(t, client, http.MethodGet, url, ""); code != 200 || !strings.Contains(body, `"answers":[]`) {
		t.Fatalf("empty bank: %d %s", code, body)
	}
	for _, bad := range []string{`{"answers":[]}`, `{"answers":[{"question":"","answer":"x"}]}`, `{"answers":[{"question":"Q?","answer":"x","kind":"essay"}]}`} {
		if code, _ := do(t, client, http.MethodPut, url, bad); code != 400 {
			t.Errorf("%s: want 400, got %d", bad, code)
		}
	}
	code, body := do(t, client, http.MethodPut, url, `{"answers":[{"question":"What is your notice period? *","answer":"Two weeks","company":"Acme"},{"question":"Why Acme?","answer":"Robots.","kind":"long"}]}`)
	if code != 200 || strings.Count(body, `"id":`) != 2 {
		t.Fatalf("save: %d %s", code, body)
	}
	// The same question, phrased with other punctuation, replaces the answer.
	_, body = do(t, client, http.MethodPut, url, `{"answers":[{"question":"what is your notice period","answer":"One month"}]}`)
	if strings.Count(body, `"id":`) != 2 || !strings.Contains(body, "One month") || strings.Contains(body, "Two weeks") || !strings.Contains(body, `"uses":2`) {
		t.Errorf("same question should replace: %s", body)
	}
	var got struct {
		Answers []store.SavedAnswer `json:"answers"`
	}
	json.Unmarshal([]byte(body), &got)
	id := got.Answers[0].ID
	if code, body := do(t, client, http.MethodPatch, fmt.Sprintf("%s/%d", url, id), `{"answer":"Three weeks"}`); code != 200 || !strings.Contains(body, "Three weeks") {
		t.Errorf("edit: %d %s", code, body)
	}
	if code, _ := do(t, client, http.MethodPatch, url+"/999999999", `{"answer":"x"}`); code != 404 {
		t.Errorf("edit unknown: want 404, got %d", code)
	}
	if code, _ := do(t, client, http.MethodDelete, fmt.Sprintf("%s/%d", url, id), ""); code != 204 {
		t.Errorf("delete one: %d", code)
	}
	if code, _ := do(t, client, http.MethodDelete, fmt.Sprintf("%s/%d", url, id), ""); code != 404 {
		t.Errorf("delete twice: want 404, got %d", code)
	}
	if code, _ := do(t, client, http.MethodDelete, url, ""); code != 204 {
		t.Errorf("delete all: %d", code)
	}
	if _, body := do(t, client, http.MethodGet, url, ""); !strings.Contains(body, `"answers":[]`) {
		t.Errorf("bank not emptied: %s", body)
	}
}

func TestAutofillRuns(t *testing.T) {
	ts, _ := testServer(t, nil)
	if code, _ := do(t, client, http.MethodPost, ts.URL+"/api/ext/autofill-runs", `{"host":"a b/c"}`); code != 400 {
		t.Errorf("bad host: want 400, got %d", code)
	}
	code, body := do(t, client, http.MethodPost, ts.URL+"/api/ext/autofill-runs", fmt.Sprintf(`{"job_id":%d,"host":"Boards.Greenhouse.io","filled":12,"suggested":3}`, testJobIDs[0]))
	if code != 201 || !strings.Contains(body, `"host":"boards.greenhouse.io"`) || !strings.Contains(body, fmt.Sprintf(`"job_id":%d`, testJobIDs[0])) {
		t.Fatalf("start: %d %s", code, body)
	}
	var run store.AutofillRun
	json.Unmarshal([]byte(body), &run)
	code, body = do(t, client, http.MethodPost, fmt.Sprintf("%s/api/ext/autofill-runs/%d/pages", ts.URL, run.ID), `{"filled":5}`)
	if code != 200 || !strings.Contains(body, `"pages":2`) || !strings.Contains(body, `"filled":17`) {
		t.Errorf("next page: %d %s", code, body)
	}
	if code, _ := do(t, client, http.MethodPost, ts.URL+"/api/ext/autofill-runs/999999999/pages", `{}`); code != 404 {
		t.Errorf("unknown run: want 404, got %d", code)
	}
	// A job that is not the user's is dropped from the run.
	code, body = do(t, client, http.MethodPost, ts.URL+"/api/ext/autofill-runs", `{"job_id":999999999,"host":"x.example"}`)
	if code != 201 || !strings.Contains(body, `"job_id":null`) {
		t.Errorf("foreign job: %d %s", code, body)
	}
	_, body = do(t, client, http.MethodGet, fmt.Sprintf("%s/api/autofill-runs?job_id=%d", ts.URL, testJobIDs[0]), "")
	if strings.Count(body, `"id":`) != 1 {
		t.Errorf("runs for the job: %s", body)
	}
	if _, body = do(t, client, http.MethodGet, ts.URL+"/api/autofill-runs", ""); strings.Count(body, `"id":`) != 2 {
		t.Errorf("all runs: %s", body)
	}
	if code, _ := do(t, client, http.MethodGet, ts.URL+"/api/autofill-runs?job_id=x", ""); code != 400 {
		t.Errorf("bad job id: want 400, got %d", code)
	}
	// The job shows as autofilled, and joined the tracker.
	_, body = do(t, client, http.MethodGet, fmt.Sprintf("%s/api/jobs/%d", ts.URL, testJobIDs[0]), "")
	if strings.Contains(body, `"autofilled_at":null`) || !strings.Contains(body, `"autofilled_at":"`) || !strings.Contains(body, `"application_status":"`) {
		t.Errorf("job after autofill: %s", body)
	}
}
