package migrate

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
)

// Captured by the real Go driver against MySQL 8.4.9 with the application's
// utf8mb4 client and utf8mb4_general_ci connection. All 25 columns and 43 index
// parts are retained verbatim, including expression bytes and SQL NULL values.
// Evidence: probe-result.json SHA256 50548308bb0aaec4b8fd7b53d4b40749a703b786d95b68215df74fc8d1905cab.
// This source-bound profile cannot be overridden with runtime configuration.
//
//go:embed settlement_shape_profile.json
var settlementShapeProfileJSON []byte

const settlementShapeProfileSHA256 = "a4a91e6bee6f253cab99ca0672f1d0dae8e12ae41af20465b1e8ff50400d38d7"

func approvedSettlementShapeProfile() *settlementShapeProfile {
	sum := sha256.Sum256(settlementShapeProfileJSON)
	if hex.EncodeToString(sum[:]) != settlementShapeProfileSHA256 {
		return nil
	}
	var p settlementShapeProfile
	if err := json.Unmarshal(settlementShapeProfileJSON, &p); err != nil {
		return nil
	}
	return &p
}
