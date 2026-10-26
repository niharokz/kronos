package watch

import (
	"strings"
	"testing"
)

// Shaped like domainoffer.net/tld/ar: registrar cell, new, ICANN fee (skipped), renew, transfer.
const domainOfferPage = `<table id="domain-list"><thead><tr><th>Registrar</th><th>New</th><th class="col-icann-fee">Fee</th><th>Renew</th><th>Transfer</th></tr></thead><tbody>
<tr><td><span class="registrar-name">Marcaria.com</span><a href="/registrar/marcaria/">View Details</a></td><td>$70.00</td><td class="col-icann-fee">-</td><td>$65.00</td><td>€ →$60.50</td></tr>
<tr><td><span class="registrar-name">Gandi</span><a href="/registrar/gandi">View Details</a></td><td>$90.00</td><td class="col-icann-fee">-</td><td>$80.00</td><td>-</td></tr>
<tr><td><span class="registrar-name">NIC.ar Reseller</span><a href="/registrar/nicar">View Details</a></td><td>$40.00</td><td class="col-icann-fee">-</td><td>$55.00</td><td>$55.00</td></tr>
</tbody></table><a href="?page=2">Next »</a>`

// Shaped like tld-list.com/tld/ar.
const tldListPage = `<table><thead><tr><th>Registrar</th><th>Registration</th><th>Renewal</th><th>Transfer</th></tr></thead><tbody>
<tr><td>Marcaria International</td><td>$71.00</td><td>$64.00</td><td>$59.00</td></tr>
<tr><td>Dynadot</td><td>$99.00</td><td>$99.00</td><td>-</td></tr>
</tbody></table>`

func TestParseAndRankDomains(t *testing.T) {
	do, next := parseDomainOffer(domainOfferPage)
	if len(do) != 3 || !next || *do[0].Transfer != 60.5 || *do[0].Renew != 65 || do[0].Link != "https://domainoffer.net/go/marcaria" || do[1].Transfer != nil {
		t.Fatalf("domainoffer %+v next=%v", do, next)
	}
	tl := parseTLDList(tldListPage)
	if len(tl) != 2 || *tl[0].Transfer != 59 || tl[1].Transfer != nil {
		t.Fatalf("tld-list %+v", tl)
	}
	// total = transfer + renew; Marcaria's two spellings merge, the cheaper quote wins.
	best := rankOffers(append(do, tl...), "total", 2)
	if len(best) != 2 || best[0].Registrar != "NIC.ar Reseller" || best[0].Score != 110 || best[1].Score != 123 || best[1].Registrar != "Marcaria International" {
		t.Errorf("ranking %+v", best)
	}
	if got := rankOffers(do, "transfer", 1); got[0].Score != 55 {
		t.Errorf("rank by transfer %+v", got)
	}
}

func TestDiffDomains(t *testing.T) {
	p := func(v float64) *float64 { return &v }
	old := Domains{RankBy: "total", TLDs: map[string][]Offer{"ar": {{Registrar: "NIC.ar Reseller", Score: 110, Transfer: p(55), Renew: p(55), Link: "L"}}}}
	if diffDomains(nil, old, old) != "" {
		t.Error("same prices should say nothing")
	}
	cur := Domains{RankBy: "total", TLDs: map[string][]Offer{"ar": {{Registrar: "Marcaria", Score: 105, Transfer: p(50), Renew: p(55), Link: "M"}}}}
	got := diffDomains(nil, old, cur)
	want := ".ar — cheapest transfer + 1st renewal (USD)\n1. Marcaria: transfer $50.00 + renew $55.00 = $105.00\n   was NIC.ar Reseller: $110.00\n   M"
	if got != want {
		t.Errorf("diff:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(showDomains(cur), "1. Marcaria: transfer $50.00") {
		t.Error("show")
	}
}
