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
	"wera/internal/testutil"
)

func TestCleanSearch(t *testing.T) {
	got, ok := cleanSearch("?q=data&job=42&offset=50&min_score=70&evil=1&industry=")
	if !ok || got != "min_score=70&q=data" {
		t.Errorf("cleaned to %q", got)
	}
	if _, ok := cleanSearch(strings.Repeat("q=x&", 300)); ok {
		t.Error("an overlong query was accepted")
	}
}

func TestSavedSearches(t *testing.T) {
	ts, pool := testServer(t, nil)
	ctx := context.Background()
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM saved_searches WHERE user_id = $1`, testUserID) })
	do := func(method, path, body string, want int) []byte {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, ts.URL+path, rd)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s %s: %d %s, want %d", method, path, body, resp.StatusCode, out, want)
		}
		return out
	}
	do(http.MethodPost, "/api/searches", `{"name":"  ","query":"q=x"}`, 400)
	var s store.SavedSearch
	_ = json.Unmarshal(do(http.MethodPost, "/api/searches", `{"name":" Remote data ","query":"?work_mode=remote&q=data&job=9"}`, 201), &s)
	if s.Name != "Remote data" || s.Query != "q=data&work_mode=remote" {
		t.Errorf("saved: %+v", s)
	}
	id := strconv.FormatInt(s.ID, 10)
	do(http.MethodPatch, "/api/searches/"+id, `{"name":"Remote data roles"}`, 204)
	var list struct{ Searches []store.SavedSearch }
	_ = json.Unmarshal(do(http.MethodGet, "/api/searches", "", 200), &list)
	if len(list.Searches) != 1 || list.Searches[0].Name != "Remote data roles" {
		t.Errorf("list: %+v", list.Searches)
	}
	// The saved query lists what its filters list.
	direct, _ := io.ReadAll(must(client.Get(ts.URL + "/api/jobs?q=data&work_mode=remote")).Body)
	saved, _ := io.ReadAll(must(client.Get(ts.URL + "/api/jobs?" + list.Searches[0].Query)).Body)
	if string(direct) != string(saved) {
		t.Error("the saved search lists different jobs")
	}

	// Someone else's search can't be renamed or deleted.
	other := testutil.User(t, pool, false)
	theirs, err := store.AddSearch(ctx, pool, other.ID, "Theirs", "q=x")
	if err != nil {
		t.Fatal(err)
	}
	tid := strconv.FormatInt(theirs.ID, 10)
	do(http.MethodPatch, "/api/searches/"+tid, `{"name":"Mine now"}`, 404)
	do(http.MethodDelete, "/api/searches/"+tid, "", 404)

	// At most 20.
	for i := 1; i < store.MaxSavedSearches; i++ {
		do(http.MethodPost, "/api/searches", `{"name":"S`+strconv.Itoa(i)+`","query":"q=x"}`, 201)
	}
	do(http.MethodPost, "/api/searches", `{"name":"One too many","query":"q=x"}`, 409)
	do(http.MethodDelete, "/api/searches/"+id, "", 204)
	do(http.MethodPost, "/api/searches", `{"name":"Room again","query":"q=x"}`, 201)
}

func must(r *http.Response, err error) *http.Response {
	if err != nil {
		panic(err)
	}
	return r
}
