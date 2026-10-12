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

	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/store"
)

// birthday_sync copies birthdays from Google Contacts (People API,
// read-only) into birthdays.md.
//
// It uses the same config/token.json the Python version used (written by
// Google's OAuth flow): the access token is refreshed with the stored
// refresh token when it has expired, and the file is updated in place.
// No Google SDK — two plain HTTPS calls.

func init() { job.Register("birthday_sync", birthdaySync) }

const (
	peopleURL       = "https://people.googleapis.com/v1/people/me/connections"
	defaultTokenURI = "https://oauth2.googleapis.com/token"
	refreshMargin   = 4 * time.Minute // refresh a little before expiry, as google-auth does
)

func birthdaySync(c *job.Ctx) error {
	tokenPath := c.Path("google_token") // kronos.yml: paths.google_token (config/token.json, gitignored)
	if _, err := os.Stat(tokenPath); err != nil {
		return fmt.Errorf("Missing token: %s", tokenPath)
	}
	client := &http.Client{Timeout: 60 * time.Second}

	access, err := accessToken(c, client, tokenPath)
	if err != nil {
		return err
	}
	people, err := fetchBirthdays(c, client, access)
	if err != nil {
		return err
	}
	// An empty result is almost certainly an API problem – keep the existing file.
	if len(people) == 0 {
		return fmt.Errorf("Google returned 0 birthdays – kept existing birthdays.md")
	}

	root := store.NewMapping()
	store.Set(root, "type", store.Str("birthdays"))
	store.Set(root, "version", store.Int(1))
	store.Set(root, "last_sync", store.Date(c.Today))
	list := store.Seq()
	for _, p := range people {
		m := store.NewMapping()
		store.Set(m, "id", store.Str(p.id))
		store.Set(m, "name", store.Str(p.name))
		store.Set(m, "birthday", store.Quoted(p.birthday)) // '0000-03-14', like PyYAML
		list.Content = append(list.Content, m)
	}
	store.Set(root, "people", list)

	data, err := store.Encode(root)
	if err != nil {
		return err
	}
	if err := c.WriteFile(c.Path("birthdays"), data); err != nil { // kronos.yml: paths.birthdays
		return err
	}
	c.Log.OK("Synced %d birthday(s)", len(people))
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

type birthday struct{ id, name, birthday string }

// fetchBirthdays pages through all contacts and keeps those with a month and
// day, sorted by MM-DD (year 0000 when unknown).
func fetchBirthdays(c *job.Ctx, client *http.Client, access string) ([]birthday, error) {
	var people []birthday
	pageToken := ""
	for {
		q := url.Values{"personFields": {"names,birthdays"}, "pageSize": {"1000"}}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		req, _ := http.NewRequestWithContext(c, http.MethodGet, peopleURL+"?"+q.Encode(), nil)
		req.Header.Set("Authorization", "Bearer "+access)
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("People API: %v", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("People API: HTTP %d: %s", resp.StatusCode, snippet(body))
		}

		var page struct {
			Connections []struct {
				ResourceName string `json:"resourceName"`
				Names        []struct {
					DisplayName string `json:"displayName"`
				} `json:"names"`
				Birthdays []struct {
					Date struct {
						Year  int `json:"year"`
						Month int `json:"month"`
						Day   int `json:"day"`
					} `json:"date"`
				} `json:"birthdays"`
			} `json:"connections"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("People API: unexpected response: %v", err)
		}

		for _, p := range page.Connections {
			if len(p.Birthdays) == 0 {
				continue
			}
			d := p.Birthdays[0].Date
			if d.Month == 0 || d.Day == 0 {
				continue
			}
			name := "Unknown"
			if len(p.Names) > 0 {
				name = p.Names[0].DisplayName
			}
			people = append(people, birthday{
				id:       p.ResourceName,
				name:     name,
				birthday: fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day),
			})
		}

		if pageToken = page.NextPageToken; pageToken == "" {
			break
		}
	}
	sort.SliceStable(people, func(i, j int) bool { return people[i].birthday[5:] < people[j].birthday[5:] })
	return people, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
