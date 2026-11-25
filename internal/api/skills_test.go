package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"wera/internal/config"
	"wera/internal/scoring"
	"wera/internal/store"
	"wera/internal/testutil"
)

func TestAddProfileSkill(t *testing.T) {
	var scored atomic.Int64
	ts, pool := testServerWith(t, nil, func(s *Server) {
		// Stands in for the scorer: it reads the saved profile, as the real
		// one does, and finds the skill now matched.
		s.ScoreNow = func(ctx context.Context, userID, jobID int64) error {
			scored.Store(jobID)
			p, err := store.GetProfile(ctx, s.Pool, userID)
			if err != nil {
				return err
			}
			if !strings.Contains(p.Markdown, "- Also: Terraform") {
				return fmt.Errorf("the scorer saw the old profile")
			}
			var aid int64
			if err := s.Pool.QueryRow(ctx, `INSERT INTO analyses (job_id, kind, model, profile_hash, fit_score, raw, user_id, skills_matched, skills_missing)
				VALUES ($1, 'score', 'm', $2, 88, '{}', $3, '{Terraform,Go}', '{}') RETURNING id`, jobID, scoring.ScoreKey(p.ProfileHash), userID).Scan(&aid); err != nil {
				return err
			}
			_, err = s.Pool.Exec(ctx, `UPDATE user_jobs SET stage = 'scored', analysis_id = $3 WHERE user_id = $1 AND job_id = $2`, userID, jobID, aid)
			return err
		}
	})
	before := matchCalls.Load()
	url := ts.URL + "/api/profile/skills"
	if code, _ := do(t, client, http.MethodPost, url, `{"skill":"Terraform"}`); code != 400 {
		t.Errorf("no profile yet: want 400, got %d", code)
	}
	md := "# Candidate Profile\n## Target roles\nSRE.\n\n## Skills\n- Languages: Go\n\n## Experience\n- Ran on-call\n"
	testutil.Profile(t, pool, testUserID, md, config.Preferences{RoleFamilies: []string{"sre"}, Levels: []string{"entry"}})
	for _, bad := range []string{`{"skill":""}`, `{"skill":"## Experience"}`, `{"skill":"` + strings.Repeat("x", 61) + `"}`} {
		if code, _ := do(t, client, http.MethodPost, url, bad); code != 400 {
			t.Errorf("%s: want 400, got %d", bad, code)
		}
	}
	if code, _ := do(t, client, http.MethodPost, url, `{"skill":"Terraform","job_id":99999999}`); code != 404 {
		t.Errorf("unknown job: want 404, got %d", code)
	}

	code, body := do(t, client, http.MethodPost, url, fmt.Sprintf(`{"skill":"  Terraform ","job_id":%d}`, testJobIDs[2]))
	var out struct {
		Added bool           `json:"added"`
		Job   *store.JobView `json:"job"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &out) != nil || !out.Added || out.Job == nil || out.Job.FitScore == nil || *out.Job.FitScore != 88 ||
		len(out.Job.SkillsMatched) != 2 || len(out.Job.SkillsMissing) != 0 || scored.Load() != testJobIDs[2] {
		t.Fatalf("add with a job: %d %s", code, body)
	}
	p, err := store.GetProfile(context.Background(), pool, testUserID)
	if err != nil || p.Markdown != strings.Replace(md, "- Languages: Go\n", "- Languages: Go\n- Also: Terraform\n", 1) ||
		p.ProfileHash != scoring.ProfileHash([]byte(p.Markdown)) || p.Preferences.RoleFamilies[0] != "sre" {
		t.Fatalf("saved profile: %+v %v", p, err)
	}
	// The background rematch started, as for any profile edit.
	if matchCalls.Load() == before {
		t.Error("no background match after the profile changed")
	}

	// Saying it again changes nothing and scores nothing.
	scored.Store(0)
	hash := p.ProfileHash
	code, body = do(t, client, http.MethodPost, url, fmt.Sprintf(`{"skill":"terraform","job_id":%d}`, testJobIDs[2]))
	if code != 200 || !strings.Contains(body, `"added":false`) || scored.Load() != 0 {
		t.Errorf("duplicate: %d %s", code, body)
	}
	if p, _ := store.GetProfile(context.Background(), pool, testUserID); p.ProfileHash != hash {
		t.Error("a duplicate skill changed the profile")
	}
	// Without a job: only the profile.
	if code, body := do(t, client, http.MethodPost, url, `{"skill":"Ansible"}`); code != 200 || !strings.Contains(body, `"job":null`) ||
		!strings.Contains(body, "- Also: Terraform, Ansible") {
		t.Errorf("without a job: %d %s", code, body)
	}
}
