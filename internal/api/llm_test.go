package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"wera/internal/config"
	"wera/internal/moderation"
	"wera/internal/store"
	"wera/internal/testutil"
)

// fakeLLM stands in for the inference API: it recognizes each feature's
// system prompt and answers like a well-behaved model would.
type fakeLLM struct {
	calls    atomic.Int64
	short    atomic.Bool  // answer cover letters with a uselessly short reply
	lastUser atomic.Value // the last request's user message
}

func (f *fakeLLM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	var req struct {
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	body, _ := io.ReadAll(r.Body)
	json.Unmarshal(body, &req)
	system := req.Messages[0].Content
	if len(req.Messages) > 1 {
		f.lastUser.Store(req.Messages[1].Content)
	}
	var content string
	switch {
	case strings.HasPrefix(system, "You prepare a candidate for an interview"):
		qs := make([]string, 0, 7)
		for i, k := range []string{"role", "behavioral", "motivation", "company", "role", "behavioral"} {
			qs = append(qs, fmt.Sprintf(`{"question":"Question %d about the role?","kind":%q,"why":"The posting asks for it.","points":["Site reliability work from the profile"],"prepare":""}`, i+1, k))
		}
		// One point claims a number the candidate never wrote: it must not reach the user.
		qs = append(qs, `{"question":"How did you cut incidents?","kind":"behavioral","why":"Reliability.","points":["Cut incidents by 73% in a quarter"],"prepare":""}`)
		content = `{"role":"Keep services healthy.","they_value":["Go","on-call"],"intro":"I work on site reliability.","questions":[` + strings.Join(qs, ",") + `],"ask":["How is on-call shared?"]}`
	case strings.HasPrefix(system, "You coach a candidate's practice answer"):
		content = `{"strengths":["You named a real incident"],"improve":["Say what changed after the fix"],"outline":["Set the scene","Say what you did","Cut pages by 73%"]}`
	case strings.HasPrefix(system, "You write a cover letter") && f.short.Load():
		content = "Dear Hiring Team, hire me."
	case strings.HasPrefix(system, "You write a cover letter"):
		content = "Dear Hiring Team,\n\nI would love to help your platform team ship reliably — every day. " +
			strings.Repeat("Keeping services healthy is work I enjoy and want to keep doing well. ", 10) +
			"\n\nSincerely,\nTest User"
	case strings.HasPrefix(system, "You write a candidate profile"):
		content = "# Candidate Profile\n## Target roles\nSite reliability engineering."
	case strings.HasPrefix(system, "You read a resume and pull out contact"):
		content = `{"school":"UT Austin","degree":"B.S.","phone":"+1 000 000 0000"}`
	case strings.HasPrefix(system, "You read a resume"):
		content = `{"role_families":["sre","made_up"],"levels":["entry"],"years_experience":1,"current_title":"SRE Intern","locations":["Dallas, TX"],"target_roles":"SRE"}`
	case strings.HasPrefix(system, "You moderate"):
		if strings.Contains(req.Messages[1].Content, "fake references") {
			content = `{"allowed":false,"categories":["unethical"],"reason":"Advises faking references."}`
		} else {
			content = `{"allowed":true,"categories":[],"reason":""}`
		}
	case strings.HasPrefix(system, "You read the text of one web page"):
		content = `{"is_job_posting":true,"company":"Acme Robotics","title":"Site Reliability Engineer","location":"Austin, TX","work_mode":"onsite","industry":"robotics","description_start":"About the role: keep our robot fleet","description_end":"Kubernetes and Go every day."}`
	case strings.HasPrefix(system, "You draft the candidate's answer"):
		user := req.Messages[1].Content
		switch {
		// Breaks the rule first, then keeps it when asked to rework.
		case strings.Contains(user, "partnerships") && strings.Contains(user, "Rework this draft"):
			content = "I ran projects end to end at Acme, from scoping to launch."
		case strings.Contains(user, "partnerships"):
			content = "I have not worked in partnerships, but I ran projects end to end."
		case strings.Contains(user, "salary"):
			content = "NEEDS_INPUT: your expected salary"
		default:
			content = "I want to keep robots online — because reliability work is what I do best."
		}
	case strings.HasPrefix(system, "You draft one short email"):
		user := req.Messages[len(req.Messages)-1].Content
		switch {
		// Volunteers sponsorship first, then keeps the rule when asked to rework.
		case strings.Contains(user, "request to a contact for a referral"):
			content = "Subject: Referral for SRE\n\nHello Sam,\n\nCould you refer me for the Site Reliability Engineer role? I will need visa sponsorship, and I can send my resume and a two-line summary to make it easy. No worries if it's not a fit.\n\nThanks,\nTest User"
		case strings.Contains(user, "Rework this draft"):
			content = "Subject: Referral for SRE\n\nHello Sam,\n\nCould you refer me for the Site Reliability Engineer role? Keeping robots online is the work I do best, and I can send my resume and a two-line summary to make it easy. No worries if it's not a fit.\n\nThanks,\nTest User"
		default:
			content = "Subject: Following up on the SRE application\n\nHello,\n\nI applied for the Site Reliability Engineer role and remain very interested — keeping robots online is the work I do best. I have run on-call rotations and automated deploys, which matches what the posting describes. Could you share where things stand?\n\nThanks,\nTest User\nNEEDS_INPUT: the recruiter's name, if you have it"
		}
	case strings.HasPrefix(system, "You fill in one page of a job application form"):
		// A good answer, an option that is not offered, and an unknown field.
		content = `{"answers":[{"id":"w1","value":"Two weeks","confidence":"high","why":"earlier answer"},{"id":"w2","value":"Maybe"},{"id":"zz","value":"x"}]}`
	case strings.HasPrefix(system, "You rewrite one resume bullet"):
		// One valid, one with a placeholder, one with an invented number.
		content = `{"suggestions": ["Cut deploy time by 50% across 12 services — with GitHub Actions.", "Automated deploys for [N] services, halving release time", "Cut deploy time by 73% for 40 teams"]}`
	case strings.HasPrefix(system, "You tailor a resume to one job posting"):
		// Rewrite the first listed bullet (one invented number, which must be
		// rejected) and the second (valid).
		ids := regexp.MustCompile(`\[b:([0-9a-f]+)\]`).FindAllStringSubmatch(req.Messages[1].Content, -1)
		content = fmt.Sprintf(`{"bullets":{%q:"Cut costs by 87%% on Kubernetes",%q:"Ran Kubernetes on-call for 2,000 requests per second"},"notes":["Led with Kubernetes"]}`, ids[0][1], ids[1][1])
	case strings.HasPrefix(system, "You convert the text of a resume into JSON"):
		content = `{"name":"Test User","email":"t@example.com","sections":[{"kind":"entries","title":"Experience","entries":[{"heading":"Acme","subheading":"SRE Intern","bullets":[{"text":"SRE intern at Acme 2024."}]}]}]}`
	default:
		http.Error(w, "unexpected prompt", http.StatusBadRequest)
		return
	}
	reply, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": content}}},
		"usage":   map[string]any{"prompt_tokens": 500, "completion_tokens": 120},
	})
	w.Header().Set("Content-Type", "application/json")
	w.Write(reply)
}

// llmServer is testServer wired to a fake inference API, using the real
// moderation path.
func llmServer(t *testing.T) (*httptest.Server, *fakeLLM) {
	t.Helper()
	llm := &fakeLLM{}
	fake := httptest.NewServer(llm)
	t.Cleanup(fake.Close)
	ts, _ := testServerWith(t, nil, func(s *Server) {
		s.Env = &config.Env{
			AI: config.AI{APIKey: "cb_test", BaseURL: fake.URL,
				Model: "deepseek-v4.1-flash-fast", DeepModel: "deepseek-v4.1-flash-fast"},
			MaxCostPerRunUSD: 1, UserBudgetUSD: 5, MaxMonthlyCostUSD: 1e6,
		}
		s.Moderator = nil
	})
	return ts, llm
}

func TestCoverLetterGeneration(t *testing.T) {
	ts, llm := llmServer(t)
	testutil.Profile(t, testPool(t), testUserID, "# Candidate Profile\n## Target roles\nSRE.", config.Preferences{
		RoleFamilies: []string{"sre"}, Levels: []string{"entry"}, MaxYearsRequired: 3})
	url := fmt.Sprintf("%s/api/jobs/%d/cover-letter", ts.URL, testJobIDs[0])
	code, body := do(t, client, http.MethodPost, url, "")
	if code != 200 || !strings.Contains(body, "Dear Hiring Team") {
		t.Fatalf("generate: %d %s", code, body)
	}
	if strings.Contains(body, "—") {
		t.Error("em dash survived cleaning")
	}
	if code, body := do(t, client, http.MethodGet, url, ""); code != 200 || !strings.Contains(body, `"edited":false`) {
		t.Errorf("stored letter: %d %s", code, body)
	}
	if llm.calls.Load() != 1 {
		t.Errorf("LLM calls: %d", llm.calls.Load())
	}
	llm.short.Store(true)
	if code, _ := do(t, client, http.MethodPost, url, ""); code != 502 {
		t.Errorf("a too-short letter must be refused: got %d", code)
	}
	if code, _ := do(t, client, http.MethodPost, ts.URL+"/api/jobs/99999999/cover-letter", ""); code != 404 {
		t.Errorf("missing job: want 404, got %d", code)
	}
}

func TestCoverLetterSteer(t *testing.T) {
	ts, llm := llmServer(t)
	testutil.Profile(t, testPool(t), testUserID, "# Candidate Profile\n## Target roles\nSRE.", config.Preferences{
		RoleFamilies: []string{"sre"}, Levels: []string{"entry"}, MaxYearsRequired: 3})
	url := fmt.Sprintf("%s/api/jobs/%d/cover-letter", ts.URL, testJobIDs[0])
	if code, _ := do(t, client, http.MethodPost, url, `{"tone":"pirate"}`); code != 400 {
		t.Errorf("unknown tone: want 400, got %d", code)
	}
	if code, body := do(t, client, http.MethodPost, url, `{"tone":"formal","length":"short","request":"Mention the on-call work"}`); code != 200 {
		t.Fatalf("steered letter: %d %s", code, body)
	}
	if u, _ := llm.lastUser.Load().(string); !strings.Contains(u, "Formal and polished") || !strings.Contains(u, "Mention the on-call work") {
		t.Errorf("steer not sent: %s", u)
	}
}

func TestDraftAndSuggestProfile(t *testing.T) {
	ts, _ := llmServer(t)
	code, body := do(t, client, http.MethodPost, ts.URL+"/api/profile/draft",
		`{"answers":{"current_title":"SRE intern","work_authorization":"sponsorship_now"},"preferences":{"role_families":["sre"],"levels":["entry"],"max_years_required":3,"us_only":true,"needs_sponsorship":true}}`)
	if code != 200 || !strings.Contains(body, "Candidate Profile") {
		t.Errorf("draft: %d %s", code, body)
	}
	// Suggestions need a stored resume; without one the endpoint says so.
	code, body = do(t, client, http.MethodPost, ts.URL+"/api/profile/suggest", "")
	if code == 200 && strings.Contains(body, "made_up") {
		t.Errorf("unknown role family passed through: %s", body)
	}
}

func TestRealModerationPath(t *testing.T) {
	ts, llm := llmServer(t)
	text := strings.Repeat("Mock interviews with friends helped me more than any course. ", 5)
	post := func(body string) (int, string) {
		b, _ := json.Marshal(map[string]any{"title": "What actually helped my search", "body": body})
		return do(t, client, http.MethodPost, ts.URL+"/api/posts", string(b))
	}
	if code, out := post(text); code != 201 {
		t.Errorf("clean post: %d %s", code, out)
	}
	if code, out := post(text + " Also use fake references."); code != 422 || !strings.Contains(out, "unethical") {
		t.Errorf("unethical post: %d %s", code, out)
	}
	if llm.calls.Load() != 2 {
		t.Errorf("moderation calls: %d", llm.calls.Load())
	}
}

func TestModerationFailsClosed(t *testing.T) {
	ts, _ := testServerWith(t, nil, func(s *Server) {
		s.Moderator = func(context.Context, moderation.Kind, string, string) (moderation.Verdict, error) {
			return moderation.Verdict{}, errors.New("model unavailable")
		}
	})
	b, _ := json.Marshal(map[string]any{"title": "A perfectly fine title", "body": strings.Repeat("fine words ", 40)})
	if code, _ := do(t, client, http.MethodPost, ts.URL+"/api/posts", string(b)); code != 503 {
		t.Errorf("want 503 when moderation is down, got %d", code)
	}
}

func TestScoreJobOnDemand(t *testing.T) {
	var scored atomic.Int64
	ts, _ := testServerWith(t, nil, func(s *Server) {
		s.ScoreNow = func(_ context.Context, _, jobID int64) error {
			scored.Store(jobID)
			return nil
		}
	})
	code, body := do(t, client, http.MethodPost, fmt.Sprintf("%s/api/jobs/%d/score", ts.URL, testJobIDs[1]), "")
	if code != 200 || scored.Load() != testJobIDs[1] || !strings.Contains(body, "Platform Engineer") {
		t.Errorf("score now: %d %s (scored %d)", code, body, scored.Load())
	}
	ts2, _ := testServer(t, nil)
	if code, _ := do(t, client, http.MethodPost, fmt.Sprintf("%s/api/jobs/%d/score", ts2.URL, testJobIDs[0]), ""); code != 501 {
		t.Errorf("without a scorer: want 501, got %d", code)
	}
}

func TestDeleteCommentAndAvatar(t *testing.T) {
	ts, _ := testServer(t, nil)
	b, _ := json.Marshal(map[string]any{"title": "Notes from my onsite loop", "body": strings.Repeat("Be specific about impact. ", 12)})
	code, out := do(t, client, http.MethodPost, ts.URL+"/api/posts", string(b))
	if code != 201 {
		t.Fatalf("post: %d %s", code, out)
	}
	var p struct{ ID int64 }
	json.Unmarshal([]byte(out), &p)
	_, out = do(t, client, http.MethodPost, fmt.Sprintf("%s/api/posts/%d/comments", ts.URL, p.ID), `{"body":"Thanks!"}`)
	var withComment struct {
		Comments []struct{ ID int64 } `json:"comments"`
	}
	json.Unmarshal([]byte(out), &withComment)
	if len(withComment.Comments) != 1 {
		t.Fatalf("comment: %s", out)
	}
	if code, _ := do(t, client, http.MethodDelete, fmt.Sprintf("%s/api/posts/%d/comments/%d", ts.URL, p.ID, withComment.Comments[0].ID), ""); code != 204 {
		t.Errorf("delete comment: %d", code)
	}
	if code, _ := do(t, client, http.MethodDelete, fmt.Sprintf("%s/api/posts/%d/comments/%d", ts.URL, p.ID, 99999999), ""); code != 403 {
		t.Errorf("delete missing comment: want 403, got %d", code)
	}
	if code, body := do(t, client, http.MethodDelete, ts.URL+"/api/profile/avatar", ""); code != 200 || !strings.Contains(body, `"avatar_version":null`) {
		t.Errorf("delete avatar: %d %s", code, body)
	}
}

func TestTriggerRun(t *testing.T) {
	ran := make(chan struct{}, 1)
	ts, _ := testServerWith(t, nil, func(s *Server) {
		s.RunPipeline = func(context.Context) (bool, error) {
			ran <- struct{}{}
			return true, nil
		}
	})
	if code, body := do(t, client, http.MethodPost, ts.URL+"/api/admin/runs", ""); code != 202 || !strings.Contains(body, "triggered") {
		t.Fatalf("trigger: %d %s", code, body)
	}
	<-ran
	ts2, _ := testServer(t, nil)
	if code, _ := do(t, client, http.MethodPost, ts2.URL+"/api/admin/runs", ""); code != 501 {
		t.Errorf("without a pipeline: want 501, got %d", code)
	}
}

func TestChangePassword(t *testing.T) {
	ts, _ := testServer(t, nil)
	anon := &http.Client{}
	email := fmt.Sprintf("pw-%d@example.com", testJobIDs[0])
	resp, err := anon.Post(ts.URL+"/api/auth/signup", "application/json",
		strings.NewReader(`{"email":"`+email+`","password":"first password 1"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	t.Cleanup(func() { do(t, client, http.MethodGet, ts.URL+"/healthz", "") })
	var cookie string
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			cookie = c.Value
		}
	}
	user := &http.Client{Transport: cookieTransport{cookie}}
	if code, _ := do(t, user, http.MethodPut, ts.URL+"/api/auth/password", `{"current_password":"wrong one","new_password":"second password 2"}`); code != 401 {
		t.Errorf("wrong current password: want 401, got %d", code)
	}
	if code, _ := do(t, user, http.MethodPut, ts.URL+"/api/auth/password", `{"current_password":"first password 1","new_password":"short"}`); code != 400 {
		t.Errorf("short new password: want 400, got %d", code)
	}
	if code, body := do(t, user, http.MethodPut, ts.URL+"/api/auth/password", `{"current_password":"first password 1","new_password":"second password 2"}`); code != 200 && code != 204 {
		t.Errorf("change: %d %s", code, body)
	}
	if code, _ := do(t, user, http.MethodDelete, ts.URL+"/api/auth/account", `{"password":"second password 2"}`); code != 204 {
		t.Errorf("new password does not work: %d", code)
	}
}

func TestEmailDrafts(t *testing.T) {
	ts, llm := llmServer(t)
	pool := testPool(t)
	testutil.Profile(t, pool, testUserID, "# Candidate Profile\n## Target roles\nSRE.", config.Preferences{
		RoleFamilies: []string{"sre"}, Levels: []string{"entry"}, MaxYearsRequired: 3})
	id := testJobIDs[0]
	url := fmt.Sprintf("%s/api/jobs/%d/email-draft", ts.URL, id)
	pool.Exec(context.Background(), `DELETE FROM applications WHERE user_id = $1 AND job_id = $2`, testUserID, id)
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM applications WHERE user_id = $1 AND job_id = $2`, testUserID, id)
	})
	if code, _ := do(t, client, http.MethodPost, url, `{"kind":"follow_up"}`); code != 409 {
		t.Errorf("untracked job: want 409, got %d", code)
	}
	if err := store.PutApplication(context.Background(), pool, testUserID, id, store.ApplicationInput{Status: "applied"}); err != nil {
		t.Fatal(err)
	}
	if code, _ := do(t, client, http.MethodPost, url, `{"kind":"cold_call"}`); code != 400 {
		t.Errorf("unknown kind: want 400, got %d", code)
	}
	code, body := do(t, client, http.MethodPost, url, `{"kind":"follow_up"}`)
	var e struct{ Subject, Body, Needs string }
	_ = json.Unmarshal([]byte(body), &e)
	if code != 200 || e.Subject != "Following up on the SRE application" || strings.Contains(e.Body, "—") || strings.Contains(e.Body, "NEEDS") || !strings.Contains(e.Needs, "recruiter") {
		t.Errorf("follow-up: %d %+v", code, e)
	}
	if u, _ := llm.lastUser.Load().(string); !strings.Contains(u, "Status: applied") {
		t.Errorf("the tracker wasn't sent: %s", u)
	}
	// A thank-you needs an interview, an offer reply an offer.
	for _, kind := range []string{"thank_you", "offer_reply"} {
		if code, body := do(t, client, http.MethodPost, url, `{"kind":"`+kind+`"}`); code != 409 {
			t.Errorf("%s on an applied job: want 409, got %d %s", kind, code, body)
		}
	}
	before := llm.calls.Load()
	code, body = do(t, client, http.MethodPost, url, `{"kind":"referral_request"}`)
	_ = json.Unmarshal([]byte(body), &e)
	if code != 200 || strings.Contains(e.Body, "sponsorship") || llm.calls.Load()-before != 2 {
		t.Errorf("referral: %d %+v after %d calls", code, e, llm.calls.Load()-before)
	}
}

func TestInterviewPrep(t *testing.T) {
	ts, llm := llmServer(t)
	testutil.Profile(t, testPool(t), testUserID, "# Candidate Profile\n## Target roles\nSRE.", config.Preferences{
		RoleFamilies: []string{"sre"}, Levels: []string{"entry"}, MaxYearsRequired: 3})
	url := fmt.Sprintf("%s/api/jobs/%d/interview-prep", ts.URL, testJobIDs[0])
	if code, _ := do(t, client, http.MethodGet, url, ""); code != 404 {
		t.Errorf("no prep yet: want 404, got %d", code)
	}
	code, body := do(t, client, http.MethodPost, url, "")
	if code != 200 || !strings.Contains(body, "Question 1 about the role?") {
		t.Fatalf("generate: %d %s", code, body)
	}
	if strings.Contains(body, "73%") {
		t.Error("a talking point with an invented number reached the user")
	}
	if !strings.Contains(body, "How did you cut incidents?") || !strings.Contains(body, `"prepare":"Think of a real example`) {
		t.Errorf("the question should stay, with a prepare line instead of the invented point: %s", body)
	}
	calls := llm.calls.Load()
	if code, body := do(t, client, http.MethodGet, url, ""); code != 200 || !strings.Contains(body, "Keep services healthy.") {
		t.Errorf("stored prep: %d %s", code, body)
	}
	if llm.calls.Load() != calls {
		t.Error("reading the stored prep must not call the AI")
	}
	if code, _ := do(t, client, http.MethodPost, ts.URL+"/api/jobs/99999999/interview-prep", ""); code != 404 {
		t.Errorf("missing job: want 404, got %d", code)
	}

	practice := url + "/practice"
	if code, _ := do(t, client, http.MethodPost, practice, `{"question":"Why us?","answer":"Because."}`); code != 400 {
		t.Errorf("a one-word answer: want 400, got %d", code)
	}
	code, body = do(t, client, http.MethodPost, practice, `{"question":"Tell me about an incident.","answer":"Last summer our checkout service went down at night and I was on call, so I rolled back the deploy and wrote the postmortem."}`)
	if code != 200 || !strings.Contains(body, "You named a real incident") {
		t.Fatalf("practice: %d %s", code, body)
	}
	if strings.Contains(body, "73%") {
		t.Error("an outline step with an invented number reached the user")
	}
}
