package arkpricecatalog

import "testing"

func TestDraftReferencePricesReuseExactResolutionAndInputVideoRates(t *testing.T) {
	ref, err := ReferenceCandidates("seedance-2.5")
	if err != nil {
		t.Fatal(err)
	}
	got, err := DraftReferencePrices(ref)
	if err != nil || len(got) != 4 {
		t.Fatalf("Draft tariff derivation: %v, %d cells", err, len(got))
	}
	want := map[string]string{
		"generate/480p/false/default/true":           "70",
		"generate/480p/true/default/true":            "42",
		"draft_conversion/1080p/false/default/false": "77",
		"draft_conversion/1080p/true/default/false":  "46",
	}
	for _, price := range got {
		if price.SupplierCNYPer1M != want[price.Key] || price.Precision != "exact" || len(price.SourceRows) == 0 {
			t.Fatalf("untraced or incorrect Draft rate: %+v", price)
		}
		delete(want, price.Key)
	}
	if len(want) != 0 {
		t.Fatalf("missing Draft rates: %v", want)
	}
	if _, err := DraftReferencePrices(Candidates{Model: "seedance-2.5"}); err == nil {
		t.Fatal("missing captured base rates accepted")
	}
}
