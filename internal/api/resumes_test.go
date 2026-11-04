package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"wera/internal/config"
	"wera/internal/resume"
	"wera/internal/scoring"
	"wera/internal/store"
)

func needTypst(t *testing.T) {
	t.Helper()
	if !resume.NewRenderer(os.Getenv("TYPST_BIN")).Available() {
		if os.Getenv("CI") != "" {
			t.Fatal("typst must be installed in CI")
		}
		t.Skip("typst not installed")
	}
}

func importSample(t *testing.T, base string, replace bool) (int, map[string]any) {
	t.Helper()
	tex, err := os.ReadFile("../resume/testdata/sample.tex")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"source": "tex", "tex": string(tex), "replace": replace})
	code, body := do(t, client, http.MethodPost, base+"/api/resumes/import", string(b))
	var out map[string]any
	json.Unmarshal([]byte(body), &out)
	return code, out
}

func TestResumeLifecycle(t *testing.T) {
	needTypst(t)
	ts, _ := testServer(t, nil)
	code, doc := importSample(t, ts.URL, false)
	if code != 201 || doc["data"].(map[string]any)["name"] != "Ada Lovelace" || doc["fit"].(map[string]any)["one_page"] != true {
		t.Fatalf("import: %d %v", code, doc)
	}
	id := int64(doc["id"].(float64))
	if code, _ := importSample(t, ts.URL, false); code != 409 {
		t.Errorf("second base resume: want 409, got %d", code)
	}
	if code, again := importSample(t, ts.URL, true); code != 201 || int64(again["id"].(float64)) != id {
		t.Errorf("replace keeps the same resume: %d", code)
	}

	url := fmt.Sprintf("%s/api/resumes/%d", ts.URL, id)
	data := doc["data"].(map[string]any)
	data["name"] = "Ada King"
	b, _ := json.Marshal(map[string]any{"data": data, "layout": map[string]any{"font_size": 9, "spacing": 1, "margin": 0.5}})
	code, body := do(t, client, http.MethodPut, url, string(b))
	if code != 200 || !strings.Contains(body, `"name":"Ada King"`) || !strings.Contains(body, `"font_size":10`) || !strings.Contains(body, `"fit":null`) {
		t.Errorf("save (font clamped to 10pt, fit cleared): %d %.300s", code, body)
	}
	data["links"] = []map[string]string{{"label": "x", "url": "javascript:alert(1)"}}
	b, _ = json.Marshal(map[string]any{"data": data})
	if code, _ := do(t, client, http.MethodPut, url, string(b)); code != 400 {
		t.Errorf("unsafe link: want 400, got %d", code)
	}

	data["links"] = []map[string]string{{"label": "github.com/ada", "url": "https://github.com/ada"}}

	resp, err := client.Get(url + "/pdf?download=1")
	if err != nil {
		t.Fatal(err)
	}
	pdf, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.HasPrefix(string(pdf), "%PDF") || resp.Header.Get("Content-Disposition") != `attachment; filename="Ada_King_Resume.pdf"` {
		t.Errorf("pdf: %q %s", resp.Header.Get("Content-Disposition"), pdf[:min(10, len(pdf))])
	}
	if code, tex := do(t, client, http.MethodGet, url+"/tex", ""); code != 200 || !strings.Contains(tex, `\resumeSubheading`) || !strings.Contains(tex, "Ada King") {
		t.Errorf("tex: %d", code)
	}

	b, _ = json.Marshal(map[string]any{"data": doc["data"], "layout": map[string]any{}})
	code, body = do(t, client, http.MethodPost, ts.URL+"/api/resumes/preview", string(b))
	var prev struct {
		Pages   []string
		Measure struct{ Pages int }
	}
	json.Unmarshal([]byte(body), &prev)
	if code != 200 || len(prev.Pages) != 1 || !strings.HasPrefix(prev.Pages[0], "<svg") || prev.Measure.Pages != 1 {
		t.Errorf("preview: %d %.200s", code, body)
	}
	if code, body := do(t, client, http.MethodPost, url+"/fit", ""); code != 200 || !strings.Contains(body, `"one_page":true`) {
		t.Errorf("fit: %d %.200s", code, body)
	}
	if _, body := do(t, client, http.MethodGet, ts.URL+"/api/resumes", ""); !strings.Contains(body, fmt.Sprintf(`"id":%d`, id)) {
		t.Errorf("list: %s", body)
	}

	// Keywords for a job: no extracted skills yet, so common terms from the posting.
	_, body = do(t, client, http.MethodGet, fmt.Sprintf("%s/api/jobs/%d/keywords", ts.URL, testJobIDs[0]), "")
	if !strings.Contains(body, `"matched":["go","kubernetes"]`) {
		t.Errorf("keywords: %s", body)
	}

	// Another user cannot see it.
	ts2, _ := testServer(t, nil)
	if code, _ := do(t, client, http.MethodGet, fmt.Sprintf("%s/api/resumes/%d", ts2.URL, id), ""); code != 404 {
		t.Errorf("another user's resume: want 404, got %d", code)
	}
}

func TestTailorResumeForAJob(t *testing.T) {
	needTypst(t)
	ts, _ := llmServer(t)
	if code, _ := do(t, client, http.MethodPost, fmt.Sprintf("%s/api/jobs/%d/resume", ts.URL, testJobIDs[0]), ""); code != 400 {
		t.Errorf("tailoring without a resume: want 400, got %d", code)
	}
	if code, _ := importSample(t, ts.URL, false); code != 201 {
		t.Fatal("import failed")
	}
	// The job has a score: the tailored resume reports it, and what it becomes.
	ctx, pool := context.Background(), testPool(t)
	if err := store.SaveProfile(ctx, pool, testUserID, "Backend engineer who writes Go.", "tailor-test", config.Preferences{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveFacts(ctx, pool, scoring.FactsResult{JobID: testJobIDs[0], Facts: &scoring.Facts{SkillsRequired: []string{"Go", "Kubernetes", "Terraform", "Kafka"}}}, "test"); err != nil {
		t.Fatal(err)
	}
	ratings := `{"ratings": {"role": 9, "seniority": 8, "skills": 6, "domain": 7, "location": 10, "blocker": false}, "reason": "close"}`
	if err := store.SaveAnalysis(ctx, pool, testUserID, testJobIDs[0], "test", "tailor-test", scoring.Outcome{
		JobID: testJobIDs[0], Stage: scoring.StageScored, Raw: ratings, Analysis: &scoring.Analysis{FitScore: 70, Verdict: "good"},
	}); err != nil {
		t.Fatal(err)
	}
	jobURL := fmt.Sprintf("%s/api/jobs/%d/resume", ts.URL, testJobIDs[0])
	code, body := do(t, client, http.MethodPost, jobURL, "")
	if code != 200 {
		t.Fatalf("tailor: %d %s", code, body)
	}
	var out struct {
		Resume struct {
			ID    int64    `json:"id"`
			JobID *int64   `json:"job_id"`
			Notes []string `json:"notes"`
			Data  struct {
				Sections []struct {
					Entries []struct {
						Bullets []struct{ Text string } `json:"bullets"`
					} `json:"entries"`
				} `json:"sections"`
			} `json:"data"`
			Coverage *struct{ Percent int }       `json:"coverage"`
			Fit      *struct{ Before, After int } `json:"fit_score"`
		} `json:"resume"`
	}
	json.Unmarshal([]byte(body), &out)
	r := out.Resume
	bullets := r.Data.Sections[2].Entries[0].Bullets
	if r.JobID == nil || *r.JobID != testJobIDs[0] || r.Coverage == nil {
		t.Fatalf("tailored copy: %+v", r)
	}
	// The tailored bullets show Kubernetes, which the profile did not.
	if r.Fit == nil || r.Fit.Before != 70 || r.Fit.After <= r.Fit.Before {
		t.Errorf("fit score before and after tailoring: %+v", r.Fit)
	}
	if bullets[0].Text != "Cut deploy time by 50% with GitHub Actions pipelines across 12 services." {
		t.Errorf("a rewrite inventing a number was applied: %q", bullets[0].Text)
	}
	if bullets[1].Text != "Ran Kubernetes on-call for 2,000 requests per second" {
		t.Errorf("a valid rewrite was not applied: %q", bullets[1].Text)
	}
	if !strings.Contains(strings.Join(r.Notes, " "), "Kept your original wording in 1 place") {
		t.Errorf("notes: %v", r.Notes)
	}
	if code, _ := do(t, client, http.MethodGet, jobURL, ""); code != 200 {
		t.Errorf("get tailored: %d", code)
	}
	// The base resume is untouched.
	_, list := do(t, client, http.MethodGet, ts.URL+"/api/resumes", "")
	if strings.Count(list, `"id":`) != 2 {
		t.Errorf("want base + tailored: %s", list)
	}
}

func TestImportResumeFromProfile(t *testing.T) {
	ts, _ := llmServer(t)
	pool := testPool(t)
	if code, _ := do(t, client, http.MethodPost, ts.URL+"/api/resumes/import", `{"source":"profile"}`); code != 400 {
		t.Errorf("no resume uploaded: want 400, got %d", code)
	}
	pool.Exec(context.Background(), `INSERT INTO profiles (user_id, resume_text) VALUES ($1, 'Test User\nSRE intern at Acme 2024.')
		ON CONFLICT (user_id) DO UPDATE SET resume_text = EXCLUDED.resume_text`, testUserID)
	code, body := do(t, client, http.MethodPost, ts.URL+"/api/resumes/import", `{"source":"profile"}`)
	if code != 201 || !strings.Contains(body, `"name":"Test User"`) {
		t.Errorf("import from profile: %d %.300s", code, body)
	}
	if code, _ := do(t, client, http.MethodPost, ts.URL+"/api/resumes/import", `{"source":"pdf"}`); code != 400 {
		t.Errorf("unknown source: want 400, got %d", code)
	}
}

// TestBackgroundResumeImport checks that uploading a resume builds the
// main resume in the background, so the Resume tab opens on it.
func TestBackgroundResumeImport(t *testing.T) {
	ts, _ := llmServer(t)
	pool := testPool(t)
	pool.Exec(context.Background(), `DELETE FROM resumes WHERE user_id = $1`, testUserID)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("text", "Test User\nSRE intern at Acme 2024. Kept Kubernetes clusters healthy and on call.")
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/profile/resume", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("upload: %v %v", err, resp)
	}
	resp.Body.Close()
	var list string
	for range 100 {
		if _, list = do(t, client, http.MethodGet, ts.URL+"/api/resumes", ""); strings.Contains(list, `"status":"done"`) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(list, `"status":"done"`) || !strings.Contains(list, `"title":"My resume"`) {
		t.Fatalf("background import: %s", list)
	}
	// Another upload leaves the existing resume alone.
	if ok, err := store.StartResumeImport(context.Background(), pool, testUserID); err != nil || !ok {
		t.Errorf("a finished import can be restarted: %v %v", ok, err)
	}
	if ok, _ := store.StartResumeImport(context.Background(), pool, testUserID); ok {
		t.Error("two imports must not run at once")
	}
}

func TestResumeTemplatesLaTeXAndReview(t *testing.T) {
	needTypst(t)
	ts, _ := testServer(t, nil)
	code, doc := importSample(t, ts.URL, true)
	if code != 201 {
		t.Fatalf("import: %d", code)
	}
	id := int64(doc["id"].(float64))
	data := doc["data"].(map[string]any)

	code, body := getBody(t, ts.URL+"/api/resumes/templates")
	if code != 200 || !strings.Contains(body, `"id":"parse"`) || !strings.Contains(body, `"id":"signal"`) {
		t.Fatalf("templates: %d %s", code, body)
	}

	// Switch to Signal with an accent: kept on save, used by the downloads.
	url := fmt.Sprintf("%s/api/resumes/%d", ts.URL, id)
	b, _ := json.Marshal(map[string]any{"data": data, "layout": map[string]any{"template": "signal", "accent": "#0F766E", "paper": "a4"}})
	if code, body := do(t, client, http.MethodPut, url, string(b)); code != 200 || !strings.Contains(body, `"template":"signal"`) || !strings.Contains(body, `"accent":"#0f766e"`) {
		t.Fatalf("save template: %d %.300s", code, body)
	}
	code, tex := getBody(t, url+"/tex")
	if code != 200 || !strings.Contains(tex, "% Signal:") || !strings.Contains(tex, "a4paper") || !strings.Contains(tex, "{0F766E}") {
		t.Errorf("tex: %d %.400s", code, tex)
	}
	code, txt := getBody(t, url+"/txt")
	if code != 200 || !strings.Contains(txt, "Ada Lovelace") || strings.Contains(txt, `\`) {
		t.Errorf("txt: %d %.200s", code, txt)
	}

	// LaTeX for unsaved edits, then edited LaTeX back into a resume.
	b, _ = json.Marshal(map[string]any{"data": data, "layout": map[string]any{"template": "ledger"}})
	code, body = do(t, client, http.MethodPost, ts.URL+"/api/resumes/tex", string(b))
	var texOut struct{ TeX, Preamble, Body string }
	json.Unmarshal([]byte(body), &texOut)
	if code != 200 || !strings.Contains(texOut.Preamble, "% Ledger:") || !strings.HasPrefix(texOut.Body, `\resumeHeader{Ada Lovelace}`) {
		t.Fatalf("tex of edits: %d preamble=%q", code, texOut.Preamble[:min(len(texOut.Preamble), 300)])
	}
	edited := strings.Replace(texOut.Body, `\resumeHeader{Ada Lovelace}`, `\resumeHeader{Ada King}`, 1)
	b, _ = json.Marshal(map[string]string{"tex": edited})
	code, body = do(t, client, http.MethodPost, ts.URL+"/api/resumes/parse-tex", string(b))
	if code != 200 || !strings.Contains(body, `"name":"Ada King"`) || !strings.Contains(body, `"warnings":[]`) {
		t.Errorf("parse-tex: %d %.300s", code, body)
	}
	b, _ = json.Marshal(map[string]string{"tex": `\section{Only}`})
	if code, _ := do(t, client, http.MethodPost, ts.URL+"/api/resumes/parse-tex", string(b)); code != 422 {
		t.Errorf("unreadable tex: want 422, got %d", code)
	}

	// Previews carry the review; thumbnails come in every template.
	b, _ = json.Marshal(map[string]any{"data": data, "layout": map[string]any{}})
	code, body = do(t, client, http.MethodPost, ts.URL+"/api/resumes/preview", string(b))
	if code != 200 || !strings.Contains(body, `"review":{"score":`) {
		t.Errorf("preview review: %d %.200s", code, body[:min(len(body), 200)])
	}
	code, body = do(t, client, http.MethodPost, ts.URL+"/api/resumes/thumbnails", string(b))
	var th struct{ Thumbnails map[string]string }
	json.Unmarshal([]byte(body), &th)
	if code != 200 || len(th.Thumbnails) != 4 || !strings.HasPrefix(strings.TrimSpace(th.Thumbnails["parse"]), "<svg") {
		t.Errorf("thumbnails: %d %d", code, len(th.Thumbnails))
	}
}

func TestSuggestBulletIsWithinTheBudget(t *testing.T) {
	ts, llm := llmServer(t)
	pool := testPool(t)
	if err := store.SetBudget(context.Background(), pool, testUserID, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.SetBudget(context.Background(), pool, testUserID, 5) })
	b, _ := json.Marshal(map[string]string{"text": "Responsible for deploys, which got 50% faster across 12 services"})
	code, body := do(t, client, http.MethodPost, ts.URL+"/api/resumes/suggest-bullet", string(b))
	if code != http.StatusPaymentRequired || llm.calls.Load() != 0 {
		t.Errorf("no budget left: want 402 and no AI call, got %d after %d calls: %s", code, llm.calls.Load(), body)
	}
}

func TestSuggestBulletWithoutAI(t *testing.T) {
	ts, _ := testServer(t, nil)
	code, _ := do(t, client, http.MethodPost, ts.URL+"/api/resumes/suggest-bullet", `{"text":"Ran the deploys"}`)
	if code != http.StatusServiceUnavailable {
		t.Errorf("want 503, got %d", code)
	}
}

func TestSuggestBullet(t *testing.T) {
	ts, _ := llmServer(t)
	b, _ := json.Marshal(map[string]string{"text": "Responsible for deploys, which got 50% faster across 12 services", "role": "SRE at Engines Inc"})
	code, body := do(t, client, http.MethodPost, ts.URL+"/api/resumes/suggest-bullet", string(b))
	var out struct{ Suggestions []string }
	json.Unmarshal([]byte(body), &out)
	if code != 200 || len(out.Suggestions) != 2 {
		t.Fatalf("suggestions: %d %s", code, body)
	}
	if out.Suggestions[0] != "Cut deploy time by 50% across 12 services, with GitHub Actions" || !strings.Contains(out.Suggestions[1], "[N]") {
		t.Errorf("cleaning or placeholders wrong: %q", out.Suggestions)
	}
}
