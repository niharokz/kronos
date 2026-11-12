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

// kshan_weather writes the phone's weather file (Kshan Clock widget) from open-meteo (no key).
// Kshan never goes online for weather; it only reads this file.
//
// Options:
//   lat, lon     12.925, 77.583
//   place        "Jayanagar, Bengaluru"     shown on the widget
//   hours        5                          next whole hours listed
//   file         logs/kshan/weather.md      relative = in the vault (paths.vault)
//   timezone     Asia/Kolkata               open-meteo's local time for `next`
//   fail_hours   6                          failed fetches in a row for this long → one alert
//   url          https://api.open-meteo.com/v1/forecast
//
// A failed fetch keeps the old file (WARN); after fail_hours of failures one FAIL + alert.

func init() { job.Register("kshan_weather", kshanWeather) }

const openMeteo = "https://api.open-meteo.com/v1/forecast"

func kshanWeather(c *job.Ctx) error {
	path := c.VaultFile("file", "logs/kshan/weather.md") // kronos.yml: jobs.kshan_weather.options.file
	f, err := fetchForecast(c)
	if err != nil {
		return weatherFailed(c, err)
	}
	data, err := weatherYAML(f, c.String("place", "Jayanagar, Bengaluru"), c.Int("hours", 5), c.Now)
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
	c.Log.OK("%s, %.1f °C, %s", f.Current.text(), round1(f.Current.Temp), path)
	return nil
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

func fetchForecast(c *job.Ctx) (forecast, error) {
	var f forecast
	q := url.Values{}
	q.Set("latitude", c.String("lat", "12.925")) // kronos.yml: jobs.kshan_weather.options.lat/lon
	q.Set("longitude", c.String("lon", "77.583"))
	q.Set("current", "temperature_2m,apparent_temperature,weather_code,is_day,relative_humidity_2m")
	q.Set("hourly", "temperature_2m,weather_code,precipitation_probability")
	q.Set("daily", "temperature_2m_min,temperature_2m_max,precipitation_probability_max")
	q.Set("timezone", c.String("timezone", "Asia/Kolkata"))
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
func weatherYAML(f forecast, place string, hours int, now time.Time) ([]byte, error) {
	root := store.NewMapping()
	store.Set(root, "updated", store.Quoted(now.Format("2006-01-02 15:04:05")))
	store.Set(root, "place", store.Str(place))

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
	keyNode(root, "next").LineComment = fmt.Sprintf("# the next %d whole hours, nearest first", hours)

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
