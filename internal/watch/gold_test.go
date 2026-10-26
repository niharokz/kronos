package watch

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Shaped like cleartax's page: the summary sentence plus the rate table.
const goldPage = `<html><head><script>var x="₹1 per gram for 22K gold";</script></head><body>
<p>As of 25th October 2026, the gold rate today in Bangalore is <b>₹14,715</b> per gram for 24K gold
and <b>&#8377;13,479</b> per gram for 22K gold, while the 18K rate is ₹11,036 per gram.</p>
<table><tr><th>Weight</th><th>24 Carat</th><th>22 Carat</th></tr><tr><td>1 g</td><td>₹14,715</td><td>₹13,479</td></tr></table>
</body></html>`

func TestParseGold(t *testing.T) {
	for karat, want := range map[string]float64{"22": 13479, "24": 14715} {
		re := regexp.MustCompile("(?i)" + strings.ReplaceAll(goldPattern, "{karat}", karat))
		if v, err := parseGold(goldPage, re); err != nil || v != want {
			t.Errorf("%sK: %v %v", karat, v, err)
		}
	}
	re := regexp.MustCompile("(?i)" + strings.ReplaceAll(goldPattern, "{karat}", "22"))
	if _, err := parseGold("<p>maintenance</p>", re); err == nil {
		t.Error("missing rate should be an error")
	}
}

func TestDiffGold(t *testing.T) {
	old := Gold{City: "bangalore", Karat: 22, PerGram: 13377}
	if diffGold(nil, old, old) != "" {
		t.Error("same rate should say nothing")
	}
	got := diffGold(nil, old, Gold{City: "bangalore", Karat: 22, PerGram: 13479})
	if got != "22K gold, Bangalore: ₹13,479 per gram\n▲ ₹102 from ₹13,377 (0.76%)" {
		t.Errorf("diff %q", got)
	}
}

// End to end through a local server (options.url), dry run: prints the rate, stores nothing.
func TestGoldJobDryRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gold-rate-mysuru" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(goldPage))
	}))
	defer srv.Close()
	c, dir, out := testCtx(t, "city: Mysuru, url: '"+srv.URL+"/gold-rate-{city}'", true)
	if err := Run(c, goldWatcher); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "22K gold, Mysuru: ₹13,479 per gram") {
		t.Errorf("output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "watch", "gold.yml")); err == nil {
		t.Error("dry run stored the rate")
	}
}
