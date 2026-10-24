package pulse

import (
	"bytes"
	"time"

	"gopkg.in/yaml.v3"

	"gitlab.com/niharokz/kronos/internal/dates"
)

// Encode marshals v as YAML under a one-line "# ..." header.
func Encode(header string, v any) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("# " + header + "\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Logs holds the daily logs read for one run.
type Logs struct {
	Days     []Day          // parsed days, oldest first
	ByDate   map[string]Day // by YYYY-MM-DD
	Problems []string       // files that couldn't be parsed (retried next run)
}

// LoadLogs reads every daily log from `from` to `to` (inclusive); unreadable ones are reported, not fatal.
func LoadLogs(rahiDir string, from, to time.Time) (Logs, error) {
	ds, err := LogDates(rahiDir)
	if err != nil {
		return Logs{}, err
	}
	l := Logs{ByDate: map[string]Day{}}
	for _, t := range ds {
		if t.Before(from) || t.After(to) {
			continue
		}
		d, ok, err := LoadDay(DayFile(rahiDir, t))
		if err != nil {
			l.Problems = append(l.Problems, err.Error())
			continue
		}
		if ok {
			if d.Date.IsZero() {
				d.Date = t
			}
			l.Days = append(l.Days, d)
			l.ByDate[dates.Format(d.Date)] = d
		}
	}
	return l, nil
}

// Final returns only the closed days.
func (l Logs) Final() []Day {
	var out []Day
	for _, d := range l.Days {
		if d.Final {
			out = append(out, d)
		}
	}
	return out
}

// AppNames maps package → display name from the logs (latest wins).
func (l Logs) AppNames() map[string]string {
	names := map[string]string{}
	for _, d := range l.Days {
		for pkg, a := range d.Apps {
			if a.Name != "" {
				names[pkg] = a.Name
			}
		}
	}
	return names
}
