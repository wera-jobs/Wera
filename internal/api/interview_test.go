package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestInterviewQuiz(t *testing.T) {
	ts, _ := testServer(t, nil)
	_, out := do(t, client, http.MethodGet, ts.URL+"/api/interview/quiz?difficulty=easy&limit=3", "")
	var quiz struct {
		Questions []map[string]any `json:"questions"`
	}
	json.Unmarshal([]byte(out), &quiz)
	if len(quiz.Questions) == 0 {
		t.Fatalf("no questions: %s", out)
	}
	if strings.Contains(out, `"answer"`) || strings.Contains(out, "explanation") {
		t.Error("quiz leaks the answer")
	}
	id := int64(quiz.Questions[0]["id"].(float64))
	code, out := do(t, client, http.MethodPost, fmt.Sprintf("%s/api/interview/questions/%d/answer", ts.URL, id), `{"choice":1}`)
	if code != 200 || !strings.Contains(out, "explanation") {
		t.Fatalf("answer: %d %s", code, out)
	}
	if code, _ := do(t, client, http.MethodPost, fmt.Sprintf("%s/api/interview/questions/%d/answer", ts.URL, id), `{"choice":7}`); code != 400 {
		t.Errorf("bad choice: want 400, got %d", code)
	}
	if _, out := do(t, client, http.MethodGet, ts.URL+"/api/interview/domains", ""); !strings.Contains(out, `"answered":1`) {
		t.Errorf("domain stats: %s", out)
	}
	if code, _ := do(t, client, http.MethodGet, ts.URL+"/api/interview/quiz?difficulty=impossible", ""); code != 400 {
		t.Errorf("bad difficulty: want 400, got %d", code)
	}
}

func TestInterviewTracksAndStack(t *testing.T) {
	ts, _ := testServer(t, nil)
	_, out := do(t, client, http.MethodGet, ts.URL+"/api/interview/tracks", "")
	var tr struct {
		Tracks []struct {
			ID        string   `json:"id"`
			Domains   []string `json:"domains"`
			Scenarios []string `json:"scenarios"`
			Stack     []struct {
				Tag string `json:"tag"`
			} `json:"stack"`
		} `json:"tracks"`
		Suggested string `json:"suggested"`
	}
	if err := json.Unmarshal([]byte(out), &tr); err != nil || len(tr.Tracks) < 8 || tr.Suggested == "" {
		t.Fatalf("tracks: %v %s", err, out)
	}
	if tr.Tracks[0].ID != "backend" || len(tr.Tracks[0].Scenarios) == 0 || len(tr.Tracks[0].Stack) == 0 {
		t.Errorf("backend track: %+v", tr.Tracks[0])
	}

	// A track's round stays inside its domains, and a stack leads with its tag.
	_, out = do(t, client, http.MethodGet, ts.URL+"/api/interview/quiz?track=frontend&stack=react&limit=25", "")
	var quiz struct {
		Questions []struct {
			Domain string   `json:"domain"`
			Tags   []string `json:"tags"`
		} `json:"questions"`
	}
	json.Unmarshal([]byte(out), &quiz)
	if len(quiz.Questions) == 0 {
		t.Fatalf("frontend quiz: %s", out)
	}
	allowed := map[string]bool{}
	for _, d := range tr.Tracks[1].Domains {
		allowed[d] = true
	}
	for _, q := range quiz.Questions {
		if !allowed[q.Domain] {
			t.Errorf("frontend round has %s", q.Domain)
		}
	}
	if q := quiz.Questions[0]; !strings.Contains(strings.Join(q.Tags, ","), "react") {
		t.Errorf("stack not preferred: first is %+v", q)
	}
	if code, _ := do(t, client, http.MethodGet, ts.URL+"/api/interview/quiz?track=astronaut", ""); code != 400 {
		t.Errorf("unknown track: want 400, got %d", code)
	}
}

func TestInterviewMissedRound(t *testing.T) {
	ts, _ := testServer(t, nil)
	_, out := do(t, client, http.MethodGet, ts.URL+"/api/interview/quiz?domain=Security&limit=1", "")
	var quiz struct {
		Questions []struct {
			ID int64 `json:"id"`
		} `json:"questions"`
	}
	json.Unmarshal([]byte(out), &quiz)
	if len(quiz.Questions) != 1 {
		t.Fatalf("security quiz: %s", out)
	}
	// Answer right, then wrong: the latest answer is what counts.
	id := quiz.Questions[0].ID
	wrong := -1
	for c := range 4 {
		_, a := do(t, client, http.MethodPost, fmt.Sprintf("%s/api/interview/questions/%d/answer", ts.URL, id), fmt.Sprintf(`{"choice":%d}`, c))
		if strings.Contains(a, `"correct":false`) {
			wrong = c
		}
	}
	if wrong < 0 {
		t.Fatal("no wrong choice")
	}
	do(t, client, http.MethodPost, fmt.Sprintf("%s/api/interview/questions/%d/answer", ts.URL, id), fmt.Sprintf(`{"choice":%d}`, wrong))
	_, out = do(t, client, http.MethodGet, ts.URL+"/api/interview/quiz?missed=1&limit=25", "")
	if !strings.Contains(out, fmt.Sprintf(`"id":%d,`, id)) || strings.Count(out, `"id":`) != 1 {
		t.Errorf("missed round, want only %d: %s", id, out)
	}
	_, out = do(t, client, http.MethodGet, ts.URL+"/api/interview/domains?tz=America/Chicago", "")
	if !strings.Contains(out, `"streak":1`) || !strings.Contains(out, `"missed":1`) {
		t.Errorf("practice: %s", out)
	}
}

func TestInterviewScenarioRun(t *testing.T) {
	ts, _ := testServer(t, nil)
	_, out := do(t, client, http.MethodGet, ts.URL+"/api/interview/scenarios", "")
	if !strings.Contains(out, "backend-first-week") || strings.Contains(out, "feedback") || strings.Contains(out, `"rating"`) {
		t.Fatalf("list: %s", out)
	}
	_, out = do(t, client, http.MethodGet, ts.URL+"/api/interview/scenarios/backend-first-week", "")
	if strings.Contains(out, "feedback") || !strings.Contains(out, "Amara Osei") || !strings.Contains(out, `"answered":[]`) {
		t.Fatalf("scenario leaks or lacks: %s", out)
	}
	total := 0
	for step := range 4 {
		code, a := do(t, client, http.MethodPost, fmt.Sprintf("%s/api/interview/scenarios/backend-first-week/steps/%d/answer", ts.URL, step), `{"choice":1}`)
		var rv struct {
			Points   int    `json:"points"`
			Takeaway string `json:"takeaway"`
			Choices  []struct {
				Feedback string `json:"feedback"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(a), &rv); code != 200 || err != nil || rv.Takeaway == "" || len(rv.Choices) != 4 || rv.Choices[0].Feedback == "" {
			t.Fatalf("step %d: %d %s", step, code, a)
		}
		total += rv.Points
	}
	_, out = do(t, client, http.MethodGet, ts.URL+"/api/interview/scenarios/backend-first-week", "")
	if !strings.Contains(out, fmt.Sprintf(`"best":%d`, total)) || !strings.Contains(out, `"answered":4`) {
		t.Errorf("finished run: want best %d: %s", total, out)
	}
	// Step 0 again starts a new run; the best score stays.
	do(t, client, http.MethodPost, ts.URL+"/api/interview/scenarios/backend-first-week/steps/0/answer", `{"choice":0}`)
	_, out = do(t, client, http.MethodGet, ts.URL+"/api/interview/scenarios", "")
	if !strings.Contains(out, `"answered":1`) || !strings.Contains(out, fmt.Sprintf(`"best":%d`, total)) {
		t.Errorf("new run: %s", out)
	}
	for _, bad := range []string{"nope/steps/0", "backend-first-week/steps/9", "backend-first-week/steps/x"} {
		if code, _ := do(t, client, http.MethodPost, ts.URL+"/api/interview/scenarios/"+bad+"/answer", `{"choice":0}`); code != 404 {
			t.Errorf("%s: want 404, got %d", bad, code)
		}
	}
	if code, _ := do(t, client, http.MethodPost, ts.URL+"/api/interview/scenarios/backend-first-week/steps/0/answer", `{"choice":4}`); code != 400 {
		t.Errorf("bad choice: want 400, got %d", code)
	}
}
