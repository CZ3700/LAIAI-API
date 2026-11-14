package arkverification

import (
	"testing"

	"github.com/laiai/laiai-next/core/internal/ark"
)

// TestLoadSeedance25Flash（2026-11-13 接线）：480p/720p、官方价 46/28 元每
// 百万 token（与 2.0 同表）、4–30s、2.5 族像素表（854×480）、输入 ≤30s。
// 模型未上架：ValidUpstreamModel 无 flash case，派发结构性不可能。
func TestLoadSeedance25Flash(t *testing.T) {
	p, err := Load(ark.Model25Flash)
	if err != nil {
		t.Fatal(err)
	}
	if p.Capability.PublicModel != ark.Model25Flash || p.Capability.MaxDuration != 30 || p.Capability.MinDuration != 4 {
		t.Fatalf("capability mismatch: %+v", p.Capability)
	}
	for _, res := range p.Capability.Resolutions {
		if res != "480p" && res != "720p" {
			t.Fatalf("flash must be 480p/720p only: %s", res)
		}
	}
	if p.MaxInputVideoDurationMS != 30000 {
		t.Fatalf("input video cap = %d, want 30000", p.MaxInputVideoDurationMS)
	}
	if p.MinimumVersion != "official-min-seedance-2.5-flash-20261105-v1" {
		t.Fatalf("minimum version = %s", p.MinimumVersion)
	}
	// 2.5 族像素表：480p 16:9 = 854×480（官方典型场景价反推证实）。
	if b := p.Bounds["480p/16:9"]; b.Width != 854 || b.Height != 480 {
		t.Fatalf("480p 16:9 bound = %dx%d, want 854x480", b.Width, b.Height)
	}
	if b := p.Bounds["720p/16:9"]; b.Width != 1280 || b.Height != 720 {
		t.Fatalf("720p 16:9 bound = %dx%d, want 1280x720", b.Width, b.Height)
	}
	// 最低量域覆盖：480p/720p × 6 显式比例 × 4–30s 全部有 cell（官方 339 行
	// minimum 表沿用——最低 token 与单价无关，flash 与 2.5 同几何）。
	seen := map[string]int64{}
	for _, cell := range p.MinimumCells {
		seen[cell.Resolution+"/"+cell.Ratio+"/"+itoaInt64(cell.OutputDurationMS)] = cell.MinimumTokens
	}
	for _, res := range []string{"480p", "720p"} {
		for _, ratio := range []string{"16:9", "4:3", "1:1", "3:4", "9:16", "21:9"} {
			for d := 4; d <= 30; d++ {
				tok, ok := seen[res+"/"+ratio+"/"+itoaInt64(int64(d*1000))]
				if !ok {
					t.Fatalf("missing minimum cell %s/%s/%ds", res, ratio, d)
				}
				if tok <= 0 {
					t.Fatalf("non-positive minimum: %s/%s/%ds = %d", res, ratio, d, tok)
				}
			}
		}
	}
	// 官方最低价表逐点自验（2026-11-13 Lark 捕获）：floor = ceil((4s+输出)×
	// W×H×24/1024)——输出 5s：480p=86,468、720p=194,400（官方输入 2/3/4s
	// 三行同值证实 4s 输入钳制；输入≥5s 的行是估算几何，不属于 floor）。
	for _, tc := range []struct {
		res  string
		d    int64
		want int64
	}{
		{"480p", 5000, 86468}, {"480p", 4000, 76860}, {"480p", 30000, 326655},
		{"720p", 5000, 194400}, {"720p", 4000, 172800}, {"720p", 30000, 734400},
	} {
		key := tc.res + "/16:9/" + itoaInt64(tc.d)
		if got := seen[key]; got != tc.want {
			t.Fatalf("official minimum floor %s d=%dms: %d, want %d", tc.res, tc.d, got, tc.want)
		}
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func itoaInt64(v int64) string { return itoa(int(v)) }
