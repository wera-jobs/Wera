// Package api implements the REST API served by `wera serve`
// (PLAN.md section 9): all JSON, chi router, CORS for the Vite dev
// server, and a Prometheus /metrics endpoint.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/auth"
	"wera/internal/buildinfo"
	"wera/internal/config"
	"wera/internal/metrics"
	"wera/internal/moderation"
	"wera/internal/notify"
	"wera/internal/resume"
	"wera/internal/store"
)

// AdvisoryLockKey mirrors pipeline.advisoryLockKey for the run trigger.
const AdvisoryLockKey = 4242

// Server bundles the API dependencies.
type Server struct {
	Pool    *pgxpool.Pool
	Log     *slog.Logger
	Metrics *metrics.Registry

	// Industries is the taxonomy from config/industries.yaml, in display
	// order (GET /api/industries).
	Industries []config.Industry

	// RunPipeline is set by `wera serve` to pipeline.RunOnce; nil
	// disables POST /api/runs.
	RunPipeline func(ctx context.Context) (bool, error)

	h1bCache, sponsorCache *ttlCache

	// Notify sends email; nil when no mail provider is set up.
	Notify *notify.Service

	// Accounts and browser security (see config.Env).
	SignupEnabled bool
	// SignupsPerHour caps sign-ups per client IP (0 means the default, 5).
	SignupsPerHour int
	UserBudgetUSD  float64 // monthly inference budget for new accounts
	CookieSecure   bool
	PublicOrigins  []string
	TrustProxy     bool

	// Profiles: the role catalog the forms offer, the AI provider for
	// drafts, and the background matcher run after a profile is saved.
	Roles     *config.Roles
	Env       *config.Env
	MatchUser func(ctx context.Context, userID int64)
	// PrepareUser filters and ranks a user's jobs (no LLM, a few seconds)
	// so the dashboard has estimated matches the moment a save returns.
	PrepareUser func(ctx context.Context, userID int64) error
	// Moderator replaces the AI moderation call (tests).
	Moderator func(ctx context.Context, kind moderation.Kind, title, body string) (moderation.Verdict, error)

	// ScoreNow scores one unscored job immediately (set by `wera serve`).
	ScoreNow func(ctx context.Context, userID, jobID int64) error
	// ShareJob offers a job a user added to the other users it suits.
	ShareJob func(ctx context.Context, jobID, addedBy int64)
	// CheckBoard lists a job board once and returns its open job count,
	// before a board found through an added job is watched.
	CheckBoard func(ctx context.Context, ats, token string) (int, error)

	signupLimit  *auth.Limiter
	loginLimit   *auth.Limiter
	accountLimit *auth.Limiter
	draftLimit   *auth.Limiter
	scoreLimit   *auth.Limiter
	letterLimit  *auth.Limiter
	postLimit    *auth.Limiter
	commentLimit *auth.Limiter
	extAILimit   *auth.Limiter
	previewLimit *auth.Limiter

	// Resumes renders resume documents (nil-safe: unavailable without typst).
	Resumes *resume.Renderer
}

// Handler builds the router with all routes.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)
	r.Use(s.cors)

	r.Get("/healthz", s.healthz)
	if s.Metrics != nil {
		r.Handle("/metrics", s.Metrics.HTTPHandler())
	}

	signups := s.SignupsPerHour
	if signups <= 0 {
		signups = 5
	}
	s.signupLimit = auth.NewLimiter(signups, time.Hour)
	s.loginLimit = auth.NewLimiter(10, 15*time.Minute)
	s.accountLimit = auth.NewLimiter(10, 15*time.Minute)
	s.draftLimit = auth.NewLimiter(10, time.Hour)
	s.scoreLimit = auth.NewLimiter(120, time.Hour)
	s.letterLimit = auth.NewLimiter(20, time.Hour)
	s.postLimit = auth.NewLimiter(5, time.Hour)
	s.commentLimit = auth.NewLimiter(30, time.Hour)
	s.extAILimit = auth.NewLimiter(120, time.Hour)
	s.previewLimit = auth.NewLimiter(900, time.Hour)
	s.h1bCache = newTTLCache(time.Hour, 5000)
	s.sponsorCache = newTTLCache(10*time.Minute, 200)

	r.Route("/api", func(r chi.Router) {
		r.Use(s.sameOrigin)
		r.Post("/auth/signup", s.signup)
		r.Post("/auth/login", s.login)
		r.Post("/auth/logout", s.logout)
		r.Get("/auth/config", s.authConfig)
		r.Get("/public/stats", s.publicStats)
		r.Post("/auth/verify-email", s.verifyEmail)
		r.Post("/auth/password/forgot", s.forgotPassword)
		r.Post("/auth/password/reset", s.resetPassword)
		r.Post("/ext/tokens", s.createExtToken)

		// Everything else needs a session.
		r.Group(func(r chi.Router) {
			r.Use(s.requireUser)
			r.Get("/auth/me", s.me)
			r.Put("/auth/password", s.changePassword)
			r.Post("/auth/verify-email/send", s.sendVerification)
			r.Delete("/auth/account", s.deleteAccount)
			r.Get("/settings", s.getSettings)
			r.Put("/settings", s.putSettings)
			r.Put("/companies/{id}/hidden", s.hideCompany)
			r.Delete("/companies/{id}/hidden", s.hideCompany)

			r.Get("/jobs", s.listJobs)
			r.Post("/jobs", s.addJob)
			r.Get("/jobs/{id}", s.getJob)
			r.Put("/jobs/{id}/application", s.putApplication)
			r.Delete("/jobs/{id}/application", s.clearApplication)
			r.Post("/jobs/{id}/score", s.scoreJob)
			r.Get("/jobs/{id}/cover-letter", s.getCoverLetter)
			r.Post("/jobs/{id}/cover-letter", s.generateCoverLetter)
			r.Put("/jobs/{id}/cover-letter", s.putCoverLetter)
			r.Get("/jobs/{id}/cover-letter/pdf", s.coverLetterPDF)

			r.Get("/posts", s.listPosts)
			r.Post("/posts", s.createPost)
			r.Get("/posts/{id}", s.getPost)
			r.Delete("/posts/{id}", s.deletePost)
			r.Post("/posts/{id}/comments", s.createComment)
			r.Delete("/posts/{id}/comments/{commentID}", s.deleteComment)
			r.Put("/posts/{id}/reactions/{kind}", s.react)
			r.Delete("/posts/{id}/reactions/{kind}", s.react)

			r.Get("/interview/domains", s.interviewDomains)
			r.Get("/interview/quiz", s.interviewQuiz)
			r.Post("/interview/questions/{id}/answer", s.interviewAnswer)
			r.Get("/sponsorship", s.sponsorship)
			r.Get("/sponsorship/{companyID}/h1b", s.companyH1B)
			r.Get("/h1b/employers", s.h1bEmployers)
			r.Get("/h1b/employer", s.h1bEmployer)

			// Chrome extension (bearer token or session).
			r.Get("/ext/tokens", s.listExtTokens)
			r.Delete("/ext/tokens/{id}", s.deleteExtToken)
			r.Get("/applicant", s.getApplicant)
			r.Put("/applicant", s.putApplicant)
			r.Post("/applicant/suggest", s.suggestApplicant)
			r.Post("/ext/extract", s.extractJob)
			r.Get("/ext/jobs/lookup", s.lookupJob)
			r.Post("/ext/keywords", s.postingKeywords)
			r.Post("/ext/jobs", s.addJob)
			r.Post("/ext/answer", s.answerQuestion)
			r.Get("/applicant/answers", s.listAnswers)
			r.Put("/applicant/answers", s.saveAnswers)
			r.Delete("/applicant/answers", s.deleteAllAnswers)
			r.Patch("/applicant/answers/{id}", s.updateAnswer)
			r.Delete("/applicant/answers/{id}", s.deleteAnswer)
			r.Post("/ext/autofill", s.suggestFill)
			r.Post("/ext/autofill-runs", s.startAutofillRun)
			r.Post("/ext/autofill-runs/{id}/pages", s.addAutofillPage)
			r.Get("/autofill-runs", s.listAutofillRuns)

			r.Get("/resumes", s.listResumes)
			r.Post("/resumes/import", s.importResume)
			r.Post("/resumes/preview", s.previewResume)
			r.Get("/resumes/templates", s.resumeTemplates)
			r.Post("/resumes/tex", s.texOf)
			r.Post("/resumes/parse-tex", s.parseTeX)
			r.Post("/resumes/thumbnails", s.thumbnails)
			r.Post("/resumes/suggest-bullet", s.suggestBullet)
			r.Get("/resumes/{id}", s.getResumeHandler)
			r.Put("/resumes/{id}", s.putResume)
			r.Delete("/resumes/{id}", s.deleteResumeHandler)
			r.Post("/resumes/{id}/fit", s.fitResume)
			r.Get("/resumes/{id}/pdf", s.resumePDF)
			r.Get("/resumes/{id}/tex", s.resumeTeX)
			r.Get("/resumes/{id}/txt", s.resumeText)
			r.Get("/jobs/{id}/keywords", s.jobKeywords)
			r.Get("/jobs/{id}/h1b", s.jobH1B)
			r.Get("/jobs/{id}/resume", s.getJobResume)
			r.Post("/jobs/{id}/resume", s.tailorJobResume)
			r.Get("/today", s.today)
			r.Get("/stats", s.stats)
			r.Get("/runs", s.runs)
			r.Get("/companies", s.companies)
			r.Get("/excluded", s.excluded)
			r.Get("/industries", s.industries)
			r.Get("/usage/me", s.myUsage)

			r.Get("/profile/options", s.profileOptions)
			r.Get("/profile", s.getProfile)
			r.Put("/profile", s.putProfile)
			r.Post("/profile/resume", s.uploadResume)
			r.Post("/profile/draft", s.draftProfile)
			r.Post("/profile/suggest", s.suggestPreferences)
			r.Get("/profile/resume/file", s.getResumeFile)
			r.Get("/profile/avatar", s.getAvatar)
			r.Put("/profile/avatar", s.putAvatar)
			r.Delete("/profile/avatar", s.deleteAvatar)
			r.Get("/users/{id}/avatar", s.getUserAvatar)

			// The admin app (wera-admin).
			r.Route("/admin", func(r chi.Router) {
				r.Use(s.requireAdmin)
				r.Get("/overview", s.adminOverview)
				r.Get("/users", s.adminUsers)
				r.Get("/users/{id}", s.adminUser)
				r.Patch("/users/{id}", s.updateUser)
				r.Delete("/users/{id}", s.deleteUser)
				r.Post("/users/{id}/sign-out", s.signOutUser)
				r.Get("/runs", s.runs)
				r.Post("/runs", s.adminTriggerRun)
				r.Get("/usage", s.usage)
				r.Get("/companies", s.companies)
				r.Get("/content", s.adminContent)
				r.Put("/content/{kind}/{id}/status", s.setContentStatus)
				r.Delete("/content/{kind}/{id}", s.deleteContent)
				r.Get("/audit", s.adminAudit)
				r.Get("/email", s.emailOverview)
				r.Get("/email/preview/{name}", s.emailPreview)
				r.Post("/email/test", s.emailTest)
			})
		})
	})
	return r
}

// allowedOrigins covers the Vite dev server (PLAN.md section 9).
var allowedOrigins = map[string]bool{
	"http://localhost:5173": true,
	"http://127.0.0.1:5173": true,
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); allowedOrigins[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeJSON renders v as JSON.
func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.Log.Error("encoding response failed", "err", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, status int, msg string) {
	s.writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if err := s.Pool.Ping(r.Context()); err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "database unreachable")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok", "version": buildinfo.Version, "commit": buildinfo.Commit,
	})
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since, _ := time.Parse("2006-01-02", q.Get("since"))
	minScore, _ := strconv.Atoi(q.Get("min_score"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	postedWithin, _ := strconv.Atoi(q.Get("posted_within"))

	jobs, err := store.ListJobs(r.Context(), s.Pool, currentUser(r).ID, store.JobQuery{
		Industry:        q.Get("industry"),
		Category:        q.Get("category"),
		MinScore:        minScore,
		Sponsorship:     q.Get("sponsorship"),
		WorkMode:        q.Get("work_mode"),
		Status:          q.Get("status"),
		Q:               q.Get("q"),
		Since:           since,
		PostedWithin:    max(0, postedWithin),
		IncludeExcluded: q.Get("include_excluded") == "true",
		Sort:            q.Get("sort"),
		Limit:           limit,
		Offset:          offset,
	})
	if err != nil {
		s.Log.Error("list jobs failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if jobs == nil {
		jobs = []store.JobView{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs, "count": len(jobs)})
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	user := currentUser(r)
	job, err := store.GetJob(r.Context(), s.Pool, user.ID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		s.Log.Error("get job failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	// PLAN.md section 9: the detail view carries the latest score
	// analysis (inside JobView), the application row, the plain-text
	// description, and the deep analysis when one exists.
	var deep *store.DeepView
	prof, err := store.GetProfile(r.Context(), s.Pool, user.ID)
	if err == nil && prof != nil {
		deep, err = store.GetDeepAnalysis(r.Context(), s.Pool, id, prof.ProfileHash)
	}
	if err != nil {
		s.Log.Error("get deep analysis failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	desc, err := store.JobDescription(r.Context(), s.Pool, id)
	if err != nil {
		s.Log.Error("get job description failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, struct {
		store.JobView
		Description string          `json:"description"`
		Deep        *store.DeepView `json:"deep"`
	}{JobView: *job, Description: desc, Deep: deep})
}
