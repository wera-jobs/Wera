package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"wera/internal/store"
)

// wageLevelFor is the prevailing wage level a seniority usually files at
// (the dashboard's wageLevelFor).
func wageLevelFor(seniority string) string {
	s := strings.ToLower(seniority)
	switch {
	case regexp.MustCompile(`intern|entry|junior|new grad`).MatchString(s):
		return "I"
	case strings.Contains(s, "mid"):
		return "II"
	case strings.Contains(s, "senior"):
		return "III"
	case regexp.MustCompile(`staff|principal|lead|director`).MatchString(s):
		return "IV"
	}
	return ""
}

var usStates = map[string]string{
	"alabama": "AL", "alaska": "AK", "arizona": "AZ", "arkansas": "AR", "california": "CA", "colorado": "CO", "connecticut": "CT",
	"delaware": "DE", "district of columbia": "DC", "florida": "FL", "georgia": "GA", "hawaii": "HI", "idaho": "ID", "illinois": "IL",
	"indiana": "IN", "iowa": "IA", "kansas": "KS", "kentucky": "KY", "louisiana": "LA", "maine": "ME", "maryland": "MD",
	"massachusetts": "MA", "michigan": "MI", "minnesota": "MN", "mississippi": "MS", "missouri": "MO", "montana": "MT",
	"nebraska": "NE", "nevada": "NV", "new hampshire": "NH", "new jersey": "NJ", "new mexico": "NM", "new york": "NY",
	"north carolina": "NC", "north dakota": "ND", "ohio": "OH", "oklahoma": "OK", "oregon": "OR", "pennsylvania": "PA",
	"rhode island": "RI", "south carolina": "SC", "south dakota": "SD", "tennessee": "TN", "texas": "TX", "utah": "UT",
	"vermont": "VT", "virginia": "VA", "washington": "WA", "west virginia": "WV", "wisconsin": "WI", "wyoming": "WY",
}

var stateCode = regexp.MustCompile(`,\s*([A-Z]{2})\b`)

// usState finds the first US state a location names ("Austin, TX",
// "Seattle, Washington"); "" when it names none (remote, abroad).
func usState(loc string) string {
	if m := stateCode.FindStringSubmatch(loc); m != nil {
		for _, c := range usStates {
			if c == m[1] {
				return c
			}
		}
	}
	l := strings.ToLower(loc)
	best, at := "", len(l)+1
	for name, c := range usStates {
		if i := strings.Index(l, name); i >= 0 && (i < at || (i == at && len(name) > len(best))) {
			// "west virginia" over "virginia" when both match at once.
			best, at = c, i
		}
	}
	return best
}

// payInsight is GET /api/jobs/{id}/pay-insight: the posted pay against
// H-1B wages for similar titles at the company and in the job's state,
// each with its sample size. Comparisons only where both numbers exist.
func (s *Server) payInsight(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	job, err := store.GetJob(r.Context(), s.Pool, currentUser(r).ID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	type posted struct {
		Low, High float64 // a year, in US dollars
	}
	var p *posted
	if job.PayMin != nil && (job.PayCurrency == nil || *job.PayCurrency == "USD") {
		lo, hi := *job.PayMin, *job.PayMin
		if job.PayMax != nil {
			hi = *job.PayMax
		}
		if job.PayPeriod != nil && *job.PayPeriod == "hour" {
			lo, hi = lo*2080, hi*2080
		}
		p = &posted{lo, hi}
	}
	level := ""
	if job.Seniority != nil {
		level = wageLevelFor(*job.Seniority)
	}
	ctx := context.WithoutCancel(r.Context())
	var company *store.JobH1B
	if v, err := s.h1bCache.get(fmt.Sprintf("job|%d|%s|%s", job.CompanyID, job.Title, level), func() (any, error) {
		return store.JobH1BFor(ctx, s.Pool, job.CompanyID, job.Title, level)
	}); err == nil {
		if c := v.(*store.JobH1B); c.SimilarMid != nil && c.SimilarCount >= 3 {
			company = c
		}
	}
	loc := job.LocationRaw
	if job.LocationSummary != nil && *job.LocationSummary != "" {
		loc = *job.LocationSummary + "; " + loc
	}
	var place *store.PlaceWages
	if st := usState(loc); st != "" {
		if v, err := s.h1bCache.get("place|"+strings.ToLower(job.Title)+"|"+st, func() (any, error) {
			return store.H1BPlaceWages(ctx, s.Pool, job.Title, st)
		}); err == nil {
			place, _ = v.(*store.PlaceWages)
		}
	}

	out := map[string]any{"posted": nil, "company": nil, "place": nil}
	says := []string{}
	if p != nil {
		out["posted"] = map[string]float64{"low": p.Low, "high": p.High}
	}
	if company != nil {
		out["company"] = map[string]any{"low": *company.SimilarLow, "median": *company.SimilarMid, "high": *company.SimilarHi,
			"count": company.SimilarCount, "level": company.Level, "period": company.Period}
		if p != nil {
			says = append(says, compare(p.Low, p.High, *company.SimilarMid, fmt.Sprintf("what %s filed for similar titles", job.Company)))
		}
	}
	if place != nil {
		out["place"] = place
		if p != nil {
			says = append(says, compare(p.Low, p.High, place.Median, "what employers in "+place.State+" filed for similar titles"))
		}
	}
	out["says"] = says
	h1bCacheHeader(w)
	s.writeJSON(w, http.StatusOK, out)
}

// compare says where a posted range sits against a median, in a sentence.
func compare(lo, hi, median float64, against string) string {
	k := func(v float64) string { return fmt.Sprintf("$%.0fk", math.Round(v/1000)) }
	switch {
	case lo > median*1.05:
		return fmt.Sprintf("Even the posted low (%s) is above the median of %s (%s).", k(lo), against, k(median))
	case hi < median*0.95:
		return fmt.Sprintf("The posted top (%s) is below the median of %s (%s): worth raising if you get an offer.", k(hi), against, k(median))
	default:
		return fmt.Sprintf("The posted range (%s to %s) takes in the median of %s (%s).", k(lo), k(hi), against, k(median))
	}
}
