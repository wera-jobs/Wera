package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"wera/internal/normalize"
	"wera/internal/resume"
	"wera/internal/scoring"
	"wera/internal/store"
)

// resumeView is a stored resume plus, for tailored copies, how it covers
// the job's keywords.
type resumeView struct {
	*store.ResumeDoc
	Coverage *resume.Coverage `json:"coverage,omitempty"`
	Fit      *tailoredFit     `json:"fit_score,omitempty"`
}

// tailoredFit is a job's fit score before and with its tailored resume.
type tailoredFit struct {
	Before int `json:"before"`
	After  int `json:"after"`
}

func (s *Server) renderer(w http.ResponseWriter) bool {
	if !s.Resumes.Available() {
		s.writeError(w, http.StatusServiceUnavailable, resume.ErrUnavailable.Error())
		return false
	}
	return true
}

func (s *Server) resumeParam(w http.ResponseWriter, r *http.Request) (*store.ResumeDoc, bool) {
	id, ok := s.pathID(w, r, "id", "resume")
	if !ok {
		return nil, false
	}
	doc, err := store.GetResume(r.Context(), s.Pool, currentUser(r).ID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "resume not found")
		return nil, false
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return nil, false
	}
	return doc, true
}

// view adds keyword coverage for a tailored copy.
func (s *Server) view(r *http.Request, doc *store.ResumeDoc) resumeView {
	v := resumeView{ResumeDoc: doc}
	if doc.JobID != nil {
		if kws, err := store.JobKeywords(r.Context(), s.Pool, currentUser(r).ID, *doc.JobID); err == nil {
			c := resume.KeywordCoverage(doc.Data.PlainText(), kws)
			v.Coverage = &c
		}
		v.Fit = s.tailoredFit(r, *doc.JobID, doc.Data.PlainText())
	}
	return v
}

// tailoredFit is the job's fit score, and what it becomes with the
// skills a tailored resume shows (the model's ratings are kept, see
// scoring.TailoredFit), or nil when the job has no score to start from.
func (s *Server) tailoredFit(r *http.Request, jobID int64, text string) *tailoredFit {
	user := currentUser(r).ID
	score, raw, required, err := store.JobFitInputs(r.Context(), s.Pool, user, jobID)
	if err != nil || score == nil || raw == "" {
		return nil
	}
	prof, err := store.GetProfile(r.Context(), s.Pool, user)
	if err != nil || prof == nil {
		return nil
	}
	before, after, ok := scoring.TailoredFit(raw, prof.Markdown, text, required)
	if !ok {
		return nil
	}
	// The shown score is the stored one; the resume moves it by what it adds.
	return &tailoredFit{Before: *score, After: max(0, min(100, *score+after-before))}
}

func (s *Server) listResumes(w http.ResponseWriter, r *http.Request) {
	list, err := store.ListResumes(r.Context(), s.Pool, currentUser(r).ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	imp, err := store.GetResumeImport(r.Context(), s.Pool, currentUser(r).ID)
	if err != nil {
		s.Log.Error("resume import state failed", "err", err)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"resumes": list, "import": imp})
}

func (s *Server) getResumeHandler(w http.ResponseWriter, r *http.Request) {
	if doc, ok := s.resumeParam(w, r); ok {
		s.writeJSON(w, http.StatusOK, s.view(r, doc))
	}
}

// importResume is POST /api/resumes/import: create (or with replace,
// overwrite) the base resume from LaTeX, from the uploaded resume (AI),
// or as a starter.
func (s *Server) importResume(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Source  string `json:"source"` // tex | profile | blank
		TeX     string `json:"tex"`
		Replace bool   `json:"replace"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	user := currentUser(r)
	var doc *resume.Resume
	var warnings []string
	switch in.Source {
	case "tex":
		if len(in.TeX) > 200_000 {
			s.writeError(w, http.StatusBadRequest, "that LaTeX file is too large")
			return
		}
		var err error
		if doc, warnings, err = resume.ImportTeX(in.TeX); err != nil {
			s.writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	case "profile":
		prof, err := store.GetProfile(r.Context(), s.Pool, user.ID)
		if err != nil || prof == nil || strings.TrimSpace(prof.ResumeText) == "" {
			s.writeError(w, http.StatusBadRequest, "upload your resume on the Profile page first")
			return
		}
		if !s.aiReady(w, r, "importing resumes") {
			return
		}
		var ierr *importError
		if doc, warnings, ierr = s.importUpload(r.Context(), user.ID, prof.ResumeText); ierr != nil {
			s.writeError(w, ierr.code, ierr.msg)
			return
		}
	case "blank":
		doc = starterResume(user)
	default:
		s.writeError(w, http.StatusBadRequest, "source must be tex, profile or blank")
		return
	}
	id, ierr := s.saveBase(r.Context(), user.ID, doc, warnings, in.Replace)
	if ierr != nil {
		s.writeError(w, ierr.code, ierr.msg)
		return
	}
	doc2, _ := store.GetResume(r.Context(), s.Pool, user.ID, id)
	s.writeJSON(w, http.StatusCreated, s.view(r, doc2))
}

// importError is a failed import step with the response it maps to.
type importError struct {
	code int
	msg  string
}

// importUpload has the AI arrange the uploaded resume text into sections.
func (s *Server) importUpload(ctx context.Context, userID int64, text string) (*resume.Resume, []string, *importError) {
	reply, err := s.chatForUser(ctx, userID, "resume_import", 4000, resume.ImportMessages(text))
	if err != nil {
		return nil, nil, &importError{http.StatusBadGateway, "the AI service did not answer; try again"}
	}
	doc, warnings, err := resume.ParseImport(reply, text)
	if err != nil {
		return nil, nil, &importError{http.StatusBadGateway, "the AI could not read your resume; try LaTeX import or start from blank"}
	}
	return doc, warnings, nil
}

// saveBase stores doc as the user's main resume, fitted to a page. An
// existing one is overwritten only with replace (keeping its template).
func (s *Server) saveBase(ctx context.Context, userID int64, doc *resume.Resume, warnings []string, replace bool) (int64, *importError) {
	existing, err := store.BaseResume(ctx, s.Pool, userID)
	saved := &store.ResumeDoc{Title: "My resume", Data: *doc, Layout: resume.DefaultLayout, Notes: warnings}
	if err == nil {
		saved.Layout = existing.Layout // re-importing keeps the chosen template
	}
	switch {
	case err == nil && !replace:
		return 0, &importError{http.StatusConflict, "you already have a resume; import with replace to overwrite it"}
	case err == nil:
		saved.ID = existing.ID
	case !errors.Is(err, pgx.ErrNoRows):
		return 0, &importError{http.StatusInternalServerError, "query failed"}
	}
	if s.Resumes.Available() {
		if res, err := s.Resumes.Fit(ctx, &saved.Data, saved.Layout, nil); err == nil {
			saved.Layout, saved.Fit = res.Layout, res
		}
	}
	id, err := store.SaveResume(ctx, s.Pool, userID, saved)
	if err != nil {
		return 0, &importError{http.StatusInternalServerError, "could not save the resume"}
	}
	return id, nil
}

// importInBackground builds the user's main resume from a fresh upload
// while they carry on, so the Resume tab opens on a finished resume. It
// does nothing when they already have one, the AI is unavailable, or an
// import is already running.
func (s *Server) importInBackground(userID int64, text string) {
	ctx := context.Background()
	if _, err := store.BaseResume(ctx, s.Pool, userID); !errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if code, _ := s.aiAvailable(ctx, userID, "importing resumes"); code != 0 {
		return
	}
	if ok, err := store.StartResumeImport(ctx, s.Pool, userID); err != nil || !ok {
		return
	}
	s.background("resume import", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		failure := ""
		doc, warnings, ierr := s.importUpload(ctx, userID, text)
		if ierr == nil {
			_, ierr = s.saveBase(ctx, userID, doc, warnings, false)
		}
		if ierr != nil && ierr.code != http.StatusConflict {
			failure = ierr.msg
			s.Log.Warn("background resume import failed", "user_id", userID, "err", ierr.msg)
		}
		if err := store.FinishResumeImport(ctx, s.Pool, userID, failure); err != nil {
			s.Log.Error("recording a resume import failed", "user_id", userID, "err", err)
		}
	})
}

func starterResume(u *store.User) *resume.Resume {
	r := &resume.Resume{Name: u.Name, Email: u.Email, Sections: []resume.Section{
		{Kind: resume.KindEntries, Title: "Education", Entries: []resume.Entry{{Heading: "University", Subheading: "Degree", Dates: "2022 – 2026", Location: "City, ST"}}},
		{Kind: resume.KindSkills, Title: "Technical Skills", Skills: []resume.SkillGroup{{Name: "Languages", Items: "Python, Go, SQL"}}},
		{Kind: resume.KindEntries, Title: "Experience", Entries: []resume.Entry{{Heading: "Company", Subheading: "Job title", Dates: "2025 – Present", Location: "City, ST",
			Bullets: []resume.Bullet{{Text: "What you built or improved, how, and the result."}}}}},
		{Kind: resume.KindProjects, Title: "Projects", Entries: []resume.Entry{{Heading: "Project name", Tech: "Stack", Text: "What it does and why it matters."}}},
	}}
	if r.Name == "" {
		r.Name = strings.Split(u.Email, "@")[0]
	}
	_ = r.Normalize()
	return r
}

// putResume saves edits (the whole document) and an optional layout.
func (s *Server) putResume(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.resumeParam(w, r)
	if !ok {
		return
	}
	var in struct {
		Title  *string        `json:"title"`
		Data   resume.Resume  `json:"data"`
		Layout *resume.Layout `json:"layout"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if err := in.Data.Normalize(); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	doc.Data = in.Data
	if in.Title != nil {
		doc.Title = *in.Title
	}
	if in.Layout != nil {
		doc.Layout = in.Layout.Clamp()
	}
	doc.Fit = nil // edits may change the length; fit again to know
	if _, err := store.SaveResume(r.Context(), s.Pool, currentUser(r).ID, doc); err != nil {
		s.writeError(w, http.StatusInternalServerError, "save failed")
		return
	}
	saved, _ := store.GetResume(r.Context(), s.Pool, currentUser(r).ID, doc.ID)
	s.writeJSON(w, http.StatusOK, s.view(r, saved))
}

func (s *Server) deleteResumeHandler(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "resume")
	if !ok {
		return
	}
	if err := store.DeleteResume(r.Context(), s.Pool, currentUser(r).ID, id); errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "resume not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fitResume is POST /api/resumes/{id}/fit: make it one page.
func (s *Server) fitResume(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.resumeParam(w, r)
	if !ok || !s.renderer(w) {
		return
	}
	prio := resume.DefaultPriority
	if doc.JobID != nil {
		if kws, err := store.JobKeywords(r.Context(), s.Pool, currentUser(r).ID, *doc.JobID); err == nil && len(kws) > 0 {
			prio = resume.KeywordPriority(kws)
		}
	}
	res, err := s.Resumes.Fit(r.Context(), &doc.Data, doc.Layout, prio)
	if err != nil {
		s.Log.Error("fit failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not lay out the resume")
		return
	}
	doc.Layout, doc.Fit = res.Layout, res
	if _, err := store.SaveResume(r.Context(), s.Pool, currentUser(r).ID, doc); err != nil {
		s.writeError(w, http.StatusInternalServerError, "save failed")
		return
	}
	s.writeJSON(w, http.StatusOK, s.view(r, doc))
}

// previewResume is POST /api/resumes/preview: render unsaved edits as SVG
// pages (shown as images, which cannot run scripts) and measure them.
func (s *Server) previewResume(w http.ResponseWriter, r *http.Request) {
	if !s.renderer(w) {
		return
	}
	if !s.previewLimit.Allow(strconv.FormatInt(currentUser(r).ID, 10)) {
		s.writeError(w, http.StatusTooManyRequests, "too many previews; slow down a little")
		return
	}
	var in struct {
		Data   resume.Resume `json:"data"`
		Layout resume.Layout `json:"layout"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if err := in.Data.Normalize(); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	pages, err := s.Resumes.SVG(ctx, &in.Data, in.Layout)
	if err != nil {
		s.Log.Warn("preview failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not render the preview")
		return
	}
	out := make([]string, len(pages))
	for i, p := range pages {
		out[i] = string(p)
	}
	m, _ := s.Resumes.Measure(ctx, &in.Data, in.Layout)
	s.writeJSON(w, http.StatusOK, map[string]any{"pages": out, "measure": m, "review": in.Data.Review(m.Pages)})
}

func resumeFilename(name, ext string) string {
	base := safeFilename(strings.ReplaceAll(strings.TrimSpace(name), " ", "_") + "_Resume." + ext)
	if base == "" || strings.HasPrefix(base, ".") {
		return "Resume." + ext
	}
	return base
}

// resumePDF is GET /api/resumes/{id}/pdf (?download=1).
func (s *Server) resumePDF(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.resumeParam(w, r)
	if !ok || !s.renderer(w) {
		return
	}
	pdf, err := s.Resumes.PDF(r.Context(), &doc.Data, doc.Layout)
	if err != nil {
		s.Log.Error("pdf failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not render the resume")
		return
	}
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, disposition, resumeFilename(doc.Data.Name, "pdf")))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; object-src 'self'; frame-ancestors 'self'")
	w.Write(pdf)
}

// resumeTeX is GET /api/resumes/{id}/tex: the LaTeX source.
func (s *Server) resumeTeX(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.resumeParam(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/x-tex; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, resumeFilename(doc.Data.Name, "tex")))
	_, _ = w.Write([]byte(doc.Data.ExportTeX(doc.Layout)))
}

// resumeText is GET /api/resumes/{id}/txt: plain text, for pasting into
// application forms that want the resume typed out.
func (s *Server) resumeText(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.resumeParam(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, resumeFilename(doc.Data.Name, "txt")))
	}
	_, _ = w.Write([]byte(strings.ReplaceAll(doc.Data.PlainText(), "**", "") + "\n"))
}

// resumeTemplates is GET /api/resumes/templates.
func (s *Server) resumeTemplates(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{"templates": resume.Templates, "default_accent": resume.DefaultAccent})
}

// texOf is POST /api/resumes/tex: the LaTeX for unsaved edits, whole and
// split into the template's preamble and the editable body.
func (s *Server) texOf(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Data   resume.Resume `json:"data"`
		Layout resume.Layout `json:"layout"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if err := in.Data.Normalize(); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	full := in.Data.ExportTeX(in.Layout)
	preamble, _, _ := strings.Cut(full, `\begin{document}`)
	s.writeJSON(w, http.StatusOK, map[string]any{"tex": full, "preamble": preamble, "body": in.Data.TeXBody()})
}

// parseTeX is POST /api/resumes/parse-tex: LaTeX (a whole file or just the
// body) back into a resume, with notes on anything not understood.
func (s *Server) parseTeX(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TeX string `json:"tex"`
	}
	if err := decodeJSON(r, &in); err != nil || strings.TrimSpace(in.TeX) == "" {
		s.writeError(w, http.StatusBadRequest, "send the LaTeX as tex")
		return
	}
	if len(in.TeX) > 200_000 {
		s.writeError(w, http.StatusRequestEntityTooLarge, "that LaTeX file is too large")
		return
	}
	data, warnings, err := resume.ImportTeX(in.TeX)
	if err != nil {
		s.writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if warnings == nil {
		warnings = []string{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"data": data, "warnings": warnings})
}

// thumbnails is POST /api/resumes/thumbnails: the first page of a resume
// in every template, for choosing one.
func (s *Server) thumbnails(w http.ResponseWriter, r *http.Request) {
	if !s.renderer(w) {
		return
	}
	if !s.previewLimit.Allow(strconv.FormatInt(currentUser(r).ID, 10)) {
		s.writeError(w, http.StatusTooManyRequests, "too many previews; slow down a little")
		return
	}
	var in struct {
		Data   resume.Resume `json:"data"`
		Layout resume.Layout `json:"layout"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if err := in.Data.Normalize(); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out := make(map[string]string, len(resume.Templates))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, t := range resume.Templates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := in.Layout
			l.Template = t.ID
			pages, err := s.Resumes.SVG(ctx, &in.Data, l)
			if err == nil && len(pages) > 0 {
				mu.Lock()
				out[t.ID] = string(pages[0])
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	s.writeJSON(w, http.StatusOK, map[string]any{"thumbnails": out})
}

// jobKeywords is GET /api/jobs/{id}/keywords: the posting's keywords and
// how the user's resume for it (tailored, else base) covers them.
func (s *Server) jobKeywords(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	user := currentUser(r)
	kws, err := store.JobKeywords(r.Context(), s.Pool, user.ID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "job not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	doc, err := store.JobResume(r.Context(), s.Pool, user.ID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		doc, err = store.BaseResume(r.Context(), s.Pool, user.ID)
	}
	out := map[string]any{"keywords": kws, "coverage": nil, "resume_id": nil}
	if err == nil {
		out["coverage"] = resume.KeywordCoverage(doc.Data.PlainText(), kws)
		out["resume_id"] = doc.ID
	}
	s.writeJSON(w, http.StatusOK, out)
}

// getJobResume is GET /api/jobs/{id}/resume: the copy tailored for a job.
func (s *Server) getJobResume(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	doc, err := store.JobResume(r.Context(), s.Pool, currentUser(r).ID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "no tailored resume for this job yet")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, s.view(r, doc))
}

// tailorJobResume is POST /api/jobs/{id}/resume: a copy of the base resume
// tailored to the job by the AI (edits keyed by bullet, checked), then
// fitted to one page keeping the bullets that match the job.
func (s *Server) tailorJobResume(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	// An optional steer from the user; an empty body is fine.
	var in struct {
		Request string `json:"request"`
	}
	if err := decodeJSON(r, &in); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if len(in.Request) > 1000 {
		s.writeError(w, http.StatusBadRequest, "that request is too long")
		return
	}
	user := currentUser(r)
	base, err := store.BaseResume(r.Context(), s.Pool, user.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusBadRequest, "create your resume first (Resume page)")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	company, title, desc, err := s.jobContext(r, id, "", "", "")
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "job not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if !s.aiReady(w, r, "tailoring resumes") {
		return
	}
	kws, _ := store.JobKeywords(r.Context(), s.Pool, user.ID, id)
	before := resume.KeywordCoverage(base.Data.PlainText(), kws)
	msgs := resume.TailorMessages(&base.Data, company, title, desc, before.Missing, in.Request)
	var tailored *resume.Tailored
	// A reply that can't be read is asked for once more before the user sees an error.
	for try := 1; tailored == nil; try++ {
		reply, err := s.chatForUser(r.Context(), user.ID, "tailor", 2500, msgs)
		if err != nil {
			s.writeError(w, http.StatusBadGateway, "the AI service did not answer; try again")
			return
		}
		if tailored, err = resume.ApplyTailoring(&base.Data, reply); err != nil {
			s.Log.Warn("unusable tailoring reply", "user_id", user.ID, "job_id", id, "try", try, "err", err)
			if try == 2 {
				s.writeError(w, http.StatusBadGateway, "the AI returned an unusable edit twice; try again in a moment")
				return
			}
		}
	}
	doc := &store.ResumeDoc{Title: fmt.Sprintf("%s, %s", title, company), JobID: &id, Data: *tailored.Resume, Layout: base.Layout, Notes: tailored.Notes}
	if s.Resumes.Available() {
		if res, err := s.Resumes.Fit(r.Context(), &doc.Data, doc.Layout, tailored.Priority(kws)); err == nil {
			doc.Layout, doc.Fit = res.Layout, res
		}
	}
	rid, err := store.SaveResume(r.Context(), s.Pool, user.ID, doc)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "could not save the tailored resume")
		return
	}
	saved, _ := store.GetResume(r.Context(), s.Pool, user.ID, rid)
	v := s.view(r, saved)
	s.writeJSON(w, http.StatusOK, map[string]any{"resume": v, "coverage_before": before})
}

// suggestBullet is POST /api/resumes/suggest-bullet: three stronger
// rewrites of one bullet (numbers only as [placeholders] when missing),
// optionally aimed at a job.
func (s *Server) suggestBullet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text  string `json:"text"`
		Role  string `json:"role"`
		JobID int64  `json:"job_id"`
	}
	if err := decodeJSON(r, &in); err != nil || strings.TrimSpace(in.Text) == "" {
		s.writeError(w, http.StatusBadRequest, "send the bullet as text")
		return
	}
	if len(in.Text) > 800 || len(in.Role) > 300 {
		s.writeError(w, http.StatusBadRequest, "that bullet is too long")
		return
	}
	if !s.aiReady(w, r, "suggesting bullets") {
		return
	}
	user := currentUser(r)
	job := ""
	if in.JobID > 0 {
		if company, title, desc, err := s.jobContext(r, in.JobID, "", "", ""); err == nil {
			job = title + " at " + company + "\n" + normalize.Clip(desc, 3000)
		}
	}
	reply, err := s.chatForUser(r.Context(), user.ID, "bullet", 600, resume.BulletMessages(in.Text, in.Role, job))
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "the AI service did not answer; try again")
		return
	}
	out, err := resume.ParseBulletSuggestions(in.Text, reply)
	if err != nil || len(out) == 0 {
		s.writeError(w, http.StatusBadGateway, "the AI did not return usable suggestions; try again")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"suggestions": out})
}

// coverLetterPDF is GET /api/jobs/{id}/cover-letter/pdf (?download=1): the
// job's cover letter as a file, for forms that ask to attach one.
func (s *Server) coverLetterPDF(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok || !s.renderer(w) {
		return
	}
	user := currentUser(r)
	job, err := store.GetJob(r.Context(), s.Pool, user.ID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	c, err := store.GetCoverLetter(r.Context(), s.Pool, user.ID, id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if c == nil || strings.TrimSpace(c.Body) == "" {
		s.writeError(w, http.StatusNotFound, "no cover letter yet")
		return
	}
	a, _, err := store.GetApplicant(r.Context(), s.Pool, user)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	name := strings.TrimSpace(strings.Join(strings.Fields(a.FirstName+" "+a.LastName), " "))
	if name == "" {
		name = user.Name
	}
	var place []string
	for _, p := range []string{a.City, a.State} {
		if p = strings.TrimSpace(p); p != "" {
			place = append(place, p)
		}
	}
	pdf, err := s.Resumes.LetterPDF(r.Context(), resume.Letter{
		Name: name, Email: a.Email, Phone: a.Phone, Location: strings.Join(place, ", "),
		Date: time.Now().Format("January 2, 2006"), Company: job.Company, Title: job.Title, Body: c.Body,
	})
	if err != nil {
		s.Log.Error("letter pdf failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not render the letter")
		return
	}
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	file := safeFilename(strings.ReplaceAll(name, " ", "_") + "_Cover_Letter.pdf")
	if file == "" || strings.HasPrefix(file, ".") || strings.HasPrefix(file, "_") {
		file = "Cover_Letter.pdf"
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, disposition, file))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; object-src 'self'; frame-ancestors 'self'")
	_, _ = w.Write(pdf)
}
