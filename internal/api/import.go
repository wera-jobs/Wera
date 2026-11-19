package api

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"wera/internal/store"
)

const maxImportRows = 500

// importStatus reads the status words other trackers use (Huntr, Teal,
// Simplify, spreadsheets) as Wera's statuses; "" is not recognized.
func importStatus(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("_", " ", "-", " ").Replace(s)
	switch {
	case s == "" || s == "saved" || s == "wishlist" || s == "bookmarked" || s == "interested" || s == "to apply" || s == "planned":
		return "saved"
	case s == "applied" || s == "submitted" || s == "application sent" || s == "in review" || s == "under review":
		return "applied"
	case strings.Contains(s, "interview") || s == "phone screen" || s == "screening" || s == "onsite" || s == "assessment" || s == "take home":
		return "interviewing"
	case strings.Contains(s, "offer") || s == "accepted" || s == "hired":
		return "offer"
	case s == "rejected" || s == "declined" || s == "no offer" || s == "not selected" || s == "closed":
		return "rejected"
	case s == "not interested" || s == "withdrawn" || s == "archived" || s == "ghosted":
		return "not_interested"
	}
	return ""
}

var dateLayouts = []string{"2006-01-02", "1/2/2006", "01/02/2006", "Jan 2, 2006", "January 2, 2006", "2 Jan 2006", time.RFC3339}

// importDate reads a date a spreadsheet might hold, as noon UTC (so no
// time zone moves it to another day); zero when it can't.
func importDate(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			if l == time.RFC3339 {
				return t
			}
			return t.Add(12 * time.Hour)
		}
	}
	return time.Time{}
}

var leadingQuote = regexp.MustCompile(`^'([=+\-@])`)

// importText undoes the export's formula guard ('=x is =x) and trims.
func importText(s string) string {
	return strings.TrimSpace(leadingQuote.ReplaceAllString(strings.TrimSpace(s), "$1"))
}

type importRow struct {
	Company  string `json:"company"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Status   string `json:"status"`
	Location string `json:"location"`
	Notes    string `json:"notes"`
	Applied  string `json:"applied"` // the date applied, if the sheet has one
}

type importResult struct {
	Row     int    `json:"row"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Status  string `json:"status,omitempty"`
	JobID   int64  `json:"job_id,omitempty"`
	Created bool   `json:"created,omitempty"`
}

// check validates one row and normalizes it in place; "" when it's fine.
func (row *importRow) check() string {
	row.Company, row.Title, row.URL = importText(row.Company), importText(row.Title), importText(row.URL)
	row.Location, row.Notes = importText(row.Location), importText(row.Notes)
	switch {
	case row.Company == "" || row.Title == "":
		return "needs a company and a title"
	case row.URL == "":
		return "needs the job's web address"
	case len(row.Company) > 140 || len(row.Title) > 200 || len(row.Location) > 200 || len(row.URL) > 2000 || len(row.Notes) > 5000:
		return "a cell is too long"
	}
	if u, err := url.Parse(row.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "the web address isn't a link (http or https)"
	}
	st := importStatus(row.Status)
	if st == "" {
		return "status \"" + row.Status + "\" isn't one Wera knows (saved, applied, interviewing, offer, rejected, not interested)"
	}
	row.Status = st
	return ""
}

// importApplications is POST /api/applications/import {"rows": [...],
// "dry_run": bool}: tracked jobs from a spreadsheet, as the dashboard
// mapped its columns. Each row comes back with what happened; a dry run
// only checks. Imported jobs join the tracker but aren't scored by the AI
// (an import can be large); opening one scores it.
func (s *Server) importApplications(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Rows   []importRow `json:"rows"`
		DryRun bool        `json:"dry_run"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if len(in.Rows) == 0 || len(in.Rows) > maxImportRows {
		s.writeError(w, http.StatusBadRequest, "import 1 to 500 rows at a time")
		return
	}
	user := currentUser(r)
	results := make([]importResult, 0, len(in.Rows))
	imported := 0
	for i := range in.Rows {
		row := &in.Rows[i]
		res := importResult{Row: i + 1}
		if why := row.check(); why != "" {
			res.Error = why
			results = append(results, res)
			continue
		}
		res.OK, res.Status = true, row.Status
		if in.DryRun {
			results = append(results, res)
			continue
		}
		id, created, err := store.AddManualJob(r.Context(), s.Pool, user.ID, store.ManualJob{
			URL: row.URL, Company: row.Company, Title: row.Title, Location: row.Location, Industry: "other",
		})
		if err == nil {
			notes := row.Notes
			err = store.PutApplication(r.Context(), s.Pool, user.ID, id, store.ApplicationInput{Status: row.Status, Notes: &notes})
		}
		if err == nil && row.Status != "saved" && row.Status != "not_interested" {
			if at := importDate(row.Applied); !at.IsZero() && at.Before(time.Now()) {
				err = store.SetAppliedAt(r.Context(), s.Pool, user.ID, id, at)
			}
		}
		if err != nil {
			s.Log.Error("import row failed", "user_id", user.ID, "row", i+1, "err", err)
			res.OK, res.Error = false, "couldn't be saved"
		} else {
			res.JobID, res.Created = id, created
			imported++
		}
		results = append(results, res)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"results": results, "imported": imported})
}
