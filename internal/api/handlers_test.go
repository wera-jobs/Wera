package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	pngenc "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"wera/internal/metrics"
	"wera/internal/store"
)

func TestPutApplicationPersists(t *testing.T) {
	ts, pool := testServer(t, nil)

	jobs, err := store.ListJobs(context.Background(), pool, testUserID, store.JobQuery{Limit: 1})
	if err != nil || len(jobs) == 0 {
		t.Skipf("no jobs available (err=%v)", err)
	}
	id := jobs[0].ID
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM applications WHERE job_id = $1 AND user_id = $2`, id, testUserID)
	})

	put := func(payload string) {
		req, err := http.NewRequest(http.MethodPut, jobURL(ts, id, "/application"),
			strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("PUT %s: %d %s", payload, resp.StatusCode, body)
		}
	}

	// Invalid status is rejected.
	req, _ := http.NewRequest(http.MethodPut, jobURL(ts, id, "/application"),
		strings.NewReader(`{"status":"nonsense"}`))
	r, _ := client.Do(req)
	r.Body.Close()
	if r.StatusCode != 400 {
		t.Errorf("invalid status: want 400, got %d", r.StatusCode)
	}

	put(`{"status":"saved","notes":"api test"}`)
	put(`{"status":"applied"}`) // a status alone (the extension) keeps the notes
	var notes string
	_ = pool.QueryRow(context.Background(), `SELECT notes FROM applications WHERE job_id = $1 AND user_id = $2`, id, testUserID).Scan(&notes)
	if notes != "api test" {
		t.Errorf("notes lost on a status-only update: %q", notes)
	}

	var appStatus string
	var appliedAt *string
	err = pool.QueryRow(context.Background(),
		`SELECT status, applied_at::text FROM applications WHERE job_id = $1 AND user_id = $2`, id, testUserID).
		Scan(&appStatus, &appliedAt)
	if err != nil {
		t.Fatalf("application row missing: %v", err)
	}
	if appStatus != "applied" {
		t.Errorf("status: got %q, want applied", appStatus)
	}
	if appliedAt == nil || *appliedAt == "" {
		t.Error("applied_at not set on first 'applied'")
	}
}

func TestClearApplicationUndoesAStatus(t *testing.T) {
	ts, pool := testServer(t, nil)
	id := testJobIDs[1]
	url := fmt.Sprintf("%s/api/jobs/%d/application", ts.URL, id)
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM applications WHERE job_id = $1 AND user_id = $2`, id, testUserID)
	})
	if code, body := do(t, client, http.MethodPut, url, `{"status":"not_interested"}`); code != 200 {
		t.Fatalf("set: %d %s", code, body)
	}
	code, body := do(t, client, http.MethodDelete, url, "")
	if code != 200 || !strings.Contains(body, `"application_status":null`) {
		t.Fatalf("clear: %d %s", code, body)
	}
	var n int
	pool.QueryRow(context.Background(), `SELECT count(*) FROM applications WHERE job_id = $1 AND user_id = $2`, id, testUserID).Scan(&n)
	if n != 0 {
		t.Errorf("the application row is still there")
	}
}

func TestTrackedJobStaysWhenThePostingCloses(t *testing.T) {
	ts, pool := testServer(t, nil)
	ctx := context.Background()
	id := testJobIDs[0]
	url := fmt.Sprintf("%s/api/jobs/%d/application", ts.URL, id)
	if code, body := do(t, client, http.MethodPut, url, `{"status":"applied"}`); code != 200 {
		t.Fatalf("apply: %d %s", code, body)
	}
	// The company takes the posting down, as most do within weeks.
	if _, err := pool.Exec(ctx, `UPDATE jobs SET closed_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `UPDATE jobs SET closed_at = NULL WHERE id = $1`, id) })

	code, body := getBody(t, ts.URL+"/api/jobs?status=applied")
	want := fmt.Sprintf(`"id":%d,`, id)
	if code != 200 || !strings.Contains(body, want) {
		t.Fatalf("the tracker lost an application when its posting closed: %d %s", code, body)
	}
	if strings.Contains(body, `"closed_at":null`) || !strings.Contains(body, `"closed_at":"`) {
		t.Errorf("a closed posting should say so: %s", body)
	}
	// Browsing without a status still shows open jobs only.
	if code, body := getBody(t, ts.URL+"/api/jobs"); code != 200 || strings.Contains(body, want) {
		t.Errorf("a closed job in the open list: %d %s", code, body)
	}
}

func TestSummaryEndpoints(t *testing.T) {
	ts, _ := testServer(t, nil)
	for _, path := range []string{
		"/api/today", "/api/stats", "/api/runs?limit=5", "/api/companies",
		"/api/admin/usage", "/api/usage/me", "/api/today?limit=5", "/api/excluded?reason=title:senior", "/api/industries",
	} {
		code, body := getBody(t, ts.URL+path)
		if code != 200 {
			t.Errorf("GET %s: want 200, got %d (%s)", path, code, body)
		}
	}
}

func TestMetricsAndCORS(t *testing.T) {
	reg := metrics.New()
	reg.JobsNewTotal.Add(3)
	reg.FetchTotal.WithLabelValues("TestCo", "ok").Inc()
	reg.LLMRequests.WithLabelValues("test-model", "scored").Inc()
	reg.LLMTokens.WithLabelValues("prompt").Add(100)
	ts, _ := testServer(t, reg)

	code, body := getBody(t, ts.URL+"/metrics")
	if code != 200 {
		t.Errorf("/metrics: %d", code)
	}
	for _, want := range []string{
		"wera_jobs_new_total 3",
		"wera_fetch_total{company=\"TestCo\",result=\"ok\"} 1",
		"wera_llm_requests_total{model=\"test-model\",result=\"scored\"} 1",
		"wera_llm_tokens_total{type=\"prompt\"} 100",
		"wera_llm_cost_usd_total",
		"wera_run_last_success_timestamp",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}

	// CORS: allowed origin reflected, others not.
	req, _ := http.NewRequest(http.MethodOptions, ts.URL+"/api/jobs", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "GET")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 || resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Errorf("preflight: status=%d allow-origin=%q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}

	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
	req2.Header.Set("Origin", "http://evil.example")
	resp2, _ := client.Do(req2)
	resp2.Body.Close()
	if resp2.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("disallowed origin got CORS headers")
	}
}

func TestAuthFlow(t *testing.T) {
	ts, pool := testServer(t, nil)
	anon := &http.Client{}

	post := func(c *http.Client, path, body string) *http.Response {
		t.Helper()
		resp, err := c.Post(ts.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	// Protected endpoints need a session.
	resp, err := anon.Get(ts.URL + "/api/jobs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("anonymous /api/jobs: want 401, got %d", resp.StatusCode)
	}

	email := fmt.Sprintf("signup-%d@example.com", time.Now().UnixNano())
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM users WHERE email = $1`, email) })

	if r := post(anon, "/api/auth/signup", `{"email":"`+email+`","password":"short"}`); r.StatusCode != 400 {
		t.Errorf("short password: want 400, got %d", r.StatusCode)
	}
	r := post(anon, "/api/auth/signup", `{"email":"`+email+`","password":"long enough pw","name":"T"}`)
	if r.StatusCode != 201 {
		t.Fatalf("signup: want 201, got %d", r.StatusCode)
	}
	var cookie string
	for _, c := range r.Cookies() {
		if c.Name == sessionCookie {
			cookie = c.Value
			if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
				t.Errorf("session cookie flags: %+v", c)
			}
		}
	}
	if cookie == "" {
		t.Fatal("signup set no session cookie")
	}
	if r := post(anon, "/api/auth/signup", `{"email":"`+strings.ToUpper(email)+`","password":"long enough pw"}`); r.StatusCode != 409 {
		t.Errorf("duplicate email: want 409, got %d", r.StatusCode)
	}

	user := &http.Client{Transport: cookieTransport{cookie}}
	resp, err = user.Get(ts.URL + "/api/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), email) {
		t.Errorf("/api/auth/me: %d %s", resp.StatusCode, body)
	}
	if r := post(user, "/api/admin/runs", ``); r.StatusCode != 403 {
		t.Errorf("non-admin POST /api/admin/runs: want 403, got %d", r.StatusCode)
	}

	if r := post(anon, "/api/auth/login", `{"email":"`+email+`","password":"wrong password"}`); r.StatusCode != 401 {
		t.Errorf("wrong password: want 401, got %d", r.StatusCode)
	}
	if r := post(anon, "/api/auth/login", `{"email":"`+email+`","password":"long enough pw"}`); r.StatusCode != 200 {
		t.Errorf("login: want 200, got %d", r.StatusCode)
	}

	// Logout ends the session.
	if r := post(user, "/api/auth/logout", ``); r.StatusCode != 204 {
		t.Errorf("logout: want 204, got %d", r.StatusCode)
	}
	resp, _ = user.Get(ts.URL + "/api/auth/me")
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("after logout: want 401, got %d", resp.StatusCode)
	}

	// Cross-origin writes are refused.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/auth/login", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://evil.example")
	resp, err = anon.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("cross-origin POST: want 403, got %d", resp.StatusCode)
	}
}

func TestProfileFlow(t *testing.T) {
	ts, pool := testServer(t, nil)

	code, body := getBody(t, ts.URL+"/api/profile/options")
	if code != 200 || !strings.Contains(body, `"data_science"`) || !strings.Contains(body, `"aerospace"`) ||
		!strings.Contains(body, `"registered_nursing"`) || !strings.Contains(body, `"field":"healthcare"`) ||
		!strings.Contains(body, `"fields":[{"id":"tech"`) {
		t.Fatalf("/api/profile/options: %d %.200s", code, body)
	}
	var opts struct {
		RoleFamilies []struct{ Label string } `json:"role_families"`
		Industries   []struct{ Label string } `json:"industries"`
	}
	json.Unmarshal([]byte(body), &opts)
	for name, list := range map[string][]struct{ Label string }{"role families": opts.RoleFamilies, "industries": opts.Industries} {
		for i := 1; i < len(list); i++ {
			if strings.ToLower(list[i-1].Label) > strings.ToLower(list[i].Label) {
				t.Fatalf("%s not A to Z: %q before %q", name, list[i-1].Label, list[i].Label)
			}
		}
	}
	code, body = getBody(t, ts.URL+"/api/profile")
	if code != 200 || !strings.Contains(body, `"ready":false`) {
		t.Fatalf("empty profile: %d %s", code, body)
	}

	// Upload the sample resume PDF.
	pdf, err := os.ReadFile("../profile/testdata/sample-resume.pdf")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "resume.pdf")
	fw.Write(pdf)
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/profile/resume", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "Example Airlines") {
		t.Fatalf("resume upload: %d %s", resp.StatusCode, b)
	}
	// The upload starts a profile with no preferences yet; its lists must
	// still be arrays (the setup wizard reads their length on resume).
	code, body = getBody(t, ts.URL+"/api/profile")
	if code != 200 || !strings.Contains(body, `"role_families":[]`) || !strings.Contains(body, `"levels":[]`) {
		t.Fatalf("profile after upload: %d %.300s", code, body)
	}

	// Without Coral settings the AI endpoints answer 503, never a crash.
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/profile/suggest", nil)
	if resp, err := client.Do(req); err != nil || resp.StatusCode != 503 {
		t.Errorf("suggest without Coral: want 503, got %v %v", resp, err)
	}

	put := func(payload string) (int, string) {
		req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/profile", strings.NewReader(payload))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(b)
	}
	if code, body := put(`{"markdown":"# P","preferences":{"role_families":["astrology"],"levels":["mid"]}}`); code != 400 {
		t.Errorf("unknown family: want 400, got %d %s", code, body)
	}
	// Setup saves the preferences before the text exists and filters in
	// the background.
	putPrefs := func(payload string) int {
		req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/profile/preferences", strings.NewReader(payload))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	filters := prefilterCalls.Load()
	if code := putPrefs(`{"preferences":{"role_families":["data_science"],"levels":["mid"]}}`); code != 204 {
		t.Fatalf("setup preferences: %d", code)
	}
	if code, body := getBody(t, ts.URL+"/api/profile"); code != 200 || !strings.Contains(body, `"role_families":["data_science"]`) || !strings.Contains(body, `"ready":false`) {
		t.Errorf("profile after setup preferences: %d %.300s", code, body)
	}
	deadline := time.Now().Add(2 * time.Second)
	for prefilterCalls.Load() == filters && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if prefilterCalls.Load() == filters {
		t.Error("setup preferences did not start the prefilter")
	}
	if code := putPrefs(`{"preferences":{"role_families":["astrology"],"levels":["mid"]}}`); code != 400 {
		t.Errorf("unknown family in setup preferences: want 400, got %d", code)
	}

	before := matchCalls.Load()
	code, body = put(`{"markdown":"# Candidate Profile\nData scientist.","preferences":{"role_families":["data_science"],` +
		`"levels":["entry","mid"],"max_years_required":5,"us_only":true},"answers":{"industries":["aerospace"],"work_modes":["remote"]}}`)
	if code != 200 || !strings.Contains(body, `"ready":true`) || !strings.Contains(body, `"resume_chars"`) {
		t.Fatalf("save profile: %d %s", code, body)
	}
	if !strings.Contains(body, `"aerospace"`) {
		t.Errorf("answers not stored: %s", body)
	}
	deadline = time.Now().Add(2 * time.Second)
	for matchCalls.Load() == before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if matchCalls.Load() == before {
		t.Error("saving a profile did not start matching")
	}

	// Text that differs only in spacing keeps its hash, so no job is
	// scored again; a real edit gets a new one.
	hash := func() string {
		var h string
		pool.QueryRow(context.Background(), `SELECT profile_hash FROM profiles WHERE user_id = $1`, testUserID).Scan(&h)
		return h
	}
	saved := hash()
	if code := putPrefs(`{"preferences":{"role_families":["data_science"],"levels":["mid"]}}`); code != 409 {
		t.Errorf("setup preferences on a set-up profile: want 409, got %d", code)
	}
	pool.Exec(context.Background(), `UPDATE profiles SET markdown = markdown || E'\n' WHERE user_id = $1`, testUserID)
	code, body = put(`{"markdown":"# Candidate Profile   \nData scientist.\n","preferences":{"role_families":["data_science","data_engineering"],` +
		`"levels":["entry","mid"],"max_years_required":5,"us_only":true}}`)
	if code != 200 || hash() != saved {
		t.Errorf("spacing changed the profile hash: %d %.200s", code, body)
	}
	if code, _ := put(`{"markdown":"# Candidate Profile\nData engineer.","preferences":{"role_families":["data_science"],"levels":["mid"]}}`); code != 200 || hash() == saved {
		t.Errorf("an edit kept the old profile hash: %d", code)
	}
}

func do(t *testing.T, c *http.Client, method, url, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(b)
}

func TestSettingsAndHiddenCompanies(t *testing.T) {
	ts, pool := testServer(t, nil)
	code, body := do(t, client, http.MethodGet, ts.URL+"/api/settings", "")
	if code != 200 || !strings.Contains(body, `"theme":"system"`) || !strings.Contains(body, `"hidden_companies":[]`) {
		t.Fatalf("defaults: %d %s", code, body)
	}
	if code, _ := do(t, client, http.MethodPut, ts.URL+"/api/settings", `{"theme":"neon","default_sort":"score","notifications":{"frequency":"daily","min_score":80}}`); code != 400 {
		t.Errorf("bad theme: want 400, got %d", code)
	}
	code, body = do(t, client, http.MethodPut, ts.URL+"/api/settings",
		`{"theme":"dark","default_sort":"newest","notifications":{"email_digest":false,"frequency":"weekly","strong_matches":true,"min_score":85}}`)
	if code != 200 || !strings.Contains(body, `"theme":"dark"`) || !strings.Contains(body, `"frequency":"weekly"`) {
		t.Fatalf("save: %d %s", code, body)
	}

	// Hiding a company removes its jobs from the user's lists.
	var jobID, companyID int64
	if err := pool.QueryRow(context.Background(), `
		SELECT j.id, j.company_id FROM user_jobs uj JOIN jobs j ON j.id = uj.job_id
		WHERE uj.user_id = $1 LIMIT 1`, testUserID).Scan(&jobID, &companyID); err != nil {
		t.Fatalf("seeded match: %v", err)
	}
	jobURL := fmt.Sprintf("%s/api/jobs/%d", ts.URL, jobID)
	if code, _ := do(t, client, http.MethodGet, jobURL, ""); code != 200 {
		t.Fatalf("job visible before hiding: %d", code)
	}
	if code, _ := do(t, client, http.MethodPut, fmt.Sprintf("%s/api/companies/%d/hidden", ts.URL, companyID), ""); code != 204 {
		t.Fatalf("hide: %d", code)
	}
	if code, _ := do(t, client, http.MethodGet, jobURL, ""); code != 404 {
		t.Errorf("hidden company's job still visible: %d", code)
	}
	if _, body := do(t, client, http.MethodGet, ts.URL+"/api/settings", ""); !strings.Contains(body, fmt.Sprintf(`"id":%d`, companyID)) {
		t.Errorf("hidden company not listed: %s", body)
	}
	do(t, client, http.MethodDelete, fmt.Sprintf("%s/api/companies/%d/hidden", ts.URL, companyID), "")
	if code, _ := do(t, client, http.MethodGet, jobURL, ""); code != 200 {
		t.Errorf("unhidden job not back: %d", code)
	}
}

func TestDeleteAccount(t *testing.T) {
	ts, pool := testServer(t, nil)
	anon := &http.Client{}
	email := fmt.Sprintf("delete-%d@example.com", time.Now().UnixNano())
	resp, err := anon.Post(ts.URL+"/api/auth/signup", "application/json",
		strings.NewReader(`{"email":"`+email+`","password":"delete me please"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var cookie string
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			cookie = c.Value
		}
	}
	user := &http.Client{Transport: cookieTransport{cookie}}
	if code, _ := do(t, user, http.MethodDelete, ts.URL+"/api/auth/account", `{"password":"wrong"}`); code != 401 {
		t.Errorf("wrong password: want 401, got %d", code)
	}
	if code, _ := do(t, user, http.MethodDelete, ts.URL+"/api/auth/account", `{"password":"delete me please"}`); code != 204 {
		t.Fatalf("delete: want 204, got %d", code)
	}
	var n int
	pool.QueryRow(context.Background(), `SELECT count(*) FROM users WHERE email = $1`, email).Scan(&n)
	if n != 0 {
		t.Error("account still exists")
	}
	if code, _ := do(t, user, http.MethodGet, ts.URL+"/api/auth/me", ""); code != 401 {
		t.Errorf("session survived deletion: %d", code)
	}
}

func upload(t *testing.T, url, method, field, name string, data []byte) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile(field, name)
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest(method, url, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(b)
}

func TestAvatarAndResumeFile(t *testing.T) {
	ts, _ := testServer(t, nil)
	var png bytes.Buffer
	pngenc.Encode(&png, image.NewRGBA(image.Rect(0, 0, 40, 20)))
	if code, body := upload(t, ts.URL+"/api/profile/avatar", http.MethodPut, "file", "me.png", png.Bytes()); code != 200 || !strings.Contains(body, `"avatar_version":`) {
		t.Fatalf("avatar upload: %d %s", code, body)
	}
	resp, err := client.Get(ts.URL + "/api/profile/avatar")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("avatar get: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if code, _ := upload(t, ts.URL+"/api/profile/avatar", http.MethodPut, "file", "x.svg", []byte("<svg/>")); code != 422 {
		t.Errorf("svg avatar: want 422, got %d", code)
	}

	pdf, _ := os.ReadFile("../profile/testdata/sample-resume.pdf")
	if code, body := upload(t, ts.URL+"/api/profile/resume", http.MethodPost, "file", `..\\evil"name.pdf`, pdf); code != 200 {
		t.Fatalf("resume upload: %d %s", code, body)
	}
	resp, err = client.Get(ts.URL + "/api/profile/resume/file")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(got, pdf) || resp.Header.Get("Content-Type") != "application/pdf" {
		t.Errorf("resume file: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if cd := resp.Header.Get("Content-Disposition"); strings.ContainsAny(strings.TrimPrefix(cd, `inline; filename="`)[:len(cd)-len(`inline; filename="`)-1], `"\\/`) {
		t.Errorf("unsafe filename in %q", cd)
	}
}

func TestCoverLetterEdits(t *testing.T) {
	ts, pool := testServer(t, nil)
	var jobID int64
	if err := pool.QueryRow(context.Background(),
		`SELECT job_id FROM user_jobs WHERE user_id = $1 LIMIT 1`, testUserID).Scan(&jobID); err != nil {
		t.Fatalf("seeded match: %v", err)
	}
	url := fmt.Sprintf("%s/api/jobs/%d/cover-letter", ts.URL, jobID)
	if code, _ := do(t, client, http.MethodGet, url, ""); code != 404 {
		t.Errorf("no letter yet: want 404, got %d", code)
	}
	if code, _ := do(t, client, http.MethodPost, url, ""); code != 503 {
		t.Errorf("generation without Coral: want 503, got %d", code)
	}
	code, body := do(t, client, http.MethodPut, url, `{"body":"Dear Hiring Team,\n\nI built it — well.\n\nSincerely,\nT"}`)
	if code != 200 || !strings.Contains(body, `"edited":true`) || strings.Contains(body, "—") {
		t.Fatalf("save edit: %d %s", code, body)
	}
	if code, body := do(t, client, http.MethodGet, url, ""); code != 200 || !strings.Contains(body, "I built it, well.") {
		t.Errorf("get after edit: %d %s", code, body)
	}
	// As a file, for forms that want one attached (503 without typst).
	if code, body := do(t, client, http.MethodGet, url+"/pdf?download=1", ""); code != 503 && (code != 200 || !strings.HasPrefix(body, "%PDF")) {
		t.Errorf("letter pdf: %d %.40s", code, body)
	}
	if code, _ := do(t, client, http.MethodGet, fmt.Sprintf("%s/api/jobs/999999999/cover-letter/pdf", ts.URL), ""); code != 404 && code != 503 {
		t.Errorf("letter pdf for someone else's job: want 404, got %d", code)
	}
	if code, _ := do(t, client, http.MethodPut, fmt.Sprintf("%s/api/jobs/999999999/cover-letter", ts.URL), `{"body":"x"}`); code != 404 {
		t.Errorf("letter for someone else's job: want 404, got %d", code)
	}
}

func TestStatsPerDayInUserTimeZone(t *testing.T) {
	ts, pool := testServer(t, nil)
	// One of the user's matches, first seen "today" in Tokyo.
	if _, err := pool.Exec(context.Background(), `UPDATE jobs SET first_seen_at = now() WHERE id = $1`, testJobIDs[0]); err != nil {
		t.Fatal(err)
	}
	for _, tz := range []string{"Asia/Tokyo", "America/Los_Angeles", "Not/AZone"} {
		_, body := getBody(t, ts.URL+"/api/stats?tz="+tz)
		var st struct {
			NewPerDay []struct {
				Day   string `json:"day"`
				Count int    `json:"count"`
			} `json:"new_per_day"`
		}
		if err := json.Unmarshal([]byte(body), &st); err != nil || len(st.NewPerDay) == 0 {
			t.Fatalf("%s: %v %s", tz, err, body)
		}
		zone := tz
		if tz == "Not/AZone" {
			zone = "UTC"
		}
		loc, _ := time.LoadLocation(zone)
		last := st.NewPerDay[len(st.NewPerDay)-1]
		if last.Day != time.Now().In(loc).Format("2006-01-02") || last.Count < 1 {
			t.Errorf("%s: last day %+v, want today in that zone with the new match", tz, last)
		}
		if len(st.NewPerDay) > 14 {
			t.Errorf("%s: %d days, want at most 14", tz, len(st.NewPerDay))
		}
	}
}
