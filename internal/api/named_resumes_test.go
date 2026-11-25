package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"wera/internal/store"
)

func TestNamedBaseResumes(t *testing.T) {
	ts, _ := llmServer(t)
	type doc struct {
		ID        int64  `json:"id"`
		Title     string `json:"title"`
		IsDefault bool   `json:"is_default"`
		BaseID    *int64 `json:"base_id"`
		JobID     *int64 `json:"job_id"`
		Data      struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	create := func(body string, want int) doc {
		t.Helper()
		code, out := do(t, client, http.MethodPost, ts.URL+"/api/resumes", body)
		var d doc
		if code != want || (want == 201 && json.Unmarshal([]byte(out), &d) != nil) {
			t.Fatalf("create %s: %d %s", body, code, out)
		}
		return d
	}
	backend := create(`{"title":"Backend SWE"}`, 201)
	if !backend.IsDefault || backend.Title != "Backend SWE" {
		t.Fatalf("the first base resume should be the default: %+v", backend)
	}
	// Two bullets, so the fake tailoring reply has bullets to rewrite.
	data := `{"name":"Ada Lovelace","sections":[{"kind":"entries","title":"Experience","entries":[{"heading":"Acme","subheading":"Engineer","bullets":[{"text":"Built the billing service in Go."},{"text":"Ran on-call for the payments team."}]}]}]}`
	if code, body := do(t, client, http.MethodPut, fmt.Sprintf("%s/api/resumes/%d", ts.URL, backend.ID), `{"data":`+data+`}`); code != 200 {
		t.Fatalf("edit: %d %s", code, body)
	}
	ml := create(fmt.Sprintf(`{"title":"ML Engineer","from_id":%d}`, backend.ID), 201)
	if ml.IsDefault || ml.Data.Name != "Ada Lovelace" {
		t.Fatalf("a copy is not the default and keeps the content: %+v", ml)
	}
	if c := create(fmt.Sprintf(`{"from_id":%d}`, backend.ID), 201); c.Title != "Backend SWE (copy)" {
		t.Errorf("copy name: %q", c.Title)
	}
	create(`{"from_id":99999999}`, 404)
	create(`{"title":"`+strings.Repeat("x", 121)+`"}`, 400)

	// Rename, then switch the default.
	mlURL := fmt.Sprintf("%s/api/resumes/%d", ts.URL, ml.ID)
	if code, body := do(t, client, http.MethodPatch, mlURL, `{"title":"  ML Platform  "}`); code != 200 || !strings.Contains(body, `"title":"ML Platform"`) {
		t.Errorf("rename: %d %s", code, body)
	}
	if code, _ := do(t, client, http.MethodPatch, mlURL, `{"title":""}`); code != 400 {
		t.Errorf("empty name: want 400, got %d", code)
	}
	if code, body := do(t, client, http.MethodPatch, mlURL, `{"is_default":true}`); code != 200 || !strings.Contains(body, `"is_default":true`) {
		t.Fatalf("set default: %d %s", code, body)
	}
	if code, _ := do(t, client, http.MethodPatch, mlURL, `{"is_default":false}`); code != 400 {
		t.Errorf("unsetting the only default: want 400, got %d", code)
	}
	ctx, pool := context.Background(), testPool(t)
	if base, err := store.BaseResume(ctx, pool, testUserID); err != nil || base.ID != ml.ID {
		t.Fatalf("default base: %+v %v", base, err)
	}
	_, list := do(t, client, http.MethodGet, ts.URL+"/api/resumes", "")
	var l struct {
		Resumes []doc `json:"resumes"`
	}
	json.Unmarshal([]byte(list), &l)
	if len(l.Resumes) != 3 || l.Resumes[0].ID != ml.ID || !l.Resumes[0].IsDefault || l.Resumes[1].IsDefault || l.Resumes[2].IsDefault {
		t.Errorf("list, default first: %s", list)
	}

	// Tailoring uses the default, or the base asked for.
	jobURL := fmt.Sprintf("%s/api/jobs/%d/resume", ts.URL, testJobIDs[0])
	code, body := do(t, client, http.MethodPost, jobURL, fmt.Sprintf(`{"base_resume_id":%d}`, backend.ID))
	var tailored struct {
		Resume doc `json:"resume"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &tailored) != nil || tailored.Resume.BaseID == nil || *tailored.Resume.BaseID != backend.ID || tailored.Resume.IsDefault {
		t.Fatalf("tailor from a chosen base: %d %s", code, body)
	}
	if code, body := do(t, client, http.MethodPost, jobURL, ""); code != 200 || !strings.Contains(body, fmt.Sprintf(`"base_id":%d`, ml.ID)) {
		t.Errorf("tailor from the default: %d %.300s", code, body)
	}
	for _, bad := range []int64{tailored.Resume.ID, 99999999} {
		if code, _ := do(t, client, http.MethodPost, jobURL, fmt.Sprintf(`{"base_resume_id":%d}`, bad)); code != 404 {
			t.Errorf("tailor from %d (not a base): want 404, got %d", bad, code)
		}
	}
	if code, _ := do(t, client, http.MethodPatch, fmt.Sprintf("%s/api/resumes/%d", ts.URL, tailored.Resume.ID), `{"is_default":true}`); code != 400 {
		t.Errorf("a tailored copy as the default: want 400, got %d", code)
	}

	// Deleting the default hands it to the most recently edited base.
	if code, _ := do(t, client, http.MethodDelete, mlURL, ""); code != 204 {
		t.Fatalf("delete default: %d", code)
	}
	base, err := store.BaseResume(ctx, pool, testUserID)
	if err != nil || base.ID == ml.ID {
		t.Fatalf("no new default after deleting it: %+v %v", base, err)
	}

	// The cap.
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM resumes WHERE user_id = $1 AND job_id IS NULL`, testUserID).Scan(&n)
	for ; n < store.MaxBaseResumes; n++ {
		create(`{"title":"More"}`, 201)
	}
	create(`{"title":"One too many"}`, 409)
}
