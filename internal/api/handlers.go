package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"wera/internal/config"
	"wera/internal/store"
)

func (s *Server) putApplication(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	var body struct {
		Status string  `json:"status"`
		Notes  *string `json:"notes"` // optional: left out, the saved notes stay
	}
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if !store.ValidApplicationStatuses[body.Status] {
		s.writeError(w, http.StatusBadRequest,
			"status must be saved, applied, interviewing, offer, rejected or not_interested")
		return
	}
	user := currentUser(r)
	err := store.PutApplication(r.Context(), s.Pool, user.ID, id, store.ApplicationInput{
		Status: body.Status, Notes: body.Notes,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		s.Log.Error("put application failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	job, err := store.GetJob(r.Context(), s.Pool, user.ID, id)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
		return
	}
	s.writeJSON(w, http.StatusOK, job)
}

// clearApplication is DELETE /api/jobs/{id}/application: the job leaves the
// tracker (undoing a status set by mistake). It returns the job as it is now.
func (s *Server) clearApplication(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	user := currentUser(r)
	if err := store.ClearApplication(r.Context(), s.Pool, user.ID, id); err != nil {
		s.Log.Error("clear application failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	job, err := store.GetJob(r.Context(), s.Pool, user.ID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "job not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, job)
}

func (s *Server) today(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	userID := currentUser(r).ID
	jobs, pending, err := store.TodayJobs(r.Context(), s.Pool, userID, limit)
	if err != nil {
		s.Log.Error("today failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	// pending: jobs waiting for a full score; ungraded: of those, the ones
	// the AI has not graded yet either.
	ungraded := 0
	if pending > 0 {
		if ungraded, err = store.Ungraded(r.Context(), s.Pool, userID); err != nil {
			s.Log.Error("today failed", "err", err)
			s.writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
	}
	if jobs == nil {
		jobs = []store.JobView{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs, "count": len(jobs), "pending": pending, "ungraded": ungraded})
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	// Days are the user's own (?tz=America/Chicago); unknown zones fall back to UTC.
	tz := r.URL.Query().Get("tz")
	if _, err := time.LoadLocation(tz); err != nil || tz == "" || len(tz) > 64 {
		tz = "UTC"
	}
	st, err := store.Stats(r.Context(), s.Pool, currentUser(r).ID, tz)
	if err != nil {
		s.Log.Error("stats failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, st)
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := store.RecentRuns(r.Context(), s.Pool, limit)
	if err != nil {
		s.Log.Error("runs failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if list == nil {
		list = []store.RunView{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"runs": list})
}

func (s *Server) triggerRun(w http.ResponseWriter, r *http.Request) {
	if s.RunPipeline == nil {
		s.writeError(w, http.StatusNotImplemented, "pipeline trigger not configured")
		return
	}
	// Fast pre-check so the dashboard gets an immediate 409 while a run
	// (this process's or the worker's) is in flight; RunOnce re-checks
	// under its own lock so overlap is still impossible.
	got, release, err := store.TryAdvisoryLock(r.Context(), s.Pool, AdvisoryLockKey)
	if err != nil {
		s.Log.Error("lock check failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "lock check failed")
		return
	}
	if !got {
		s.writeError(w, http.StatusConflict, "a pipeline run is already in progress")
		return
	}
	unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	release(unlockCtx)
	cancel()

	// The run outlives the request on purpose.
	s.background("pipeline run", func() {
		if _, err := s.RunPipeline(context.Background()); err != nil {
			s.Log.Error("triggered pipeline run failed", "err", err)
		}
	})
	s.writeJSON(w, http.StatusAccepted, map[string]string{"status": "triggered"})
}

func (s *Server) companies(w http.ResponseWriter, r *http.Request) {
	list, err := store.ListCompanies(r.Context(), s.Pool, currentUser(r).ID)
	if err != nil {
		s.Log.Error("companies failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if list == nil {
		list = []store.CompanyView{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"companies": list})
}

// usage is the admin report: overall token usage plus each user's spend
// this month.
func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	u, err := store.Usage(r.Context(), s.Pool)
	if err != nil {
		s.Log.Error("usage failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	users, err := store.SpendByUser(r.Context(), s.Pool)
	if err != nil {
		s.Log.Error("usage by user failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if users == nil {
		users = []store.UserSpend{}
	}
	s.writeJSON(w, http.StatusOK, struct {
		*store.UsageView
		Users []store.UserSpend `json:"users"`
	}{u, users})
}

// myUsage returns the current user's budget and spend this month.
func (s *Server) myUsage(w http.ResponseWriter, r *http.Request) {
	b, err := store.UserBudget(r.Context(), s.Pool, currentUser(r).ID)
	if err != nil {
		s.Log.Error("my usage failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]float64{
		"monthly_budget_usd": b.MonthlyUSD,
		"month_spend_usd":    b.SpentUSD,
		"remaining_usd":      b.Remaining(),
	})
}

// industryView is one row of GET /api/industries: the taxonomy entry
// plus its counts.
type industryView struct {
	config.Industry
	store.IndustryCounts
}

// industriesAZ is the industry catalog sorted by label, so long lists are
// easy to scan.
func (s *Server) industriesAZ() []config.Industry {
	list := slices.Clone(s.Industries)
	slices.SortFunc(list, func(a, b config.Industry) int {
		return strings.Compare(strings.ToLower(a.Label), strings.ToLower(b.Label))
	})
	return list
}

func (s *Server) industries(w http.ResponseWriter, r *http.Request) {
	counts, err := store.CountByIndustry(r.Context(), s.Pool, currentUser(r).ID)
	if err != nil {
		s.Log.Error("industries failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	list := make([]industryView, 0, len(s.Industries))
	for _, ind := range s.industriesAZ() {
		list = append(list, industryView{Industry: ind, IndustryCounts: counts[ind.ID]})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"industries": list})
}

// scoreJob is POST /api/jobs/{id}/score: score one of the user's unscored
// jobs now (they opened it before the queue got to it) and return it.
func (s *Server) scoreJob(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	if s.ScoreNow == nil {
		s.writeError(w, http.StatusNotImplemented, "scoring is not configured on this server")
		return
	}
	user := currentUser(r)
	if !s.scoreLimit.Allow(strconv.FormatInt(user.ID, 10)) {
		s.writeError(w, http.StatusTooManyRequests, "too many scoring requests; try again soon")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.ScoreNow(ctx, user.ID, id); err != nil {
		s.Log.Warn("on-demand scoring failed", "user_id", user.ID, "job_id", id, "err", err)
	}
	job, err := store.GetJob(r.Context(), s.Pool, user.ID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, job)
}
