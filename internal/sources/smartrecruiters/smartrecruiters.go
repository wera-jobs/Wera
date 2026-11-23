// Package smartrecruiters implements a DetailSource for the SmartRecruiters
// public Posting API. The token is the company identifier, e.g.
// "ServiceNow" for https://jobs.smartrecruiters.com/ServiceNow.
package smartrecruiters

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"wera/internal/normalize"
	"wera/internal/sources"
)

const (
	defaultBaseURL = "https://api.smartrecruiters.com"
	name           = "smartrecruiters"
	pageSize       = 100
	maxListed      = 1000
)

// Adapter fetches jobs from SmartRecruiters.
type Adapter struct {
	http    *sources.HTTP
	baseURL string
}

// New returns a SmartRecruiters adapter using the shared HTTP client.
func New(h *sources.HTTP) *Adapter { return &Adapter{http: h, baseURL: defaultBaseURL} }

// NewWithBaseURL is New with a custom API base URL (used by tests).
func NewWithBaseURL(h *sources.HTTP, baseURL string) *Adapter {
	return &Adapter{http: h, baseURL: strings.TrimRight(baseURL, "/")}
}

// Name implements sources.Source.
func (a *Adapter) Name() string { return name }

type location struct {
	FullLocation string `json:"fullLocation"`
	City         string `json:"city"`
	Region       string `json:"region"`
	Country      string `json:"country"`
	Remote       bool   `json:"remote"`
}

func (l location) String() string {
	s := l.FullLocation
	if s == "" {
		s = strings.Join(nonEmpty(l.City, l.Region, strings.ToUpper(l.Country)), ", ")
	}
	if l.Remote && !strings.Contains(strings.ToLower(s), "remote") {
		s = strings.TrimPrefix(s+" (Remote)", " ")
	}
	return s
}

type posting struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	ReleasedDate string   `json:"releasedDate"`
	Location     location `json:"location"`
	Department   struct {
		Label string `json:"label"`
	} `json:"department"`
}

type listResponse struct {
	TotalFound int       `json:"totalFound"`
	Content    []posting `json:"content"`
}

// List implements sources.DetailSource. The API answers an unknown
// company with an empty list, the same as a company with no open
// postings, so an empty board is not an error: a watched company that
// closed its last posting must close it here too. Callers that probe for
// a board (CheckBoard, discover) treat zero postings as no board.
func (a *Adapter) List(ctx context.Context, token string) ([]sources.RawJob, error) {
	var jobs []sources.RawJob
	for offset := 0; offset < maxListed; offset += pageSize {
		var resp listResponse
		endpoint := fmt.Sprintf("%s/v1/companies/%s/postings?limit=%d&offset=%d",
			a.baseURL, url.PathEscape(token), pageSize, offset)
		if err := a.http.GetJSON(ctx, endpoint, &resp); err != nil {
			if offset > 0 {
				return jobs, fmt.Errorf("%w: offset %d: %w", sources.ErrPartialList, offset, err)
			}
			return nil, err
		}
		for _, p := range resp.Content {
			remote := p.Location.Remote
			jobs = append(jobs, sources.RawJob{
				ExtID:       p.ID,
				Title:       strings.TrimSpace(p.Name),
				LocationRaw: p.Location.String(),
				Department:  p.Department.Label,
				URL:         fmt.Sprintf("https://jobs.smartrecruiters.com/%s/%s", token, p.ID),
				IsRemote:    &remote,
				PostedAt:    parseTime(p.ReleasedDate),
			})
		}
		if len(resp.Content) < pageSize || offset+pageSize >= resp.TotalFound {
			break
		}
	}
	return jobs, nil
}

type detailResponse struct {
	PostingURL string `json:"postingUrl"`
	JobAd      struct {
		Sections map[string]struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"sections"`
	} `json:"jobAd"`
}

// sectionOrder is the order sections appear in a posting.
var sectionOrder = []string{"companyDescription", "jobDescription", "qualifications", "additionalInformation"}

// Detail implements sources.DetailSource.
func (a *Adapter) Detail(ctx context.Context, token string, j *sources.RawJob) error {
	var resp detailResponse
	endpoint := fmt.Sprintf("%s/v1/companies/%s/postings/%s", a.baseURL, url.PathEscape(token), url.PathEscape(j.ExtID))
	if err := a.http.GetJSON(ctx, endpoint, &resp); err != nil {
		return err
	}
	var html strings.Builder
	for _, key := range sectionOrder {
		if sec, ok := resp.JobAd.Sections[key]; ok && sec.Text != "" {
			if sec.Title != "" {
				fmt.Fprintf(&html, "<h3>%s</h3>", sec.Title)
			}
			html.WriteString(sec.Text)
		}
	}
	j.DescriptionHTML = html.String()
	j.DescriptionText = normalize.HTMLToText(j.DescriptionHTML)
	if resp.PostingURL != "" {
		j.URL = resp.PostingURL
	}
	return nil
}

// Fetch implements sources.Source (list plus every detail).
func (a *Adapter) Fetch(ctx context.Context, token string) ([]sources.RawJob, error) {
	return sources.FetchAll(ctx, a, token)
}

func parseTime(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

func nonEmpty(vals ...string) []string {
	out := vals[:0]
	for _, v := range vals {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
