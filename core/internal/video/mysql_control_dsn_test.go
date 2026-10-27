package video

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	drivermysql "github.com/go-sql-driver/mysql"
)

var videoTestControlDatabase = regexp.MustCompile(`^ark_test_s2_compat_[a-z0-9]{1,24}$`)

// Parse the actual database field, never a substring in credentials or params.
// The compatibility namespace permits isolated reruns without altering an old
// failure database. Existing private-schema allocation remains unchanged.
func parseVideoTestControlDSN(raw string) (*drivermysql.Config, error) {
	cfg, err := drivermysql.ParseDSN(raw)
	if err != nil || (cfg.DBName != "ark_test" && !videoTestControlDatabase.MatchString(cfg.DBName)) {
		return nil, errors.New("integration requires an approved isolated control database")
	}
	return cfg, nil
}

func TestVideoTestControlDSNBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, dsn string
		want      bool
	}{
		{"original", "unit@tcp(localhost:3306)/ark_test?parseTime=true", true},
		{"r3", "unit@tcp(localhost:3306)/ark_test_s2_compat_r3?parseTime=true", true},
		{"namespace_limit", "unit@tcp(localhost:3306)/ark_test_s2_compat_" + strings.Repeat("a", 24), true},
		{"empty", "unit@tcp(localhost:3306)/", false},
		{"production", "unit@tcp(localhost:3306)/production?parseTime=true", false},
		{"legacy_prefix", "unit@tcp(localhost:3306)/ark_test_other", false},
		{"empty_suffix", "unit@tcp(localhost:3306)/ark_test_s2_compat_", false},
		{"long_suffix", "unit@tcp(localhost:3306)/ark_test_s2_compat_" + strings.Repeat("a", 25), false},
		{"uppercase_suffix", "unit@tcp(localhost:3306)/ark_test_s2_compat_R3", false},
		{"punctuation_suffix", "unit@tcp(localhost:3306)/ark_test_s2_compat_r-3", false},
		{"parameter_disguise", "unit@tcp(localhost:3306)/production?note=/ark_test?", false},
		{"encoded_parameter_disguise", "unit@tcp(localhost:3306)/production?note=%2Fark_test%3F", false},
		{"password_disguise", "unit:/ark_test?@tcp(localhost:3306)/production", false},
		{"invalid_dsn", "unit@tcp(localhost:3306)/ark_test?parseTime=not-a-bool", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseVideoTestControlDSN(tc.dsn)
			if (err == nil) != tc.want || (cfg != nil) != tc.want {
				t.Fatal("database allowlist differs")
			}
		})
	}
}
