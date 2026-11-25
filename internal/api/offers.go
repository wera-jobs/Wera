package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"wera/internal/store"
)

// Offer field limits.
const (
	maxOfferAmount   = 100_000_000 // a year's pay, equity or bonus in any currency
	maxOfferBonusPct = 1000
	maxOfferLocation = 200
	maxOfferNotes    = 5000
)

// offerBody is the JSON of PUT /api/jobs/{id}/offer.
type offerBody struct {
	BaseSalary   *float64   `json:"base_salary"`
	BonusPct     *float64   `json:"bonus_pct"`
	AnnualEquity *float64   `json:"annual_equity"`
	SignOn       *float64   `json:"sign_on"`
	Currency     string     `json:"currency"`
	WorkMode     string     `json:"work_mode"`
	Location     string     `json:"location"`
	DeadlineAt   *time.Time `json:"deadline_at"`
	Notes        string     `json:"notes"`
}

func (b *offerBody) input() (store.OfferInput, error) {
	in := store.OfferInput{
		BaseSalary: b.BaseSalary, BonusPct: b.BonusPct, AnnualEquity: b.AnnualEquity, SignOn: b.SignOn,
		Currency: strings.ToUpper(strings.TrimSpace(b.Currency)), WorkMode: strings.TrimSpace(b.WorkMode),
		Location: strings.TrimSpace(b.Location), Notes: strings.TrimSpace(b.Notes), DeadlineAt: b.DeadlineAt,
	}
	for _, f := range []struct {
		v    *float64
		max  float64
		what string
	}{{in.BaseSalary, maxOfferAmount, "base_salary"}, {in.AnnualEquity, maxOfferAmount, "annual_equity"},
		{in.SignOn, maxOfferAmount, "sign_on"}, {in.BonusPct, maxOfferBonusPct, "bonus_pct"}} {
		if f.v != nil && (*f.v < 0 || *f.v > f.max) {
			return in, fmt.Errorf("%s must be between 0 and %.0f", f.what, f.max)
		}
	}
	if in.Currency == "" {
		in.Currency = "USD"
	}
	if len(in.Currency) != 3 || strings.Trim(in.Currency, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return in, errors.New("currency must be a three-letter code like USD")
	}
	switch in.WorkMode {
	case "", "remote", "hybrid", "onsite":
	default:
		return in, errors.New("work_mode must be remote, hybrid or onsite")
	}
	if utf8.RuneCountInString(in.Location) > maxOfferLocation {
		return in, errors.New("location is too long")
	}
	if utf8.RuneCountInString(in.Notes) > maxOfferNotes {
		return in, errors.New("notes are too long")
	}
	return in, nil
}

// OfferBenchmark is what the employer paid on its certified H-1B
// applications (U.S. Department of Labor LCA data): overall, and for
// titles like the job's. Only given for offers in U.S. dollars.
type OfferBenchmark struct {
	EmployerFilings    int64           `json:"employer_filings"`
	EmployerMedianWage *float64        `json:"employer_median_wage"`
	TitleMedianWage    *float64        `json:"title_median_wage"`
	TitleLowWage       *float64        `json:"title_low_wage"`
	TitleHighWage      *float64        `json:"title_high_wage"`
	TitleFilings       int             `json:"title_filings"`
	Period             store.H1BPeriod `json:"period"`
}

// offerView is an offer with its totals and benchmark.
type offerView struct {
	*store.Offer
	// AnnualTotal is base, bonus at target and a year's equity;
	// FirstYearTotal adds the sign-on bonus. Nil without a base salary.
	AnnualTotal    *float64        `json:"annual_total"`
	FirstYearTotal *float64        `json:"first_year_total"`
	Benchmark      *OfferBenchmark `json:"benchmark"`
}

func offerTotals(o *store.Offer) (annual, firstYear *float64) {
	if o.BaseSalary == nil {
		return nil, nil
	}
	v := func(p *float64) float64 {
		if p == nil {
			return 0
		}
		return *p
	}
	a := *o.BaseSalary + *o.BaseSalary*v(o.BonusPct)/100 + v(o.AnnualEquity)
	f := a + v(o.SignOn)
	return &a, &f
}

// offerBenchmark reads the job's H-1B figures through the H-1B cache (the
// same entry GET /api/jobs/{id}/h1b uses), nil when the employer filed none.
func (s *Server) offerBenchmark(ctx context.Context, o *store.Offer) (*OfferBenchmark, error) {
	if o.Currency != "USD" {
		return nil, nil
	}
	cid, title := o.CompanyID, o.JobTitle
	if title == "" {
		var err error
		if err = s.Pool.QueryRow(ctx, `SELECT company_id, title FROM jobs WHERE id = $1`, o.JobID).Scan(&cid, &title); err != nil {
			return nil, err
		}
	}
	v, err := s.h1bCache.get(fmt.Sprintf("job|%d|%s|%s", cid, title, ""), func() (any, error) {
		return store.JobH1BFor(context.WithoutCancel(ctx), s.Pool, cid, title, "")
	})
	if err != nil {
		return nil, err
	}
	h := v.(*store.JobH1B)
	if !h.Matched {
		return nil, nil
	}
	return &OfferBenchmark{EmployerFilings: h.Filings, EmployerMedianWage: h.WageMedian, TitleMedianWage: h.SimilarMid,
		TitleLowWage: h.SimilarLow, TitleHighWage: h.SimilarHi, TitleFilings: h.SimilarCount, Period: h.Period}, nil
}

func (s *Server) viewOffer(ctx context.Context, o *store.Offer) (offerView, error) {
	v := offerView{Offer: o}
	v.AnnualTotal, v.FirstYearTotal = offerTotals(o)
	var err error
	v.Benchmark, err = s.offerBenchmark(ctx, o)
	return v, err
}

// getOffer is GET /api/jobs/{id}/offer.
func (s *Server) getOffer(w http.ResponseWriter, r *http.Request) {
	jobID, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	o, err := store.GetOffer(r.Context(), s.Pool, currentUser(r).ID, jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "no offer saved for this job")
		return
	}
	s.writeOffer(w, r, http.StatusOK, o, err)
}

func (s *Server) writeOffer(w http.ResponseWriter, r *http.Request, status int, o *store.Offer, err error) {
	var v offerView
	if err == nil {
		v, err = s.viewOffer(r.Context(), o)
	}
	if err != nil {
		s.Log.Error("offer failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, status, v)
}

// putOffer is PUT /api/jobs/{id}/offer: saves the whole offer.
func (s *Server) putOffer(w http.ResponseWriter, r *http.Request) {
	jobID, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	var body offerBody
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	in, err := body.input()
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	o, err := store.PutOffer(r.Context(), s.Pool, currentUser(r).ID, jobID, in)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "job not found")
		return
	}
	s.writeOffer(w, r, http.StatusOK, o, err)
}

// deleteOffer is DELETE /api/jobs/{id}/offer.
func (s *Server) deleteOffer(w http.ResponseWriter, r *http.Request) {
	jobID, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	found, err := store.DeleteOffer(r.Context(), s.Pool, currentUser(r).ID, jobID)
	if err != nil {
		s.Log.Error("delete offer failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not delete the offer")
		return
	}
	if !found {
		s.writeError(w, http.StatusNotFound, "no offer saved for this job")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listOffers is GET /api/offers: every offer, with totals and benchmarks.
func (s *Server) listOffers(w http.ResponseWriter, r *http.Request) {
	list, err := store.Offers(r.Context(), s.Pool, currentUser(r).ID)
	out := make([]offerView, 0, len(list))
	for i := range list {
		if err != nil {
			break
		}
		var v offerView
		v, err = s.viewOffer(r.Context(), &list[i])
		out = append(out, v)
	}
	if err != nil {
		s.Log.Error("list offers failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"offers": out})
}
