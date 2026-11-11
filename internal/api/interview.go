package api

import (
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"wera/internal/interview"
	"wera/internal/store"
)

// interviewDomains is GET /api/interview/domains?tz=: progress by domain,
// and the user's practice streak (days in their time zone).
func (s *Server) interviewDomains(w http.ResponseWriter, r *http.Request) {
	uid := currentUser(r).ID
	d, err := store.InterviewDomains(r.Context(), s.Pool, uid)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	tz := r.URL.Query().Get("tz")
	if _, err := time.LoadLocation(tz); err != nil || tz == "" || len(tz) > 64 {
		tz = "UTC"
	}
	p, err := store.InterviewPractice(r.Context(), s.Pool, uid, tz)
	if err != nil {
		s.Log.Error("interview practice failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"domains": d, "practice": p})
}

type stackTag struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

type trackView struct {
	interview.Track
	// Stack is the tags of the track's questions, most common first: the stacks a round can lean toward.
	Stack     []stackTag `json:"stack"`
	Scenarios []string   `json:"scenarios"`
}

// interviewTracks is GET /api/interview/tracks: the role tracks, and the
// one that fits the user's profile.
func (s *Server) interviewTracks(w http.ResponseWriter, r *http.Request) {
	out := make([]trackView, 0, len(interview.Tracks))
	for _, t := range interview.Tracks {
		v := trackView{Track: t, Stack: []stackTag{}, Scenarios: []string{}}
		byDomain, err := store.InterviewTags(r.Context(), s.Pool, t.Domains)
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		for tag, n := range byDomain {
			v.Stack = append(v.Stack, stackTag{tag, n})
		}
		sort.Slice(v.Stack, func(i, j int) bool {
			if v.Stack[i].Count != v.Stack[j].Count {
				return v.Stack[i].Count > v.Stack[j].Count
			}
			return v.Stack[i].Tag < v.Stack[j].Tag
		})
		for _, sc := range interview.Scenarios {
			if slices.Contains(sc.Tracks, t.ID) {
				v.Scenarios = append(v.Scenarios, sc.ID)
			}
		}
		out = append(out, v)
	}
	var families []string
	if p, err := store.GetProfile(r.Context(), s.Pool, currentUser(r).ID); err == nil && p != nil {
		families = p.Preferences.RoleFamilies
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"tracks": out, "suggested": interview.Suggest(families)})
}

// interviewQuiz is GET /api/interview/quiz?track=&domain=&difficulty=&stack=go,react&missed=1&limit=.
func (s *Server) interviewQuiz(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	diff := q.Get("difficulty")
	if diff != "" && diff != "easy" && diff != "medium" && diff != "hard" {
		s.writeError(w, http.StatusBadRequest, "difficulty must be easy, medium or hard")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	quiz := store.Quiz{Domain: q.Get("domain"), Difficulty: diff, Limit: limit, Missed: q.Get("missed") == "1"}
	if id := q.Get("track"); id != "" {
		t, ok := interview.TrackByID(id)
		if !ok {
			s.writeError(w, http.StatusBadRequest, "unknown track")
			return
		}
		quiz.Domains = t.Domains
	}
	for _, tag := range strings.Split(q.Get("stack"), ",") {
		if tag = strings.TrimSpace(strings.ToLower(tag)); tag != "" && len(quiz.Stack) < 10 {
			quiz.Stack = append(quiz.Stack, tag)
		}
	}
	qs, err := store.QuizQuestions(r.Context(), s.Pool, currentUser(r).ID, quiz)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"questions": qs})
}

// interviewAnswer is POST /api/interview/questions/{id}/answer {"choice": 0-3}.
func (s *Server) interviewAnswer(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "question")
	if !ok {
		return
	}
	var in struct {
		Choice int `json:"choice"`
	}
	if err := decodeJSON(r, &in); err != nil || in.Choice < 0 || in.Choice > 3 {
		s.writeError(w, http.StatusBadRequest, "choice must be 0-3")
		return
	}
	a, err := store.AnswerQuestion(r.Context(), s.Pool, currentUser(r).ID, id, in.Choice)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "question not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "could not record the answer")
		return
	}
	s.writeJSON(w, http.StatusOK, a)
}

// scenarioSteps maps each simulation to its number of steps.
func scenarioSteps() map[string]int {
	m := make(map[string]int, len(interview.Scenarios))
	for _, sc := range interview.Scenarios {
		m[sc.ID] = len(sc.Steps)
	}
	return m
}

type scenarioCard struct {
	ID         string                 `json:"id"`
	Tracks     []string               `json:"tracks"`
	Title      string                 `json:"title"`
	Role       string                 `json:"role"`
	Blurb      string                 `json:"blurb"`
	Difficulty string                 `json:"difficulty"`
	Cast       []interview.Person     `json:"cast"`
	Steps      int                    `json:"steps"`
	MaxPoints  int                    `json:"max_points"`
	Skills     []string               `json:"skills"`
	Progress   store.ScenarioProgress `json:"progress"`
}

func card(sc interview.Scenario, p store.ScenarioProgress, ok bool) scenarioCard {
	if !ok {
		p = store.ScenarioProgress{Best: -1}
	}
	skills := make([]string, len(sc.Steps))
	for i, st := range sc.Steps {
		skills[i] = st.Skill
	}
	return scenarioCard{ID: sc.ID, Tracks: sc.Tracks, Title: sc.Title, Role: sc.Role, Blurb: sc.Blurb, Difficulty: sc.Difficulty,
		Cast: sc.Cast, Steps: len(sc.Steps), MaxPoints: sc.MaxPoints(), Skills: skills, Progress: p}
}

// interviewScenarios is GET /api/interview/scenarios: every work simulation with the user's progress.
func (s *Server) interviewScenarios(w http.ResponseWriter, r *http.Request) {
	runs, err := store.ScenarioRuns(r.Context(), s.Pool, currentUser(r).ID, scenarioSteps())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	out := make([]scenarioCard, 0, len(interview.Scenarios))
	for _, sc := range interview.Scenarios {
		p, ok := runs[sc.ID]
		out = append(out, card(sc, p, ok))
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"scenarios": out})
}

// reveal is a step once answered: every reply's rating and why, and the takeaway.
type reveal struct {
	Step     int                `json:"step"`
	Choice   int                `json:"choice"`
	Rating   string             `json:"rating"`
	Points   int                `json:"points"`
	Choices  []interview.Choice `json:"choices"`
	Takeaway string             `json:"takeaway"`
}

func revealStep(sc interview.Scenario, step, choice int) reveal {
	st := sc.Steps[step]
	return reveal{Step: step, Choice: choice, Rating: st.Choices[choice].Rating, Points: interview.Points(st.Choices[choice].Rating),
		Choices: st.Choices, Takeaway: st.Takeaway}
}

// interviewScenario is GET /api/interview/scenarios/{id}: the emails and
// replies, and what the user already answered on their current run.
func (s *Server) interviewScenario(w http.ResponseWriter, r *http.Request) {
	sc, ok := interview.ScenarioByID(chi.URLParam(r, "id"))
	if !ok {
		s.writeError(w, http.StatusNotFound, "scenario not found")
		return
	}
	runs, err := store.ScenarioRuns(r.Context(), s.Pool, currentUser(r).ID, map[string]int{sc.ID: len(sc.Steps)})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	p, seen := runs[sc.ID]
	answered := []reveal{}
	for i, c := range p.Choices {
		if c >= 0 {
			answered = append(answered, revealStep(sc, i, c))
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"scenario": card(sc, p, seen), "steps": sc.Public(), "answered": answered})
}

// interviewScenarioAnswer is POST /api/interview/scenarios/{id}/steps/{step}/answer {"choice": 0-3}.
// Answering the first step starts a new run.
func (s *Server) interviewScenarioAnswer(w http.ResponseWriter, r *http.Request) {
	sc, ok := interview.ScenarioByID(chi.URLParam(r, "id"))
	if !ok {
		s.writeError(w, http.StatusNotFound, "scenario not found")
		return
	}
	step, err := strconv.Atoi(chi.URLParam(r, "step"))
	if err != nil || step < 0 || step >= len(sc.Steps) {
		s.writeError(w, http.StatusNotFound, "step not found")
		return
	}
	var in struct {
		Choice int `json:"choice"`
	}
	if err := decodeJSON(r, &in); err != nil || in.Choice < 0 || in.Choice > 3 {
		s.writeError(w, http.StatusBadRequest, "choice must be 0-3")
		return
	}
	rv := revealStep(sc, step, in.Choice)
	if err := store.ScenarioAnswer(r.Context(), s.Pool, currentUser(r).ID, sc.ID, step, in.Choice, rv.Points); err != nil {
		s.writeError(w, http.StatusInternalServerError, "could not record the answer")
		return
	}
	s.writeJSON(w, http.StatusOK, rv)
}
