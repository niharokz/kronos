package jobs

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGoogle serves a calendar list and per-calendar events like the Calendar API.
func fakeGoogle(t *testing.T, events map[string]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/users/me/calendarList" {
			_, _ = w.Write([]byte(`{"items":[
				{"id":"me@example.com","summary":"me@example.com","summaryOverride":"Personal"},
				{"id":"en.indian#holiday@group.v.calendar.google.com","summary":"Holidays in India"},
				{"id":"cricket@group.calendar.google.com","summary":"Cricket"}]}`))
			return
		}
		for id, body := range events {
			if r.URL.Path == "/calendars/"+id+"/events" {
				_, _ = w.Write([]byte(body))
				return
			}
		}
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	old := calendarAPI
	calendarAPI = srv.URL
	t.Cleanup(func() { calendarAPI = old })
}

func validToken(t *testing.T, dir string) {
	t.Helper()
	exp := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	put(t, filepath.Join(dir, "token.json"), `{"token":"good","expiry":"`+exp+`"}`)
}

func TestCalendarSync(t *testing.T) {
	c, dir, _ := testCtx(t, "2026-10-17", false)
	c.Cfg.Paths["google_token"] = filepath.Join(dir, "token.json")
	validToken(t, dir)
	fakeGoogle(t, map[string]string{
		"me@example.com": `{"items":[
			{"id":"b1_20261020","summary":"Asha's birthday","eventType":"birthday","start":{"date":"2026-10-20"},"end":{"date":"2026-10-21"}},
			{"id":"m1","summary":"Dentist","location":"Main Street","start":{"dateTime":"2026-10-18T10:30:00+05:30"},"end":{"dateTime":"2026-10-18T11:00:00+05:30"}},
			{"id":"t1","summary":"Goa trip","start":{"date":"2026-10-24"},"end":{"date":"2026-10-27"}},
			{"id":"a1","summary":"Ravi & Meera's anniversary","eventType":"birthday","birthdayProperties":{"type":"anniversary"},"start":{"date":"2026-10-22"},"end":{"date":"2026-10-23"}},
			{"id":"f1","summary":"Flight to Goa","eventType":"fromGmail","start":{"dateTime":"2026-10-24T06:10:00+05:30"},"end":{"dateTime":"2026-10-24T07:40:00+05:30"}},
			{"id":"x1","status":"cancelled","summary":"Dropped","start":{"date":"2026-10-19"},"end":{"date":"2026-10-20"}}]}`,
		"cricket@group.calendar.google.com": `{"items":[
			{"id":"c1","summary":"IND v AUS","start":{"date":"2026-10-25"},"end":{"date":"2026-10-26"}}]}`,
		"en.indian#holiday@group.v.calendar.google.com": `{"items":[
			{"id":"h1","summary":"Diwali","description":"Public holiday","start":{"date":"2026-11-08"},"end":{"date":"2026-11-09"}},
			{"id":"h2","summary":"Karva Chauth","description":"Observance\nTo hide observances, go to Google Calendar Settings","start":{"date":"2026-10-29"},"end":{"date":"2026-10-30"}}]}`,
	})
	oldLocal := time.Local
	time.Local = time.FixedZone("IST", 5*3600+1800)
	c.Job.Options = map[string]any{"categories": map[string]any{"Cricket": "Sport"}}
	t.Cleanup(func() { time.Local = oldLocal })
	if err := calendarSync(c); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "calendar.md"))
	got := string(raw)
	for _, want := range []string{
		"type: calendar", "last_sync: '2026-10-17'",
		"title: Dentist\n    date: '2026-10-18'\n    time: '10:30'\n    category: event\n    calendar: Personal\n    location: Main Street",
		"title: Asha's birthday\n    date: '2026-10-20'\n    category: birthday",
		"title: Goa trip\n    date: '2026-10-24'\n    end: '2026-10-26'",
		"title: Diwali\n    date: '2026-11-08'\n    category: holiday\n    calendar: Holidays in India",
		"title: Karva Chauth\n    date: '2026-10-29'\n    category: festival",
		"title: Ravi & Meera's anniversary\n    date: '2026-10-22'\n    category: anniversary",
		"title: Flight to Goa\n    date: '2026-10-24'\n    time: '06:10'\n    category: reservation",
		"title: IND v AUS\n    date: '2026-10-25'\n    category: sport\n    calendar: Cricket",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("calendar.md lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Dropped") || strings.Index(got, "Dentist") > strings.Index(got, "Diwali") {
		t.Errorf("cancelled kept or not sorted:\n%s", got)
	}

	// The agenda picks it up.
	if err := dailyUpdate(c); err != nil {
		t.Fatal(err)
	}
	agenda, _ := os.ReadFile(filepath.Join(dir, "daily.md"))
	if !strings.Contains(string(agenda), "BIRTHDAY- Asha (2026-10-20)") || !strings.Contains(string(agenda), "CALENDAR- Dentist 10:30 (2026-10-18)") {
		t.Errorf("daily.md:\n%s", agenda)
	}
}

// A failing calendar or an empty result never replaces the existing file.
func TestCalendarSyncKeepsFileOnProblems(t *testing.T) {
	c, dir, _ := testCtx(t, "2026-10-17", false)
	c.Cfg.Paths["google_token"] = filepath.Join(dir, "token.json")
	validToken(t, dir)
	before := "events:\n- {title: keep me, date: '2026-10-18'}\n"
	put(t, filepath.Join(dir, "calendar.md"), before)

	fakeGoogle(t, map[string]string{"me@example.com": `{"items":[]}`}) // holidays calendar → 404
	if err := calendarSync(c); err == nil || !strings.Contains(err.Error(), "Holidays in India") {
		t.Fatalf("err = %v", err)
	}
	c.Job.Options = map[string]any{"exclude": []any{"Holidays in India", "cricket@group.calendar.google.com"}}
	if err := calendarSync(c); err == nil || !strings.Contains(err.Error(), "0 events") {
		t.Fatalf("err = %v", err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "calendar.md")); string(raw) != before {
		t.Error("calendar.md was replaced")
	}
}
