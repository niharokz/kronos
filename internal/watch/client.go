package watch

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gitlab.com/niharokz/kronos/internal/job"
)

// userAgent is a plain desktop browser; some price sites refuse obvious bots.
const userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

// Client fetches pages for monitors: browser headers, a timeout, one retry.
type Client struct {
	c    *job.Ctx
	http *http.Client
}

// NewClient uses options.timeout_s (default 30) per request.
func NewClient(c *job.Ctx) *Client {
	t := time.Duration(c.Int("timeout_s", 30)) * time.Second // kronos.yml: jobs.watch_<name>.options.timeout_s
	return &Client{c: c, http: &http.Client{Timeout: t}}
}

// Get returns a page's body; a network error or 5xx is retried once after 5 s.
func (h *Client) Get(url string) (string, error) {
	body, retry, err := h.get(url)
	if err != nil && retry {
		select {
		case <-time.After(5 * time.Second):
		case <-h.c.Done():
			return "", err
		}
		body, _, err = h.get(url)
	}
	return body, err
}

func (h *Client) get(url string) (string, bool, error) {
	req, err := http.NewRequestWithContext(h.c, http.MethodGet, url, nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-IN,en;q=0.9")
	resp, err := h.http.Do(req)
	if err != nil {
		return "", true, fmt.Errorf("%s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode >= 500, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // pages are well under 8 MB
	if err != nil {
		return "", true, fmt.Errorf("%s: %v", url, err)
	}
	return string(raw), false, nil
}

// ── Small HTML helpers (no parser dependency; each monitor's tests pin its page shape) ──

var (
	reHidden = regexp.MustCompile(`(?is)<(script|style|noscript)\b.*?</(script|style|noscript)>`)
	reTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpace  = regexp.MustCompile(`\s+`)
	reMoney  = regexp.MustCompile(`([\d][\d,]*(?:\.\d+)?)`)
)

// Text is the visible text of an HTML fragment: tags dropped, entities decoded, spaces collapsed.
func Text(s string) string {
	s = reHidden.ReplaceAllString(s, " ")
	s = reTag.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(html.UnescapeString(s), "\u00a0", " ") // &nbsp; is a space too
	return strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
}

// Attr is an attribute's value inside one tag's attribute text ("" when absent).
func Attr(attrs, name string) string {
	re := regexp.MustCompile(`(?is)\b` + regexp.QuoteMeta(name) + `\s*=\s*("([^"]*)"|'([^']*)')`)
	m := re.FindStringSubmatch(attrs)
	if m == nil {
		return ""
	}
	return html.UnescapeString(m[2] + m[3])
}

// Number parses "1,47,153.50" (Indian or Western grouping) → 147153.5.
func Number(s string) (float64, bool) {
	m := reMoney.FindString(s)
	if m == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(m, ",", ""), 64)
	return v, err == nil
}

// Commas formats 13479 → "13,479" and 13479.5 → "13,479.50".
func Commas(v float64) string {
	whole := int64(v)
	s := strconv.FormatInt(whole, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if frac := v - float64(whole); frac > 0.004 {
		s += fmt.Sprintf("%.2f", frac)[1:]
	}
	return s
}
