package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestProblemReports(t *testing.T) {
	ts, _ := testServer(t, nil)
	ext, _ := connectExtension(t, ts.URL)
	send := func(body string) (int, string) { return do(t, ext, http.MethodPost, ts.URL+"/api/ext/reports", body) }

	for _, bad := range []string{`{}`, `{"note":"   "}`, `{"note":"` + strings.Repeat("x", reportNoteMax+1) + `"}`,
		`{"note":"x","details":"` + strings.Repeat("y", reportDetailsMax+1) + `"}`, `not json`} {
		if code, _ := send(bad); code != 400 {
			t.Errorf("%.40s: want 400, got %d", bad, code)
		}
	}
	code, body := send(`{"note":"  It skipped the phone box.  ","page":"boards.greenhouse.io/acme/jobs/1","version":"0.16.3","details":"{\"fields\":[]}"}`)
	if code != 201 {
		t.Fatalf("send: %d %s", code, body)
	}
	var made struct{ ID int64 }
	json.Unmarshal([]byte(body), &made)

	// The admin sees it open, without details in the list, then with them.
	_, body = do(t, client, http.MethodGet, ts.URL+"/api/admin/reports", "")
	if !strings.Contains(body, `"note":"It skipped the phone box."`) || strings.Contains(body, `"details":`) || !strings.Contains(body, `"details_size":13`) {
		t.Errorf("list: %.400s", body)
	}
	one := fmt.Sprintf("%s/api/admin/reports/%d", ts.URL, made.ID)
	if code, body := do(t, client, http.MethodGet, one, ""); code != 200 || !strings.Contains(body, `"details":"{\"fields\":[]}"`) || !strings.Contains(body, `"version":"0.16.3"`) {
		t.Errorf("one: %d %.400s", code, body)
	}
	if _, body := do(t, client, http.MethodGet, ts.URL+"/api/admin/overview", ""); strings.Contains(body, `"reports_open":0`) {
		t.Errorf("overview counts no open report: %.300s", body)
	}

	// Resolved: off the open list, still in all, audited.
	if code, _ := do(t, client, http.MethodPatch, one, `{"resolved":true}`); code != 204 {
		t.Errorf("resolve: want 204, got %d", code)
	}
	if _, body := do(t, client, http.MethodGet, ts.URL+"/api/admin/reports", ""); strings.Contains(body, fmt.Sprintf(`"id":%d,`, made.ID)) {
		t.Errorf("resolved report still open: %.300s", body)
	}
	if _, body := do(t, client, http.MethodGet, ts.URL+"/api/admin/reports?all=1", ""); !strings.Contains(body, fmt.Sprintf(`"id":%d,`, made.ID)) {
		t.Errorf("resolved report missing from all: %.300s", body)
	}
	if code, _ := do(t, client, http.MethodPatch, ts.URL+"/api/admin/reports/999999999", `{"resolved":true}`); code != 404 {
		t.Errorf("unknown report: want 404, got %d", code)
	}
	if _, body := do(t, client, http.MethodGet, ts.URL+"/api/admin/audit", ""); !strings.Contains(body, "report.resolve") {
		t.Errorf("audit misses report.resolve")
	}

	// An extension token cannot read the admin API, even an admin's.
	if code, _ := do(t, ext, http.MethodGet, ts.URL+"/api/admin/reports", ""); code == 200 {
		t.Errorf("extension token read the reports")
	}

	// Ten an hour per person.
	for range 10 {
		send(`{"note":"again"}`)
	}
	if code, _ := send(`{"note":"one too many"}`); code != 429 {
		t.Errorf("rate limit: want 429, got %d", code)
	}
}
