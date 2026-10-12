package jobs

import (
	"os"
	"sort"
	"strings"
	"testing"

	"gitlab.com/niharokz/kronos/internal/config"
	"gitlab.com/niharokz/kronos/internal/job"
)

// kronos.yml.example is the list of built-in jobs: adding or removing a job
// file only needs its entry there, no list to keep in sync here.
func TestExampleConfigMatchesRegisteredJobs(t *testing.T) {
	raw, err := os.ReadFile("../../kronos.yml.example")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(raw, "kronos.yml.example")
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, j := range cfg.Jobs {
		if len(j.Command) == 0 { // script jobs have no Go code
			listed = append(listed, j.Name)
		}
	}
	sort.Strings(listed)
	if got, want := strings.Join(job.Names(), ","), strings.Join(listed, ","); got != want {
		t.Errorf("registered jobs:   %s\nkronos.yml.example: %s", got, want)
	}
}
