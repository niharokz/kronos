package kshan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/job"
	"gitlab.com/niharokz/kronos/internal/store"
)

// kshan_weather writes the phone's weather file (Kshan Clock widget) from open-meteo (no key)
// for the place in Kshan's location.md. Kshan never goes online for weather.
//
// Runs at start, hourly, and when location.md changes (kronos.yml: run_at_start + watch).
// Skips the network call while location.md is unchanged and weather.md is younger than
// `every` − 10 min (default 60 → 50 min); a location.md with `reason: tap` always fetches.
//
// Options:
//   location_file  logs/kshan/weather/location.md   Kshan writes it (updated, lat, lon, accuracy_m, place, reason); never written here
//   weather_file   logs/kshan/weather/weather.md    relative = in the vault (paths.vault)
//   hours          6                                next whole hours listed
//   every          60                               minutes between refreshes (with the hourly schedule)
//   lat, lon, place  (none)                         used when location.md is missing or unreadable;
//                                                   set them from .env: ${KRONOS_WEATHER_LAT} … (no fallback → skip)
//   timezone       auto                             open-meteo's local time for `next` (auto = the place's zone)
//   fail_hours     6                                failed fetches in a row for this long → one alert
//   url            https://api.open-meteo.com/v1/forecast
//
// A failed fetch keeps the old file (WARN); after fail_hours of failures one FAIL + alert.

func init() { job.Register("kshan_weather", kshanWeather) }

const openMeteo = "https://api.open-meteo.com/v1/forecast"

// Location is where to fetch the weather for.
type Location struct {
	Lat, Lon float64
	Place    string
	Reason   string // Kshan's reason for writing it, e.g. "tap"
	Fallback bool   // from the options, not location.md
}

func kshanWeather(c *job.Ctx) error {
	locPath := c.VaultFile("location_file", "logs/kshan/weather/location.md") // kronos.yml: jobs.kshan_weather.options
	path := c.VaultFile("weather_file", "logs/kshan/weather/weather.md")
	why := runReason(c)

	fallback, hasFallback := fallbackLocation(c)
	loc, problem := readLocation(locPath, fallback)
	if loc.Fallback && !hasFallback { // no location.md and no lat/lon in the options: nothing to fetch for
		why := problem
		if why == "" {
			why = filepath.Base(locPath) + " not written by Kshan yet"
		}
		c.Log.Skip("%s and no fallback lat/lon (.env KRONOS_WEATHER_LAT/LON) – old file kept", why)
		return nil
	}
	if problem != "" {
		c.Log.Warn("%s – using %s", problem, loc.Place)
	}
	if fresh, age := isFresh(path, locPath, loc, c.Int("every", 60), c.Now); fresh && !c.DryRun {
		c.Log.Skip("%s (%.4g, %.4g): weather.md is %d min old, location unchanged – %s", loc.Place, loc.Lat, loc.Lon, int(age.Minutes()), why)
		return nil
	}

	f, err := fetchForecast(c, loc)
	if err != nil {
		return weatherFailed(c, err)
	}
	data, err := weatherYAML(f, loc, c.Int("hours", 6), c.Now)
	if err != nil {
		return weatherFailed(c, err)
	}
	weatherRecovered(c)
	if err := c.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	if err := c.WriteFile(path, data); err != nil {
		return err
	}
	c.Log.OK("%s (%.4g, %.4g): %.1f °C, %s – %s", loc.Place, loc.Lat, loc.Lon, round1(f.Current.Temp), f.Current.text(), why)
	return nil
}

// runReason says why this run happened, for the log line.
func runReason(c *job.Ctx) string {
	switch r := c.Reason; {
	case r == "start":
		return "startup"
	case strings.HasPrefix(r, "changed:"):
		return "location changed"
	case r == "":
		return "hourly"
	default:
		return r // manual, catch-up …
	}
}

// fallbackLocation is options lat/lon/place (from .env via ${KRONOS_WEATHER_*}); ok = a valid lat/lon is set.
func fallbackLocation(c *job.Ctx) (Location, bool) {
	place := strings.TrimSpace(c.String("place", "")) // kronos.yml: jobs.kshan_weather.options.lat/lon/place
	if place == "" {
		place = "Home"
	}
	loc := Location{Place: place, Fallback: true}
	lat, errLat := strconv.ParseFloat(strings.TrimSpace(c.String("lat", "")), 64)
	lon, errLon := strconv.ParseFloat(strings.TrimSpace(c.String("lon", "")), 64)
	if errLat != nil || errLon != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return loc, false
	}
	loc.Lat, loc.Lon = lat, lon
	return loc, true
}

// readLocation reads Kshan's location.md; missing or unusable → the fallback and a reason why.
func readLocation(path string, fallback Location) (Location, string) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fallback, ""
	}
	if err != nil {
		return fallback, fmt.Sprintf("%s: %v", filepath.Base(path), err)
	}
	var doc struct {
		Lat    *float64 `yaml:"lat"`
		Lon    *float64 `yaml:"lon"`
		Place  string   `yaml:"place"`
		Reason string   `yaml:"reason"`
	}
	if err := yaml.Unmarshal(stripFrontmatter(raw), &doc); err != nil {
		return fallback, fmt.Sprintf("%s unreadable (%v)", filepath.Base(path), err)
	}
	if doc.Lat == nil || doc.Lon == nil || *doc.Lat < -90 || *doc.Lat > 90 || *doc.Lon < -180 || *doc.Lon > 180 {
		return fallback, filepath.Base(path) + " has no valid lat/lon"
	}
	loc := Location{Lat: *doc.Lat, Lon: *doc.Lon, Place: strings.TrimSpace(doc.Place), Reason: strings.TrimSpace(doc.Reason)}
	if loc.Place == "" {
		loc.Place = fallback.Place
	}
	return loc, ""
}

// stripFrontmatter accepts both plain YAML and a markdown file with a --- frontmatter block.
func stripFrontmatter(raw []byte) []byte {
	s := strings.TrimPrefix(string(raw), "\ufeff")
	if !strings.HasPrefix(s, "---") {
		return []byte(s)
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(s, "---"), "\n")
	if i := strings.Index(rest, "\n---"); i >= 0 {
		return []byte(rest[:i+1])
	}
	return []byte(rest)
}

// isFresh: no fetch needed — weather.md is younger than every−10 min, location.md is not newer
// than it, and the location isn't a tap. Returns weather.md's age.
func isFresh(weatherPath, locPath string, loc Location, every int, now time.Time) (bool, time.Duration) {
	w, err := os.Stat(weatherPath)
	if err != nil {
		return false, 0
	}
	age := now.Sub(w.ModTime())
	if loc.Reason == "tap" || age < 0 || age >= time.Duration(every-10)*time.Minute {
		return false, age
	}
	if l, err := os.Stat(locPath); err == nil && l.ModTime().After(w.ModTime()) {
		return false, age // location.md changed since the last fetch
	}
	return true, age
}

// ── open-meteo response ─────────────────────────────────────────────────

type forecast struct {
	Current current `json:"current"`
	Hourly  struct {
		Time []string   `json:"time"`
		Temp []*float64 `json:"temperature_2m"`
		Code []*int     `json:"weather_code"`
		Rain []*float64 `json:"precipitation_probability"`
	} `json:"hourly"`
	Daily struct {
		Min  []*float64 `json:"temperature_2m_min"`
		Max  []*float64 `json:"temperature_2m_max"`
		Rain []*float64 `json:"precipitation_probability_max"`
	} `json:"daily"`
}

type current struct {
	Time     string   `json:"time"` // local, "2026-11-12T21:15"
	Temp     *float64 `json:"temperature_2m"`
	Feels    *float64 `json:"apparent_temperature"`
	Code     *int     `json:"weather_code"`
	IsDay    *int     `json:"is_day"`
	Humidity *float64 `json:"relative_humidity_2m"`
}

func (c current) text() string { return wmoText(c.Code) }

func fetchForecast(c *job.Ctx, loc Location) (forecast, error) {
	var f forecast
	q := url.Values{}
	q.Set("latitude", coord(loc.Lat))
	q.Set("longitude", coord(loc.Lon))
	q.Set("current", "temperature_2m,apparent_temperature,weather_code,is_day,relative_humidity_2m")
	q.Set("hourly", "temperature_2m,weather_code,precipitation_probability")
	q.Set("daily", "temperature_2m_min,temperature_2m_max,precipitation_probability_max")
	q.Set("timezone", c.String("timezone", "auto")) // kronos.yml: jobs.kshan_weather.options.timezone
	q.Set("forecast_days", "2")
	u := c.String("url", openMeteo) + "?" + q.Encode()

	req, err := http.NewRequestWithContext(c, http.MethodGet, u, nil)
	if err != nil {
		return f, err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return f, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return f, err
	}
	if resp.StatusCode != http.StatusOK {
		return f, fmt.Errorf("open-meteo: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body[:min(len(body), 200)])))
	}
	if err := json.Unmarshal(body, &f); err != nil {
		return f, fmt.Errorf("open-meteo: %v", err)
	}
	if f.Current.Time == "" || len(f.Hourly.Time) == 0 {
		return f, errors.New("open-meteo: no current or hourly data")
	}
	return f, nil
}

// ── weather.md ──────────────────────────────────────────────────────────

const weatherHeader = "# Kshan weather - written by Kronos (open-meteo). YAML.\n"

// weatherYAML renders the file: dates/times single-quoted, numbers to 1 decimal, null when missing.
func weatherYAML(f forecast, loc Location, hours int, now time.Time) ([]byte, error) {
	root := store.NewMapping()
	store.Set(root, "updated", store.Quoted(now.Format("2006-01-02 15:04:05")))
	store.Set(root, "place", store.Str(loc.Place))
	store.Set(root, "lat", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: coord(loc.Lat)})
	store.Set(root, "lon", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: coord(loc.Lon)})

	cur := flowMap(
		"temp_c", numNode(f.Current.Temp),
		"feels_c", numNode(f.Current.Feels),
		"code", intNode(f.Current.Code),
		"text", store.Str(f.Current.text()),
		"is_day", boolNode(f.Current.IsDay),
		"humidity", numNode(f.Current.Humidity),
	)
	store.Set(root, "now", cur)
	store.Set(root, "today", flowMap(
		"min_c", numNode(at(f.Daily.Min, 0)),
		"max_c", numNode(at(f.Daily.Max, 0)),
		"rain_pct", numNode(at(f.Daily.Rain, 0)),
	))

	next := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for i, t := range f.Hourly.Time {
		if len(next.Content) >= hours {
			break
		}
		if t <= f.Current.Time { // whole hours after now only
			continue
		}
		hh := t
		if k := strings.IndexByte(t, 'T'); k >= 0 && len(t) >= k+3 {
			hh = t[k+1:k+3] + ":00"
		}
		next.Content = append(next.Content, flowMap(
			"time", store.Quoted(hh),
			"temp_c", numNode(at(f.Hourly.Temp, i)),
			"code", intNode(at(f.Hourly.Code, i)),
			"rain_pct", numNode(at(f.Hourly.Rain, i)),
		))
	}
	store.Set(root, "next", next)
	keyNode(root, "next").LineComment = fmt.Sprintf("# the next %d whole hours after now, nearest first", hours)

	var b bytes.Buffer
	b.WriteString(weatherHeader)
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// wmoText is the word for a WMO weather code.
func wmoText(code *int) string {
	if code == nil {
		return "—"
	}
	switch c := *code; {
	case c == 0:
		return "Clear"
	case c == 1:
		return "Mostly clear"
	case c == 2:
		return "Partly cloudy"
	case c == 3:
		return "Cloudy"
	case c == 45 || c == 48:
		return "Fog"
	case c >= 51 && c <= 57:
		return "Drizzle"
	case c >= 61 && c <= 67:
		return "Rain"
	case c >= 80 && c <= 82:
		return "Showers"
	case (c >= 71 && c <= 77) || c == 85 || c == 86:
		return "Snow"
	case c >= 95 && c <= 99:
		return "Storm"
	}
	return "—"
}

func flowMap(kv ...any) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
	for i := 0; i+1 < len(kv); i += 2 {
		store.Set(m, kv[i].(string), kv[i+1].(*yaml.Node))
	}
	return m
}

func keyNode(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i]
		}
	}
	return &yaml.Node{}
}

var null = func() *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"} }

// numNode: one decimal place (24 → 24.0, 23.46 → 23.5).
func numNode(v *float64) *yaml.Node {
	if v == nil {
		return null()
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(round1(v), 'f', 1, 64)}
}

func intNode(v *int) *yaml.Node {
	if v == nil {
		return null()
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(*v)}
}

func boolNode(v *int) *yaml.Node {
	if v == nil {
		return null()
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(*v == 1)}
}

// coord writes a latitude/longitude as given (up to 6 decimals, no trailing zeros).
func coord(v float64) string {
	s := strconv.FormatFloat(math.Round(v*1e6)/1e6, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func round1(v *float64) float64 {
	if v == nil {
		return 0
	}
	return math.Round(*v*10) / 10
}

func at[T any](list []*T, i int) *T {
	if i < len(list) {
		return list[i]
	}
	return nil
}

// ── Failures: keep the old file, alert once after fail_hours ────────────

type failMark struct {
	Since   string `yaml:"since"`
	Alerted bool   `yaml:"alerted"`
}

// failFile lives next to Kronos's state.json (not in the phone's folder).
func failFile(c *job.Ctx) string {
	return filepath.Join(filepath.Dir(c.Path("state")), "kshan_weather.fail.yml") // kronos.yml: paths.state
}

func weatherFailed(c *job.Ctx, err error) error {
	if c.DryRun {
		return err
	}
	var m failMark
	if raw, rerr := os.ReadFile(failFile(c)); rerr == nil {
		_ = yaml.Unmarshal(raw, &m)
	}
	since, perr := time.ParseInLocation("2006-01-02 15:04:05", m.Since, time.Local)
	if perr != nil { // first failure: start counting
		since, m.Since, m.Alerted = c.Now, c.Now.Format("2006-01-02 15:04:05"), false
	}
	limit := time.Duration(c.Int("fail_hours", 6)) * time.Hour // kronos.yml: jobs.kshan_weather.options.fail_hours
	alert := !m.Alerted && c.Now.Sub(since) >= limit
	if alert {
		m.Alerted = true
	}
	if data, merr := yaml.Marshal(m); merr == nil {
		if werr := store.AtomicWrite(failFile(c), data); werr != nil {
			c.Log.Warn("Failure mark not saved: %v", werr)
		}
	}
	if alert {
		return fmt.Errorf("no weather for %s (old file kept): %v", c.Now.Sub(since).Round(time.Minute), err)
	}
	c.Log.Warn("Weather not updated, old file kept (failing since %s): %v", since.Format("15:04"), err)
	return nil
}

func weatherRecovered(c *job.Ctx) {
	if c.DryRun {
		return
	}
	if _, err := os.Stat(failFile(c)); err == nil {
		_ = os.Remove(failFile(c))
		c.Log.Info("Weather working again")
	}
}
