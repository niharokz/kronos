package jobs

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gitlab.com/niharokz/kronos/internal/dates"
	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/store"
)

// calendar_sync copies events from every Google Calendar you can see —
// your own calendar (incl. contact birthdays), holidays, shared and
// subscribed calendars — into calendar.md (Google Calendar API, read-only).
//
// Each repeat is stored as its own dated entry, from past_days ago to
// ahead_days ahead, so other jobs never have to expand rules.
// The file is rewritten on every run; Google is the source of truth.
//
// Token: config/token.json from Google's OAuth flow (scope calendar.readonly);
// the access token is refreshed with the stored refresh token and the file is
// updated in place. No Google SDK — plain HTTPS calls.
//
// Every entry gets one category: birthday, anniversary, holiday (public),
// festival (observance), reservation (from Gmail), out_of_office, focus_time,
// working_location or event — or the one you set for its calendar.
//
// options: past_days (7)    — keep events this many days back
//          ahead_days (365) — fetch events this many days ahead
//          exclude ([])     — calendar names or ids to skip
//          categories ({})  — calendar name or id → category, e.g. { Family: family }

func init() { job.Register("calendar_sync", calendarSync) }

const (
	defaultTokenURI = "https://oauth2.googleapis.com/token"
	refreshMargin   = 4 * time.Minute // refresh a little before expiry, as google-auth does
)

var calendarAPI = "https://www.googleapis.com/calendar/v3" // var so tests can point it at a fake server

func calendarSync(c *job.Ctx) error {
	tokenPath := c.Path("google_token") // kronos.yml: paths.google_token (config/token.json, gitignored)
	if _, err := os.Stat(tokenPath); err != nil {
		return fmt.Errorf("Missing token: %s", tokenPath)
	}
	client := &http.Client{Timeout: 60 * time.Second}

	access, err := accessToken(c, client, tokenPath)
	if err != nil {
		return err
	}
	cals, err := fetchCalendars(c, client, access)
	if err != nil {
		return err
	}

	skip := map[string]bool{}
	for _, s := range c.Strings("exclude", nil) { // kronos.yml: jobs.calendar_sync.options.exclude
		skip[strings.ToLower(s)] = true
	}
	own := map[string]string{}                                     // calendar → category set in kronos.yml
	if m, ok := c.Job.Options["categories"].(map[string]any); ok { // kronos.yml: jobs.calendar_sync.options.categories
		for k, v := range m {
			own[strings.ToLower(k)] = strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))
		}
	}
	from := c.Today.AddDate(0, 0, -c.Int("past_days", 7))   // kronos.yml: jobs.calendar_sync.options.past_days
	to := c.Today.AddDate(0, 0, c.Int("ahead_days", 365)+1) // kronos.yml: jobs.calendar_sync.options.ahead_days

	var events []calEvent
	used := 0
	for _, cal := range cals {
		if skip[strings.ToLower(cal.name)] || skip[strings.ToLower(cal.id)] {
			continue
		}
		got, err := fetchEvents(c, client, access, cal, from, to)
		if err != nil {
			return err // any failure keeps the old file: never write half a calendar
		}
		cat := own[strings.ToLower(cal.name)]
		if cat == "" {
			cat = own[strings.ToLower(cal.id)]
		}
		if cat != "" {
			for i := range got {
				got[i].category = cat // your category for this calendar wins
			}
		}
		events = append(events, got...)
		used++
	}
	// Nothing at all (not even a holiday) is almost certainly an API problem – keep the existing file.
	if len(events) == 0 {
		return fmt.Errorf("Google returned 0 events from %d calendar(s) – kept existing calendar.md", used)
	}
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.date != b.date {
			return a.date < b.date
		}
		if a.time != b.time {
			return a.time < b.time // all-day ("") first
		}
		return a.title < b.title
	})

	root := store.NewMapping()
	store.Set(root, "type", store.Str("calendar"))
	store.Set(root, "version", store.Int(1))
	store.Set(root, "last_sync", store.Date(c.Today))
	list := store.Seq()
	for _, e := range events {
		m := store.NewMapping()
		store.Set(m, "id", store.Str(e.id))
		store.Set(m, "title", store.Str(e.title))
		store.Set(m, "date", store.Quoted(e.date))
		if e.end != "" {
			store.Set(m, "end", store.Quoted(e.end)) // last day, inclusive (multi-day events only)
		}
		if e.time != "" {
			store.Set(m, "time", store.Quoted(e.time)) // local start time; absent = all day
		}
		store.Set(m, "category", store.Str(e.category))
		store.Set(m, "calendar", store.Str(e.calendar))
		if e.location != "" {
			store.Set(m, "location", store.Str(e.location))
		}
		list.Content = append(list.Content, m)
	}
	store.Set(root, "events", list)

	data, err := store.Encode(root)
	if err != nil {
		return err
	}
	if err := c.WriteFile(c.Path("calendar"), data); err != nil { // kronos.yml: paths.calendar
		return err
	}
	c.Log.OK("Synced %d event(s) from %d calendar(s), %s – %s", len(events), used, dates.Format(from), dates.Format(to.AddDate(0, 0, -1)))
	return nil
}

// accessToken returns a valid access token from token.json, refreshing it
// (and saving the file) when it is missing or about to expire.
func accessToken(c *job.Ctx, client *http.Client, path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("Cannot read token: %w", err)
	}
	tok := map[string]any{}
	if err := json.Unmarshal(raw, &tok); err != nil {
		return "", fmt.Errorf("Invalid token file %s: %v", path, err)
	}
	str := func(k string) string { s, _ := tok[k].(string); return s }

	if access := str("token"); access != "" {
		if exp, ok := parseExpiry(str("expiry")); ok && time.Now().UTC().Before(exp.Add(-refreshMargin)) {
			return access, nil
		}
	}

	refresh, id, secret := str("refresh_token"), str("client_id"), str("client_secret")
	if refresh == "" || id == "" || secret == "" {
		return "", fmt.Errorf("Token expired and %s has no refresh_token/client_id/client_secret – rerun the OAuth flow", path)
	}
	uri := str("token_uri")
	if uri == "" {
		uri = defaultTokenURI
	}
	req, _ := http.NewRequestWithContext(c, http.MethodPost, uri, strings.NewReader(url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
		"client_id":     {id},
		"client_secret": {secret},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Token refresh failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		hint := ""
		if strings.Contains(string(body), "invalid_grant") { // revoked, or expired (OAuth app still in Testing mode)
			hint = " – refresh token no longer valid: regenerate " + filepath.Base(path) + " (see README, Google OAuth)"
		}
		return "", fmt.Errorf("Token refresh failed: HTTP %d: %s%s", resp.StatusCode, snippet(body), hint)
	}
	var r struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.AccessToken == "" {
		return "", fmt.Errorf("Token refresh failed: unexpected response: %s", snippet(body))
	}

	tok["token"] = r.AccessToken
	tok["expiry"] = time.Now().UTC().Add(time.Duration(r.ExpiresIn) * time.Second).Format("2006-01-02T15:04:05.000000Z")
	if !c.DryRun { // never print the token in a dry run
		out, _ := json.Marshal(tok)
		if err := store.AtomicWritePerm(path, out, 0o600); err != nil {
			c.Log.Warn("Refreshed token not saved: %v", err)
		}
	}
	return r.AccessToken, nil
}

func parseExpiry(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

type calendar struct{ id, name string }

type calEvent struct{ id, title, date, end, time, category, calendar, location string }

// googleGet GETs one Calendar API page into out.
func googleGet(c *job.Ctx, client *http.Client, access, path string, q url.Values, out any) error {
	req, _ := http.NewRequestWithContext(c, http.MethodGet, calendarAPI+path+"?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Calendar API: %v", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		hint := ""
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized { // wrong scope or API not enabled
			hint = " – is the Calendar API enabled and token.json made with calendar.readonly?"
		}
		return fmt.Errorf("Calendar API: HTTP %d: %s%s", resp.StatusCode, snippet(body), hint)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("Calendar API: unexpected response: %v", err)
	}
	return nil
}

// fetchCalendars lists every calendar shown in Google Calendar (hidden ones are skipped).
func fetchCalendars(c *job.Ctx, client *http.Client, access string) ([]calendar, error) {
	var cals []calendar
	pageToken := ""
	for {
		q := url.Values{"maxResults": {"250"}}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		var page struct {
			Items []struct {
				ID              string `json:"id"`
				Summary         string `json:"summary"`
				SummaryOverride string `json:"summaryOverride"`
			} `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := googleGet(c, client, access, "/users/me/calendarList", q, &page); err != nil {
			return nil, err
		}
		for _, it := range page.Items {
			name := it.SummaryOverride // your own name for it, if you renamed it
			if name == "" {
				name = it.Summary
			}
			cals = append(cals, calendar{id: it.ID, name: name})
		}
		if pageToken = page.NextPageToken; pageToken == "" {
			break
		}
	}
	return cals, nil
}

// fetchEvents returns one calendar's events in [from, to), repeats expanded.
func fetchEvents(c *job.Ctx, client *http.Client, access string, cal calendar, from, to time.Time) ([]calEvent, error) {
	var out []calEvent
	pageToken := ""
	for {
		q := url.Values{
			"singleEvents": {"true"}, // one entry per repeat
			"orderBy":      {"startTime"},
			"timeMin":      {from.Format(time.RFC3339)},
			"timeMax":      {to.Format(time.RFC3339)},
			"maxResults":   {"2500"},
		}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		var page struct {
			Items []struct {
				ID        string `json:"id"`
				Status    string `json:"status"`
				Summary   string `json:"summary"`
				Location  string `json:"location"`
				EventType string `json:"eventType"`
				// Google's holiday calendars say "Public holiday" or "Observance" here
				Description        string `json:"description"`
				BirthdayProperties struct {
					Type string `json:"type"` // birthday | anniversary | self | custom | other
				} `json:"birthdayProperties"`
				Start gTime `json:"start"`
				End   gTime `json:"end"`
			} `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := googleGet(c, client, access, "/calendars/"+url.PathEscape(cal.id)+"/events", q, &page); err != nil {
			return nil, fmt.Errorf("%v (calendar %q)", err, cal.name)
		}
		for _, it := range page.Items {
			if it.Status == "cancelled" {
				continue
			}
			e := calEvent{id: it.ID, title: strings.TrimSpace(it.Summary), calendar: cal.name, location: strings.TrimSpace(it.Location)}
			if e.title == "" {
				e.title = "(no title)"
			}
			start, timed, ok := it.Start.local()
			if !ok {
				continue
			}
			e.date = dates.Format(start)
			if timed {
				e.time = start.Format("15:04")
			}
			if end, _, ok := it.End.local(); ok {
				last := end
				if !timed || (end.Hour() == 0 && end.Minute() == 0 && end.After(start)) {
					last = end.AddDate(0, 0, -1) // Google's end is exclusive
				}
				if l := dates.Format(last); l > e.date {
					e.end = l
				}
			}
			e.category = calCategory(it.EventType, it.BirthdayProperties.Type, it.Description, cal.id)
			out = append(out, e)
		}
		if pageToken = page.NextPageToken; pageToken == "" {
			break
		}
	}
	return out, nil
}

// gTime is Google's start/end: date (all day) or dateTime (timed).
type gTime struct {
	Date     string `json:"date"`
	DateTime string `json:"dateTime"`
}

// local returns the moment in the container's time zone and whether it has a time of day.
func (g gTime) local() (time.Time, bool, bool) {
	if g.DateTime != "" {
		t, err := time.Parse(time.RFC3339, g.DateTime)
		return t.In(time.Local), true, err == nil
	}
	d, ok := dates.ParseISO(g.Date)
	return d, false, ok
}

// calCategory sorts an entry by what Google says it is.
func calCategory(eventType, birthdayType, description, calendarID string) string {
	switch {
	case eventType == "birthday" && birthdayType == "anniversary":
		return "anniversary"
	case eventType == "birthday" || strings.HasPrefix(calendarID, "addressbook#"):
		return "birthday"
	case strings.Contains(calendarID, "#holiday@"):
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(description)), "observance") {
			return "festival" // observances: festivals and days that aren't public holidays
		}
		return "holiday"
	case eventType == "fromGmail":
		return "reservation" // flights, hotels, tickets found in Gmail
	case eventType == "outOfOffice":
		return "out_of_office"
	case eventType == "focusTime":
		return "focus_time"
	case eventType == "workingLocation":
		return "working_location"
	}
	return "event"
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
