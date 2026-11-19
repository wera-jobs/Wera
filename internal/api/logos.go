package api

import (
	"net/http"
	"strconv"

	"wera/internal/store"
)

// companyLogo is GET /api/companies/{id}/logo: the company's logo as Wera
// stored it (see internal/logos), or 404 for a letter tile. Its URL
// carries a version (store.LogoURL), so it is cached for a year. An SVG
// can't run anything: it is served under a sandboxing policy.
func (s *Server) companyLogo(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "company")
	if !ok {
		return
	}
	data, ct, at, found, err := store.CompanyLogo(r.Context(), s.Pool, id)
	if err != nil {
		s.Log.Error("company logo failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if !found {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		s.writeError(w, http.StatusNotFound, "no logo")
		return
	}
	etag := `"` + strconv.FormatInt(at.Unix(), 36) + `"`
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("ETag", etag)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(data) //nolint:gosec // an image type, nosniff and a sandboxing CSP: never run as a page
}
