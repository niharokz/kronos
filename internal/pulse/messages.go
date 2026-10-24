package pulse

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/dates"
)

// Message types, in priority order (lower sends first when the daily cap is near).
const (
	TypePattern    = "pattern"     // strike pattern → Telegram (never dropped; deferred to the briefing)
	TypeStepsFinal = "steps-final" // last call for steps → inbox
	TypeApp        = "app"         // app over its usual → inbox
	TypeStepsSoft  = "steps-soft"  // early steps hint → inbox
	TypeReport     = "report"      // weekly/monthly → Telegram (+ inbox), outside the cap
)

// Channels.
const (
	ChanInbox    = "inbox"
	ChanTelegram = "telegram"
	ChanBriefing = "briefing" // deferred to the next morning's briefing
)

var priority = map[string]int{TypePattern: 0, TypeStepsFinal: 1, TypeApp: 2, TypeStepsSoft: 3}

// Priority is a type's rank (0 first); reports are outside the cap and come last.
func Priority(typ string) int {
	if p, ok := priority[typ]; ok {
		return p
	}
	return 9
}

// Message is one nudge, warning or report.
type Message struct {
	ID      string  // deterministic, e.g. nudge-instagram-20261024
	Type    string  // Type*
	Metric  string  // metric key it is about
	Channel string  // Chan*
	Title   string  // inbox title / Telegram title
	Text    string  // body
	Target  float64 // the target at send time (for feedback)
}

// Sent is state/sent.yml: everything sent, for dedup, the daily cap and feedback.
type Sent struct {
	Sent []SentItem `yaml:"sent"`
}

// SentItem is one sent message.
type SentItem struct {
	ID      string  `yaml:"id"`
	Date    string  `yaml:"date"`          // day it was decided
	At      string  `yaml:"at"`            // HH:MM
	Type    string  `yaml:"type"`          // Type*
	Metric  string  `yaml:"metric"`        // metric key
	Channel string  `yaml:"channel"`       // Chan*
	For     string  `yaml:"for,omitempty"` // briefing date, for deferred warnings
	Target  float64 `yaml:"target,omitempty"`
	Text    string  `yaml:"text,omitempty"` // kept for deferred warnings
}

// SentFile is <rahi>/state/sent.yml.
func SentFile(rahiDir string) string { return filepath.Join(rahiDir, "state", "sent.yml") }

// LoadSent reads sent.yml; missing gives an empty log.
func LoadSent(path string) (*Sent, error) {
	s := &Sent{}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, yaml.Unmarshal(raw, s)
}

// Has reports whether an id was already sent (or deferred).
func (s *Sent) Has(id string) bool {
	for _, it := range s.Sent {
		if it.ID == id {
			return true
		}
	}
	return false
}

// CountToday counts today's capped messages (reports excluded, deferred ones excluded).
func (s *Sent) CountToday(day time.Time) int {
	n, d := 0, dates.Format(day)
	for _, it := range s.Sent {
		if it.Date == d && it.Type != TypeReport && it.Channel != ChanBriefing {
			n++
		}
	}
	return n
}

// CountType counts today's messages of one type for one metric.
func (s *Sent) CountType(day time.Time, typ, metric string) int {
	n, d := 0, dates.Format(day)
	for _, it := range s.Sent {
		if it.Date == d && it.Type == typ && it.Metric == metric {
			n++
		}
	}
	return n
}

// SentSince reports whether a message of this type and metric was sent or deferred on/after `since`.
func (s *Sent) SentSince(typ, metric string, since time.Time) bool {
	cut := dates.Format(since)
	for _, it := range s.Sent {
		if it.Type == typ && it.Metric == metric && it.Date >= cut {
			return true
		}
	}
	return false
}

// Record adds a sent message.
func (s *Sent) Record(m Message, now time.Time, forDay string) {
	it := SentItem{ID: m.ID, Date: dates.Format(now), At: now.Format("15:04"), Type: m.Type, Metric: m.Metric,
		Channel: m.Channel, For: forDay, Target: Round(m.Target)}
	if m.Channel == ChanBriefing {
		it.Text = m.Text
	}
	s.Sent = append(s.Sent, it)
}

// Deferred are warnings waiting for a day's briefing.
func (s *Sent) Deferred(day time.Time) []SentItem {
	var out []SentItem
	for _, it := range s.Sent {
		if it.Channel == ChanBriefing && it.For == dates.Format(day) {
			out = append(out, it)
		}
	}
	return out
}

// Prune drops entries older than keep days.
func (s *Sent) Prune(today time.Time, keep int) {
	cut := dates.Format(today.AddDate(0, 0, -keep))
	out := s.Sent[:0]
	for _, it := range s.Sent {
		if it.Date >= cut {
			out = append(out, it)
		}
	}
	s.Sent = out
}

// Plan orders candidates by priority and splits them into send-now and defer-to-briefing.
// Quiet hours or a full budget drop nudges but defer pattern warnings; nothing is sent twice.
func Plan(cands []Message, s *Sent, today time.Time, max int, quiet bool) (send, deferred []Message) {
	sort.SliceStable(cands, func(i, j int) bool { return priority[cands[i].Type] < priority[cands[j].Type] })
	left := max - s.CountToday(today)
	for _, m := range cands {
		if s.Has(m.ID) {
			continue
		}
		switch {
		case !quiet && left > 0:
			send = append(send, m)
			left--
		case m.Type == TypePattern:
			m.Channel = ChanBriefing
			deferred = append(deferred, m)
		}
	}
	return send, deferred
}

// ── Inbox files ─────────────────────────────────────────────────────────

// InboxHeader is the frontmatter Rahi reads (title/from/date); id/type are for Kronos.
type InboxHeader struct {
	ID    string `yaml:"id"`
	Title string `yaml:"title"`
	From  string `yaml:"from"`
	Date  string `yaml:"date"`
	Type  string `yaml:"type"`
}

// InboxFile is <rahi>/inbox/<id>.md.
func InboxFile(rahiDir, id string) string { return filepath.Join(rahiDir, "inbox", id+".md") }

// InboxMarkdown renders one inbox message: YAML frontmatter + body.
func InboxMarkdown(m Message, day time.Time) ([]byte, error) {
	head, err := yaml.Marshal(InboxHeader{ID: m.ID, Title: m.Title, From: "Kronos", Date: dates.Format(day), Type: m.Type})
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("---\n")
	b.Write(head)
	b.WriteString("---\n")
	b.WriteString(strings.TrimSpace(m.Text) + "\n")
	return b.Bytes(), nil
}

// ── Feedback from inbox/read ────────────────────────────────────────────

// ReadItem is a message you've seen: Rahi moved it to inbox/read/.
type ReadItem struct {
	ID        string
	ReadDay   time.Time
	Dismissed bool // ✓ without opening
}

var readSuffix = regexp.MustCompile(`_(\d{8})(-\d+)?$`)
var frontmatter = regexp.MustCompile(`(?s)\A---\s*\n(.*?)\n---\s*(\n|\z)`)

// LoadRead lists messages in inbox/read; Rahi 0.0.5 fields (read_at, dismissed) when present,
// else the read day from the file name (<id>_YYYYMMDD.md).
func LoadRead(rahiDir string) []ReadItem {
	dir := filepath.Join(rahiDir, "inbox", "read")
	entries, _ := os.ReadDir(dir)
	var out []ReadItem
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), ".md")
		it := ReadItem{ID: readSuffix.ReplaceAllString(stem, "")}
		if m := readSuffix.FindStringSubmatch(stem); m != nil {
			it.ReadDay, _ = dates.ParseISO(m[1])
		}
		if raw, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
			if fm := frontmatter.FindSubmatch(raw); fm != nil {
				var h struct {
					ID        string `yaml:"id"`
					ReadAt    string `yaml:"read_at"`
					Dismissed bool   `yaml:"dismissed"`
				}
				if yaml.Unmarshal(fm[1], &h) == nil {
					if h.ID != "" {
						it.ID = h.ID
					}
					if t, ok := dates.ParseISO(firstN(h.ReadAt, 10)); ok {
						it.ReadDay = t
					}
					it.Dismissed = h.Dismissed
				}
			}
		}
		out = append(out, it)
	}
	return out
}

// BackedOff reports whether nudges of (type, metric) should pause today: the last 3 read nudges
// were each dismissed or ended the day off target (a mindful turn-back counts as improvement).
// A back-off lasts 7 days from the latest of those nudges and allows every other day.
func BackedOff(s *Sent, read []ReadItem, days map[string]Day, m Metric, typ string, today time.Time) bool {
	readBy := map[string]ReadItem{}
	for _, r := range read {
		readBy[r.ID] = r
	}
	var ignored []SentItem
	for i := len(s.Sent) - 1; i >= 0 && len(ignored) < 3; i-- {
		it := s.Sent[i]
		if it.Type != typ || it.Metric != m.Key || it.Channel != ChanInbox {
			continue
		}
		r, ok := readBy[it.ID]
		d, final := days[it.Date]
		if !ok || !final {
			return false // unread or day still open: no verdict yet
		}
		if !r.Dismissed && improved(d, m, it.Target) {
			return false
		}
		ignored = append(ignored, it)
	}
	if len(ignored) < 3 {
		return false
	}
	last, _ := dates.ParseISO(ignored[0].Date)
	if today.After(last.AddDate(0, 0, 7)) {
		return false
	}
	return today.YearDay()%2 == 1 // every other day
}

func improved(d Day, m Metric, target float64) bool {
	if m.App != "" && d.TurnedBack[m.App] > 0 {
		return true
	}
	v := d.Value(m)
	return v != nil && (target == 0 || Meets(m, *v, target))
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Slug makes an id-safe name: "YouTube Music" → "youtube-music".
func Slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// Num formats a value for messages: 8421 → "8,421".
func Num(v float64) string {
	n := int64(v + 0.5)
	s := fmt.Sprint(n)
	if n < 0 {
		return s
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
