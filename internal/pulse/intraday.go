package pulse

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Intraday is state/intraday-YYYYMMDD.yml: one snapshot per pulse_watch run.
type Intraday struct {
	Date      string     `yaml:"date"`
	Snapshots []Snapshot `yaml:"snapshots"`
}

// Snapshot is the day so far at one moment.
type Snapshot struct {
	At        string             `yaml:"at"` // "HH:MM"
	Steps     *float64           `yaml:"steps"`
	ScreenMin *float64           `yaml:"screen_min"`
	Apps      map[string]float64 `yaml:"apps,omitempty"` // tracked apps' minutes
}

// IntradayFile is <rahi>/state/intraday-YYYYMMDD.yml.
func IntradayFile(rahiDir string, d time.Time) string {
	return filepath.Join(rahiDir, "state", "intraday-"+d.Format("20060102")+".yml")
}

// LoadIntraday reads one day's snapshots; missing gives an empty file.
func LoadIntraday(path string) (Intraday, error) {
	var in Intraday
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return in, nil
	}
	if err != nil {
		return in, err
	}
	err = yaml.Unmarshal(raw, &in)
	return in, err
}

// Add records a snapshot, replacing one taken at the same minute (reruns stay idempotent).
func (in *Intraday) Add(s Snapshot) {
	for i := range in.Snapshots {
		if in.Snapshots[i].At == s.At {
			in.Snapshots[i] = s
			return
		}
	}
	in.Snapshots = append(in.Snapshots, s)
}

// StepsAt is the last known step count at or before hhmm.
func (in Intraday) StepsAt(hhmm string) (float64, bool) {
	var v float64
	ok := false
	for _, s := range in.Snapshots {
		if s.At <= hhmm && s.Steps != nil {
			v, ok = *s.Steps, true
		}
	}
	return v, ok
}

// StepDay pairs a finished day's total with its snapshots, for learning the step curve.
type StepDay struct {
	Total float64
	Snaps Intraday
}

// LearnAfter is how many goal days of snapshots the step forecast needs before it trusts them.
const LearnAfter = 14

// StepsStillToCome estimates the steps you'll still walk after hhmm today.
// With enough goal days it is the median of (final − steps at hhmm) on those days;
// before that, the typical daily total spread evenly over your waking hours.
func StepsStillToCome(hhmm string, history []StepDay, typicalTotal float64, wake, sleep string) float64 {
	var rest []float64
	for _, d := range history {
		if at, ok := d.Snaps.StepsAt(hhmm); ok {
			rest = append(rest, d.Total-at)
		}
	}
	if len(rest) >= LearnAfter {
		return *Median(rest)
	}
	if wake == "" || sleep == "" || sleep <= wake {
		wake, sleep = "07:00", "23:00"
	}
	total, left := minutes(sleep)-minutes(wake), minutes(sleep)-minutes(hhmm)
	if left <= 0 || total <= 0 {
		return 0
	}
	if left > total {
		left = total
	}
	return typicalTotal * float64(left) / float64(total)
}

// minutes turns "HH:MM" into minutes since midnight (bad input → 0).
func minutes(hhmm string) int {
	parts := strings.SplitN(hhmm, ":", 2)
	if len(parts) != 2 {
		return 0
	}
	h, m := atoi(parts[0]), atoi(parts[1])
	return h*60 + m
}

func atoi(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// MedianClock is the median of "HH:MM" values (empty when none).
func MedianClock(times []string) string {
	var ms []float64
	for _, t := range times {
		if t != "" {
			ms = append(ms, float64(minutes(t)))
		}
	}
	m := Median(ms)
	if m == nil {
		return ""
	}
	v := int(*m)
	return twoDigits(v/60) + ":" + twoDigits(v%60)
}

func twoDigits(n int) string {
	return string([]byte{byte('0' + n/10%10), byte('0' + n%10)})
}
