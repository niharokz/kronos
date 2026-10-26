package watch

import (
	"strings"
	"testing"
)

// Shaped like a rewards.bing.com/redeem/in card list.
const rewardsPage = `<div class="grid">
<a href="/redeem/sku/000400000123" class="card"><img src="x.png" alt="Amazon Pay gift card ₹500"><h3>Amazon Pay gift card ₹500</h3><p>Instant delivery by email</p><span>6,500 pts</span></a>
<a href="https://rewards.bing.com/redeem/sku/000800000456"><div><h4>Donate to CRY</h4><span>1,000 points</span></div></a>
<a href="/redeem/sku/000900000789"><span>Mystery sweepstakes Mystery sweepstakes</span></a>
<a href="/about">About</a></div>`

func TestParseRewards(t *testing.T) {
	items := parseRewards(rewardsPage, "featured")
	if len(items) != 3 {
		t.Fatalf("%d items: %+v", len(items), items)
	}
	a, b, c := items[0], items[1], items[2]
	if a.SKU != "000400000123" || a.Title != "Amazon Pay gift card ₹500" || a.Points != 6500 || !strings.Contains(a.Desc, "Instant delivery") {
		t.Errorf("card 1 %+v", a)
	}
	if b.Title != "Donate to CRY" || b.Points != 1000 {
		t.Errorf("card 2 %+v", b)
	}
	if c.Title != "Mystery sweepstakes" || c.Points != 0 {
		t.Errorf("card 3 (repeated title) %+v", c)
	}
}

// New items always; points changes per notify_price; text-only changes and removals are silent by default.
func TestDiffRewards(t *testing.T) {
	c, _, _ := testCtx(t, "", false)
	old := Catalog{Items: []Item{{SKU: "1", Title: "A", Points: 100}, {SKU: "2", Title: "B", Points: 200}, {SKU: "3", Title: "C", Points: 50}}}
	same := Catalog{Items: []Item{{SKU: "1", Title: "A (new text)", Points: 100}, {SKU: "2", Title: "B", Points: 200}, {SKU: "3", Title: "C", Points: 50}}}
	if diffRewards(c, old, same) != "" {
		t.Error("a text-only change should be silent")
	}
	cur := Catalog{Items: []Item{{SKU: "1", Title: "A", Points: 100}, {SKU: "2", Title: "B", Points: 150}, {SKU: "4", Title: "D", Points: 300}}}
	got := diffRewards(c, old, cur)
	if !strings.Contains(got, "🆕 D — 300 pts") || !strings.Contains(got, "B: 200 → 150 pts") || strings.Contains(got, "Removed") {
		t.Errorf("diff:\n%s", got)
	}
	c2, _, _ := testCtx(t, "notify_price: false, notify_removed: true", false)
	got = diffRewards(c2, old, cur)
	if strings.Contains(got, "Points changed") || !strings.Contains(got, "➖ C") {
		t.Errorf("options ignored:\n%s", got)
	}
	if merged := keepRewards(old, Catalog{Items: []Item{{SKU: "4", Title: "D"}}}); len(merged.Items) != 4 {
		t.Errorf("partial fetch should keep old items: %+v", merged)
	}
}
