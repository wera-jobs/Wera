package store_test

import (
	"context"
	"math"
	"testing"

	"wera/internal/store"
	"wera/internal/testutil"
)

// Admin spend counts fit scoring (analyses) and every other AI call
// (llm_usage) alike, so the overview, the user list, the usage page and
// SpendByUser agree.
func TestAdminSpendCountsEveryAICall(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	u := testutil.User(t, pool, false)
	companyID, _ := testutil.Company(t, pool, "ai_ml")
	job := testutil.Jobs(t, pool, companyID, testutil.Job{Title: "Platform Engineer"})[0]

	overview0, err := store.AdminOverview(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	usage0, err := store.Usage(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}

	var analysisID int64
	if err := pool.QueryRow(ctx, `INSERT INTO analyses (job_id, kind, model, profile_hash, fit_score, raw, prompt_tokens, cached_tokens, completion_tokens, cost_usd, user_id)
		VALUES ($1, 'score', 'm', $2, 80, '{}', 1000, 400, 200, 0.25, $3) RETURNING id`, job, testutil.Unique("hash"), u.ID).Scan(&analysisID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM analyses WHERE id = $1`, analysisID) })
	if err := store.RecordLLMUsage(ctx, pool, u.ID, "cover_letter", "m", 3000, 0, 500, 0.5); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM llm_usage WHERE user_id = $1`, u.ID) })

	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

	au, err := store.AdminUserByID(ctx, pool, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !near(au.MonthUSD, 0.75) {
		t.Errorf("admin user spend %.4f, want 0.75 (scoring 0.25 + cover letter 0.5)", au.MonthUSD)
	}
	spends, err := store.SpendByUser(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range spends {
		if s.ID == u.ID && !near(s.MonthUSD, au.MonthUSD) {
			t.Errorf("SpendByUser %.4f disagrees with the admin user %.4f", s.MonthUSD, au.MonthUSD)
		}
	}

	overview, err := store.AdminOverview(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if d := overview.MonthSpendUSD - overview0.MonthSpendUSD; !near(d, 0.75) {
		t.Errorf("overview spend grew by %.4f, want 0.75", d)
	}

	usage, err := store.Usage(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if d := usage.Totals.CostUSD - usage0.Totals.CostUSD; !near(d, 0.75) {
		t.Errorf("usage total cost grew by %.4f, want 0.75", d)
	}
	if d := usage.Totals.PromptTokens - usage0.Totals.PromptTokens; d != 4000 {
		t.Errorf("usage prompt tokens grew by %d, want 4000", d)
	}
	if d := usage.Totals.Analyses - usage0.Totals.Analyses; d != 1 {
		t.Errorf("analyses count grew by %d, want 1 (it counts fit analyses only)", d)
	}
	// The newest day is today; it may already have other calls on it.
	newest := func(v *store.UsageView) (string, float64) {
		if len(v.PerDay) == 0 {
			return "", 0
		}
		return v.PerDay[0].Day, v.PerDay[0].CostUSD
	}
	day0, cost0 := newest(usage0)
	day, cost := newest(usage)
	if day != day0 {
		cost0 = 0
	}
	if !near(cost-cost0, 0.75) {
		t.Errorf("today's cost grew by %.4f, want 0.75", cost-cost0)
	}
}
