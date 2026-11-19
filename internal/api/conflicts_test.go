package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"wera/internal/config"
	"wera/internal/profile"
	"wera/internal/store"
)

func TestFindConflicts(t *testing.T) {
	five := 5.0
	a := &store.Applicant{AuthorizedToWork: "yes", NeedsSponsorship: "no", CurrentTitle: "Data Scientist", YearsExperience: "3"}
	ans := profile.Answers{WorkAuthorization: "sponsorship_future", CurrentTitle: "data scientist", YearsExperience: &five}
	got := findConflicts(a, ans, config.Preferences{NeedsSponsorship: true})
	keys := make([]string, 0, len(got))
	for _, c := range got {
		keys = append(keys, c.Key)
	}
	// The title differs only in case: no disagreement.
	if strings.Join(keys, ",") != "work_authorization,years_experience" {
		t.Fatalf("conflicts = %v", keys)
	}
	if got[0].Details != "Authorized to work, no sponsorship needed" || got[0].Elsewhere != "Authorized now, will need sponsorship later" || !got[0].Redraft {
		t.Errorf("work authorization = %+v", got[0])
	}
	// Agreeing answers, but matching still filters for sponsorship.
	ans.WorkAuthorization = "citizen_or_resident"
	if got := findConflicts(a, ans, config.Preferences{NeedsSponsorship: true}); len(got) == 0 || got[0].Elsewhere != "Show only jobs open to sponsorship" {
		t.Errorf("matching setting not compared: %+v", got)
	}
	// Details that don't say (no answer to sponsorship) disagree with nothing.
	if got := findConflicts(&store.Applicant{AuthorizedToWork: "yes"}, profile.Answers{WorkAuthorization: "sponsorship_now"}, config.Preferences{}); len(got) != 0 {
		t.Errorf("silent details conflicted: %+v", got)
	}
}

func TestResolveConflicts(t *testing.T) {
	ts, pool := testServer(t, nil)
	ctx := context.Background()
	five := 5.0
	answers, _ := json.Marshal(profile.Answers{WorkAuthorization: "sponsorship_future", CurrentTitle: "Analyst", YearsExperience: &five})
	prefs := config.Preferences{NeedsSponsorship: true}
	if err := store.SavePreferences(ctx, pool, testUserID, prefs, answers); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveApplicant(ctx, pool, testUserID, &store.Applicant{FirstName: "Ada", AuthorizedToWork: "yes", NeedsSponsorship: "no", CurrentTitle: "Data Scientist", YearsExperience: "5"}); err != nil {
		t.Fatal(err)
	}
	code, body := do(t, client, http.MethodGet, ts.URL+"/api/applicant/conflicts", "")
	if code != 200 || !strings.Contains(body, `"key":"work_authorization"`) || !strings.Contains(body, `"key":"current_title"`) || strings.Contains(body, "years_experience") {
		t.Fatalf("conflicts: %d %s", code, body)
	}
	// Keep the details: the profile's answer and matching follow.
	if code, body := do(t, client, http.MethodPost, ts.URL+"/api/applicant/conflicts/resolve", `{"key":"work_authorization","keep":"details"}`); code != 200 || strings.Contains(body, "work_authorization") {
		t.Fatalf("resolve: %d %s", code, body)
	}
	prof, _ := store.GetProfile(ctx, pool, testUserID)
	var ans profile.Answers
	_ = json.Unmarshal(prof.Answers, &ans)
	if ans.WorkAuthorization != "citizen_or_resident" || prof.Preferences.NeedsSponsorship {
		t.Errorf("profile kept the old answer: %q, needs sponsorship %v", ans.WorkAuthorization, prof.Preferences.NeedsSponsorship)
	}
	// Keep the profile: the details follow.
	if code, body := do(t, client, http.MethodPost, ts.URL+"/api/applicant/conflicts/resolve", `{"key":"current_title","keep":"profile"}`); code != 200 || strings.TrimSpace(body) != `{"conflicts":[]}` {
		t.Fatalf("resolve title: %d %s", code, body)
	}
	a, _, _ := store.GetApplicant(ctx, pool, &store.User{ID: testUserID})
	if a.CurrentTitle != "Analyst" || a.FirstName != "Ada" {
		t.Errorf("details = %+v", a)
	}
	if code, _ := do(t, client, http.MethodPost, ts.URL+"/api/applicant/conflicts/resolve", `{"key":"current_title","keep":"profile"}`); code != 404 {
		t.Errorf("resolving twice: want 404, got %d", code)
	}
	if code, _ := do(t, client, http.MethodPost, ts.URL+"/api/applicant/conflicts/resolve", `{"key":"x","keep":"both"}`); code != 400 {
		t.Errorf("bad keep: want 400, got %d", code)
	}
}
