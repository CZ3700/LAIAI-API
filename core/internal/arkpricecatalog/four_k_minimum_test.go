package arkpricecatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestFourKMinimumCaptureAndCompleteOriginalRows(t *testing.T) {
	cells, sources, err := fourKMinimumCandidates()
	if err != nil || len(cells) != 162 || len(sources[0].RawRows) != 108 {
		t.Fatal("incomplete 4K capture", err)
	}
	var capture struct {
		Screenshots []struct{ Path, SHA256 string }
	}
	if err := json.Unmarshal(fourKMinimumCapture, &capture); err != nil || len(capture.Screenshots) != 6 {
		t.Fatal("screenshot provenance missing", err)
	}
	// The watermarked screenshots are kept outside the public repository. An
	// operator can verify the original files locally by setting this directory.
	evidenceDir := os.Getenv("LAIAI_4K_EVIDENCE_DIR")
	for _, source := range capture.Screenshots {
		if _, err := hex.DecodeString(source.SHA256); err != nil || len(source.SHA256) != 64 || !strings.HasPrefix(source.Path, "minimum-") {
			t.Fatal("invalid screenshot provenance", source.Path)
		}
		if evidenceDir == "" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(evidenceDir, filepath.FromSlash(source.Path)))
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(raw)
		if hex.EncodeToString(h[:]) != source.SHA256 {
			t.Fatal("captured screenshot changed", source.Path)
		}
	}
	seen := map[string]int{}
	for _, c := range cells {
		if c.FPS != nil || len(c.SourceRows) != 1 || c.SourceRows[0].SourceID != FourKMinimumSourceID {
			t.Fatal("missing screenshot FPS was invented or source row lost")
		}
		seen[c.Ratio]++
	}
	if len(seen) != 6 {
		t.Fatal("missing ratios")
	}
	for ratio, count := range seen {
		if count != 27 {
			t.Fatal("missing durations", ratio, count)
		}
	}

	// Independent arithmetic check against the already captured 2.5 table's
	// explicit minimum input durations, not rounded per-video monetary examples.
	old, err := ReferenceCandidates("seedance-2.5")
	if err != nil {
		t.Fatal(err)
	}
	inputSeconds := map[int64]int64{}
	for _, source := range old.Sources {
		if source.TableID != "tblheIYDXXwYM43b" {
			continue
		}
		for _, row := range source.RawRows {
			if row[4] != "480p" || !strings.Contains(row[5], "16:9") {
				continue
			}
			in, ie := strconv.ParseInt(row[1], 10, 64)
			out, oe := strconv.ParseInt(row[2], 10, 64)
			if ie == nil && oe == nil {
				inputSeconds[out] = in
			}
		}
	}
	for _, row := range sources[0].RawRows {
		out, _ := strconv.ParseInt(row[0], 10, 64)
		tokens, _ := strconv.ParseInt(row[1], 10, 64)
		width, _ := strconv.ParseInt(row[4], 10, 64)
		height, _ := strconv.ParseInt(row[5], 10, 64)
		in, ok := inputSeconds[out]
		if !ok || ((in+out)*width*height*24+512)/1024 != tokens {
			t.Fatalf("transcription failed pixel arithmetic cross-check: %v", row)
		}
	}
}
