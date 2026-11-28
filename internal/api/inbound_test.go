package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"wera/internal/config"
	"wera/internal/store"
	"wera/internal/testutil"
)

func TestInboundEmail(t *testing.T) {
	const secret = "test-secret"
	ts, pool := testServerWith(t, nil, func(s *Server) {
		s.Env = &config.Env{InboundEmailDomain: "in.example.com", InboundEmailSecret: secret}
	})
	ctx := t.Context()
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM email_suggestions WHERE user_id = $1`, testUserID)
		pool.Exec(ctx, `DELETE FROM inbound_addresses WHERE user_id = $1`, testUserID)
	})
	companyID, company := testutil.Company(t, pool, "other")
	ids := testutil.Jobs(t, pool, companyID, testutil.Job{Title: "Inbound Analyst"})
	if _, err := pool.Exec(ctx, `INSERT INTO user_jobs (user_id, job_id, content_hash, stage) VALUES ($1, $2, 'h', 'scored')`, testUserID, ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := store.PutApplication(ctx, pool, testUserID, ids[0], store.ApplicationInput{Status: "applied"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM applications WHERE job_id = $1`, ids[0]) })

	// Off until the user turns it on.
	code, body := do(t, client, http.MethodGet, ts.URL+"/api/inbound-address", "")
	if code != 200 || !strings.Contains(body, `"available":true`) || !strings.Contains(body, `"address":null`) {
		t.Fatalf("before: %d %s", code, body)
	}
	code, body = do(t, client, http.MethodPost, ts.URL+"/api/inbound-address", "")
	var view struct {
		Address string `json:"address"`
	}
	json.Unmarshal([]byte(body), &view)
	if code != 200 || !strings.HasPrefix(view.Address, "u-") || !strings.HasSuffix(view.Address, "@in.example.com") {
		t.Fatalf("turn on: %d %s", code, body)
	}

	post := func(rcpt, msg, sig string, at time.Time) int {
		t.Helper()
		stamp := strconv.FormatInt(at.Unix(), 10)
		if sig == "" {
			sig = inboundSignature(secret, stamp, rcpt, []byte(msg))
		}
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/inbound-email", bytes.NewReader([]byte(msg)))
		req.Header.Set("X-Wera-Rcpt", rcpt)
		req.Header.Set("X-Wera-Timestamp", stamp)
		req.Header.Set("X-Wera-Signature", sig)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	reject := fmt.Sprintf("From: %s Recruiting <no-reply@us.greenhouse-mail.io>\r\nTo: someone@example.com\r\nSubject: Your application to %s\r\n\r\nThank you for applying to the Inbound Analyst role. Unfortunately, we have decided to move forward with other candidates.", company, company)
	now := time.Now()
	if c := post(view.Address, reject, "00", now); c != 401 {
		t.Errorf("bad signature: %d", c)
	}
	if c := post(view.Address, reject, "", now.Add(-time.Hour)); c != 401 {
		t.Errorf("replayed: %d", c)
	}
	if c := post("u-nobody@in.example.com", reject, "", now); c != 404 {
		t.Errorf("unknown address: %d", c)
	}
	if c := post(view.Address, reject, "", now); c != 204 {
		t.Fatalf("reply: %d", c)
	}
	// Gmail's confirmation is kept for the user to finish setup.
	confirm := "From: Gmail Team <forwarding-noreply@google.com>\r\nSubject: (#123456789) Gmail Forwarding Confirmation - Receive Mail from someone@example.com\r\n\r\nTo allow, click https://mail-settings.google.com/mail/vf-abc123 or https://evil.example.com/x"
	if c := post(view.Address, confirm, "", now); c != 204 {
		t.Fatalf("confirmation: %d", c)
	}
	code, body = do(t, client, http.MethodGet, ts.URL+"/api/inbound-address", "")
	if !strings.Contains(body, `"confirm_code":"123456789"`) || !strings.Contains(body, `"confirm_url":"https://mail-settings.google.com/mail/vf-abc123"`) || !strings.Contains(body, `"last_email_at":"`) {
		t.Errorf("setup: %d %s", code, body)
	}

	// The suggestion waits for approval; the tracker has not changed.
	code, body = do(t, client, http.MethodGet, ts.URL+"/api/email-suggestions", "")
	var list struct {
		Suggestions []store.EmailSuggestion `json:"suggestions"`
	}
	json.Unmarshal([]byte(body), &list)
	if code != 200 || len(list.Suggestions) != 1 || list.Suggestions[0].Status != "rejected" || list.Suggestions[0].JobID != ids[0] ||
		list.Suggestions[0].Current != "applied" || list.Suggestions[0].FromDomain != "us.greenhouse-mail.io" {
		t.Fatalf("suggestions: %d %s", code, body)
	}
	var stored int
	pool.QueryRow(ctx, `SELECT count(*) FROM email_suggestions WHERE user_id = $1 AND subject LIKE '%Unfortunately%'`, testUserID).Scan(&stored)
	if stored != 0 {
		t.Error("the body was stored")
	}
	// The same email again refreshes the one suggestion.
	post(view.Address, reject, "", now)
	if _, body = do(t, client, http.MethodGet, ts.URL+"/api/email-suggestions", ""); strings.Count(body, `"id":`) != 1 {
		t.Errorf("duplicate: %s", body)
	}
	sid := list.Suggestions[0].ID
	if code, _ := do(t, client, http.MethodPost, fmt.Sprintf("%s/api/email-suggestions/%d/accept", ts.URL, sid), ""); code != 204 {
		t.Fatalf("accept: %d", code)
	}
	var status string
	pool.QueryRow(ctx, `SELECT status FROM applications WHERE job_id = $1 AND user_id = $2`, ids[0], testUserID).Scan(&status)
	if status != "rejected" {
		t.Errorf("status after accepting: %q", status)
	}
	if code, _ := do(t, client, http.MethodPost, fmt.Sprintf("%s/api/email-suggestions/%d/dismiss", ts.URL, sid), ""); code != 404 {
		t.Errorf("deciding twice: %d", code)
	}

	// A new address retires the old one; turning it off retires both.
	old := view.Address
	_, body = do(t, client, http.MethodPost, ts.URL+"/api/inbound-address", "")
	json.Unmarshal([]byte(body), &view)
	if view.Address == old || post(old, reject, "", now) != 404 {
		t.Errorf("old address still works")
	}
	if code, _ := do(t, client, http.MethodDelete, ts.URL+"/api/inbound-address", ""); code != 200 || post(view.Address, reject, "", now) != 404 {
		t.Errorf("turned off: %d", code)
	}
}
