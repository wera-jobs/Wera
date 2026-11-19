package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"wera/internal/store"
)

func TestCompanyLogoRoute(t *testing.T) {
	ts, pool := testServer(t, nil)
	ctx := context.Background()
	var id, bare int64
	if err := pool.QueryRow(ctx, `INSERT INTO companies (name, ats, token, industry) VALUES ('Logo Test Co', 'greenhouse', 'logo-test-co', 'other') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO companies (name, ats, token, industry) VALUES ('Bare Test Co', 'greenhouse', 'bare-test-co', 'other') RETURNING id`).Scan(&bare); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM companies WHERE id = ANY($1)`, []int64{id, bare}) })
	if err := store.SaveLogo(ctx, pool, id, "image/svg+xml", []byte("<svg/>"), "https://x"); err != nil {
		t.Fatal(err)
	}
	// No session needed: an <img> asks for it.
	resp, err := http.Get(ts.URL + "/api/companies/" + strconv.FormatInt(id, 10) + "/logo?v=1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	h := resp.Header
	if resp.StatusCode != 200 || string(body) != "<svg/>" || h.Get("Content-Type") != "image/svg+xml" ||
		!strings.Contains(h.Get("Cache-Control"), "immutable") || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("logo: %d %q %v", resp.StatusCode, body, h)
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/companies/"+strconv.FormatInt(id, 10)+"/logo", nil)
	req.Header.Set("If-None-Match", h.Get("ETag"))
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != http.StatusNotModified {
		t.Errorf("conditional: %d", r.StatusCode)
	}
	if r, _ := http.Get(ts.URL + "/api/companies/" + strconv.FormatInt(bare, 10) + "/logo"); r.StatusCode != 404 {
		t.Errorf("no logo: %d", r.StatusCode)
	}
}

func TestCompanyPageRoute(t *testing.T) {
	ts, pool := testServer(t, nil)
	ctx := context.Background()
	var id int64
	if err := pool.QueryRow(ctx, `INSERT INTO companies (name, ats, token, industry) VALUES ('Page Test Co', 'greenhouse', 'page-test-co', 'other') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM companies WHERE id = $1`, id) })
	path := ts.URL + "/api/companies/" + strconv.FormatInt(id, 10)
	resp, err := client.Get(path)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Name     string
		Followed bool
		Roles    []any
		H1B      any
	}
	_ = json.NewDecoder(resp.Body).Decode(&page)
	resp.Body.Close()
	if resp.StatusCode != 200 || page.Name != "Page Test Co" || page.Roles == nil || page.H1B != nil {
		t.Errorf("page: %d %+v", resp.StatusCode, page)
	}
	for _, m := range []string{http.MethodPut, http.MethodDelete} {
		req, _ := http.NewRequest(m, path+"/follow", nil)
		r, _ := client.Do(req)
		if r.StatusCode != http.StatusNoContent {
			t.Errorf("%s follow: %d", m, r.StatusCode)
		}
	}
	if r, _ := client.Get(ts.URL + "/api/companies/999999999"); r.StatusCode != 404 {
		t.Errorf("missing company: %d", r.StatusCode)
	}
}
