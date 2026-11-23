// Package eightfold implements a DetailSource for career sites run on
// Eightfold (e.g. Netflix), using the public JSON endpoints the site
// itself calls. The token is "host/domain", e.g.
// "explore.jobs.netflix.net/netflix.com".
package eightfold

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"wera/internal/normalize"
	"wera/internal/sources"
)

const (
	name            = "eightfold"
	pageSize        = 10 // fixed by the API whatever num asks for
	maxListed       = 1000
	pageConcurrency = 4
)

// Adapter fetches jobs from Eightfold career sites.
type Adapter struct {
	http    *sources.HTTP
	baseURL string // tests only: replaces https://<host>
}

// New returns an Eightfold adapter using the shared HTTP client.
func New(h *sources.HTTP) *Adapter { return &Adapter{http: h} }

// NewWithBaseURL is New with a fixed host (used by tests).
func NewWithBaseURL(h *sources.HTTP, baseURL string) *Adapter {
	return &Adapter{http: h, baseURL: strings.TrimRight(baseURL, "/")}
}

// Name implements sources.Source.
func (a *Adapter) Name() string { return name }

func (a *Adapter) parse(token string) (base, domain string, err error) {
	host, domain, ok := strings.Cut(token, "/")
	if !ok || host == "" || domain == "" {
		return "", "", fmt.Errorf("eightfold token %q: want host/domain", token)
	}
	base = "https://" + host
	if a.baseURL != "" {
		base = a.baseURL
	}
	return base, domain, nil
}

type position struct {
	ID                 int64    `json:"id"`
	Name               string   `json:"name"`
	Location           string   `json:"location"`
	Locations          []string `json:"locations"`
	Department         string   `json:"department"`
	TCreate            int64    `json:"t_create"`
	WorkLocationOption string   `json:"work_location_option"`
	CanonicalURL       string   `json:"canonicalPositionUrl"`
	JobDescription     string   `json:"job_description"`
}

type listResponse struct {
	Count     int        `json:"count"`
	Positions []position `json:"positions"`
}

// List implements sources.DetailSource.
func (a *Adapter) List(ctx context.Context, token string) ([]sources.RawJob, error) {
	base, domain, err := a.parse(token)
	if err != nil {
		return nil, err
	}
	page := func(ctx context.Context, start int) (listResponse, error) {
		var resp listResponse
		endpoint := fmt.Sprintf("%s/api/apply/v2/jobs?domain=%s&start=%d&num=%d",
			base, url.QueryEscape(domain), start, pageSize)
		err := a.http.GetJSON(ctx, endpoint, &resp)
		return resp, err
	}
	first, err := page(ctx, 0)
	if err != nil {
		return nil, err
	}
	pages := []listResponse{first}
	var partial error
	if limit := min(first.Count, maxListed); limit > pageSize {
		rest := make([]listResponse, (limit-1)/pageSize)
		errs := make([]error, len(rest))
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(pageConcurrency)
		for i := range rest {
			g.Go(func() error {
				// A failed page drops only itself, and the listing is
				// then reported as partial.
				rest[i], errs[i] = page(gctx, (i+1)*pageSize)
				return nil
			})
		}
		_ = g.Wait()
		pages = append(pages, rest...)
		partial = errors.Join(errs...)
	}

	var jobs []sources.RawJob
	seen := map[int64]bool{}
	for _, resp := range pages {
		for _, p := range resp.Positions {
			if p.ID == 0 || seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			jobs = append(jobs, toRaw(p))
		}
	}
	if err := ctx.Err(); err != nil {
		return jobs, err
	}
	if partial != nil {
		return jobs, fmt.Errorf("%w: %w", sources.ErrPartialList, partial)
	}
	return jobs, nil
}

func toRaw(p position) sources.RawJob {
	locs := p.Locations
	if len(locs) == 0 && p.Location != "" {
		locs = []string{p.Location}
	}
	loc := strings.Join(locs, "; ")
	remote := strings.EqualFold(p.WorkLocationOption, "remote") || strings.Contains(strings.ToLower(loc), "remote")
	raw := sources.RawJob{
		ExtID:       fmt.Sprint(p.ID),
		Title:       strings.TrimSpace(p.Name),
		LocationRaw: loc,
		Department:  p.Department,
		URL:         p.CanonicalURL,
		IsRemote:    &remote,
	}
	if p.TCreate > 0 {
		t := time.Unix(p.TCreate, 0).UTC()
		raw.PostedAt = &t
	}
	return raw
}

// Detail implements sources.DetailSource.
func (a *Adapter) Detail(ctx context.Context, token string, j *sources.RawJob) error {
	base, domain, err := a.parse(token)
	if err != nil {
		return err
	}
	var p position
	endpoint := fmt.Sprintf("%s/api/apply/v2/jobs/%s?domain=%s", base, url.PathEscape(j.ExtID), url.QueryEscape(domain))
	if err := a.http.GetJSON(ctx, endpoint, &p); err != nil {
		return err
	}
	j.DescriptionHTML = p.JobDescription
	j.DescriptionText = normalize.HTMLToText(p.JobDescription)
	return nil
}

// Fetch implements sources.Source (list plus every detail).
func (a *Adapter) Fetch(ctx context.Context, token string) ([]sources.RawJob, error) {
	return sources.FetchAll(ctx, a, token)
}
