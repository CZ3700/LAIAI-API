package arkpricecatalog

import "testing"

func TestFourKPriceEvidenceAndConversionRates(t *testing.T) {
	ref, err := LatestReferenceCandidates("seedance-2.5")
	if err != nil || len(ref.Prices) != 8 || FourKSourceHash() != "d1b11de6f86130d2a60be77f1ad964d916f8d887ed13c76b43e3544728323bac" {
		t.Fatal("4K document or prices changed", err)
	}
	prices, err := DraftReferencePrices(ref)
	if err != nil || len(prices) != 6 {
		t.Fatal("4K conversion rate missing", err)
	}
	for _, p := range append(prices, ref.Prices...) {
		if p.Resolution != "4k" {
			continue
		}
		want := "31"
		if p.InputVideo {
			want = "19"
		}
		if p.SupplierCNYPer1M != want || p.SourceRows[0].SourceID != FourKSourceID {
			t.Fatalf("wrong 4K price/provenance: %+v", p)
		}
	}
	old, _ := ReferenceCandidates("seedance-2.5")
	if len(old.Prices) != 6 || len(ref.Minimum.Cells) != len(old.Minimum.Cells)+162 {
		t.Fatal("historical prices changed or 4K minimum coverage missing")
	}
}
