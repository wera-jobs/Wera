package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"wera/internal/store"
)

func TestTrackerDetailsAndEvents(t *testing.T) {
	ts, pool := testServer(t, nil)
	ctx := context.Background()
	jobs, err := store.ListJobs(ctx, pool, testUserID, store.JobQuery{Limit: 2})
	if err != nil || len(jobs) < 2 {
		t.Skipf("need two jobs (err=%v)", err)
	}
	id, other := jobs[0].ID, jobs[1].ID
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM applications WHERE user_id = $1 AND job_id = ANY($2)`, testUserID, []int64{id, other})
	})
	call := func(method, suffix, body string, want int) []byte {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, jobURL(ts, id, suffix), rd)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s %s: %d %s, want %d", method, suffix, body, resp.StatusCode, out, want)
		}
		return out
	}

	// Nothing to add details to before there's an application.
	call(http.MethodPatch, "/application", `{"next_step":"Email them"}`, 404)
	call(http.MethodPost, "/events", `{"at":"2026-12-01T15:00:00Z","kind":"interview"}`, 404)
	call(http.MethodPut, "/application", `{"status":"interviewing","notes":"kept"}`, 200)

	next := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	var job store.JobView
	_ = json.Unmarshal(call(http.MethodPatch, "/application",
		`{"next_step":"  Send the take-home  ","next_step_at":"`+next.Format(time.RFC3339)+`","contact":{"name":"Sam Recruiter","role":"Recruiter","link":"sam@example.com"}}`, 200), &job)
	if job.NextStep == nil || *job.NextStep != "Send the take-home" || job.NextStepAt == nil || !job.NextStepAt.Equal(next) {
		t.Errorf("next step: %v %v", job.NextStep, job.NextStepAt)
	}
	if job.Contact == nil || job.Contact.Name != "Sam Recruiter" || job.Contact.Link != "sam@example.com" {
		t.Errorf("contact: %+v", job.Contact)
	}
	if job.AppNotes == nil || *job.AppNotes != "kept" || job.AppStatus == nil || *job.AppStatus != "interviewing" {
		t.Errorf("details touched the status or notes: %v %v", job.AppStatus, job.AppNotes)
	}
	// Fields left out stay; "" and clear_contact clear.
	_ = json.Unmarshal(call(http.MethodPatch, "/application", `{"next_step_at":""}`, 200), &job)
	if job.NextStepAt != nil || job.NextStep == nil || job.Contact == nil {
		t.Errorf("clearing the date: %v %v %+v", job.NextStepAt, job.NextStep, job.Contact)
	}
	_ = json.Unmarshal(call(http.MethodPatch, "/application", `{"clear_contact":true,"next_step":""}`, 200), &job)
	if job.Contact != nil || job.NextStep != nil {
		t.Errorf("clearing: %+v %v", job.Contact, job.NextStep)
	}
	call(http.MethodPatch, "/application", `{"next_step_at":"tomorrow"}`, 400)
	call(http.MethodPatch, "/application", `{"next_step":"`+strings.Repeat("x", 201)+`"}`, 400)

	// Events.
	at := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	call(http.MethodPost, "/events", `{"at":"`+at.Format(time.RFC3339)+`","kind":"coffee"}`, 400)
	var ev store.Event
	_ = json.Unmarshal(call(http.MethodPost, "/events", `{"at":"`+at.Format(time.RFC3339)+`","kind":"onsite","with":"The team"}`, 201), &ev)
	if ev.ID == 0 || ev.Kind != "onsite" || ev.With != "The team" {
		t.Fatalf("event: %+v", ev)
	}
	var list struct{ Events []store.Event }
	_ = json.Unmarshal(call(http.MethodGet, "/events", "", 200), &list)
	if len(list.Events) != 1 || list.Events[0].ID != ev.ID {
		t.Errorf("job events: %+v", list.Events)
	}
	upcoming := func() []store.Event {
		t.Helper()
		resp, err := client.Get(ts.URL + "/api/events?days=7")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct{ Events []store.Event }
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out.Events
	}
	if got := upcoming(); len(got) != 1 || got[0].Title == "" || got[0].Company == "" {
		t.Errorf("upcoming: %+v", got)
	}
	// Due reminders see the event and a next step, and not after archiving.
	call(http.MethodPatch, "/application", `{"next_step":"Follow up","next_step_at":"`+next.Format(time.RFC3339)+`"}`, 200)
	due := func() int {
		t.Helper()
		rs, err := store.DueReminders(ctx, pool, time.Now(), time.Now().Add(96*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, r := range rs {
			if r.UserID == testUserID && r.JobID == id {
				n++
			}
		}
		return n
	}
	if n := due(); n != 2 {
		t.Errorf("due reminders: %d, want the event and the next step", n)
	}

	// Archiving takes it off the board and out of reminders, keeping its history.
	_ = json.Unmarshal(call(http.MethodPatch, "/application", `{"archived":true}`, 200), &job)
	if job.ArchivedAt == nil {
		t.Error("not archived")
	}
	board, _ := store.ListJobs(ctx, pool, testUserID, store.JobQuery{Status: "interviewing"})
	for _, j := range board {
		if j.ID == id {
			t.Error("an archived application is still on the board")
		}
	}
	archived, _ := store.ListJobs(ctx, pool, testUserID, store.JobQuery{Archived: true})
	if len(archived) != 1 || archived[0].ID != id {
		t.Errorf("archived list: %d", len(archived))
	}
	if n, ev := due(), upcoming(); n != 0 || len(ev) != 0 {
		t.Errorf("archived still reminds: %d, %d", n, len(ev))
	}
	_ = json.Unmarshal(call(http.MethodPatch, "/application", `{"archived":false}`, 200), &job)
	if job.ArchivedAt != nil || job.AppStatus == nil || *job.AppStatus != "interviewing" {
		t.Errorf("restore: %v %v", job.ArchivedAt, job.AppStatus)
	}

	// Someone's event id under another job is not found; deleting works.
	req, _ := http.NewRequest(http.MethodDelete, jobURL(ts, other, "/events/")+strconv.FormatInt(ev.ID, 10), nil)
	if resp, _ := client.Do(req); resp.StatusCode != 404 {
		t.Errorf("delete under another job: %d", resp.StatusCode)
	}
	call(http.MethodDelete, "/events/"+strconv.FormatInt(ev.ID, 10), "", 204)
	if got := upcoming(); len(got) != 0 {
		t.Errorf("after delete: %+v", got)
	}
}
