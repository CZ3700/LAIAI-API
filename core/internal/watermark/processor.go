package watermark

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Processor wraps the ffmpeg/ffprobe pair. The filter reproduces the operator
// tool's watermark-crop chain (scale up → slight rotate on black → centered
// crop back to source size → setsar) parameterized by source resolution: the
// published command hardcodes 1280x720, so other resolutions get the same
// relative geometry instead of a distorting fixed resize.
type Processor struct {
	FFmpeg  string        // executable path, default "ffmpeg"
	FFProbe string        // executable path, default "ffprobe"
	Filter  string        // explicit override; empty = derive from source size
	Timeout time.Duration // per invocation; must stay under the job lease
}

const (
	rotateRadians = "0.026179939" // 1.5°, from the operator tool
	// The published command scales 1280x720 to 1347x758 before rotating and
	// cropping back; keep those exact ratios for every source resolution.
	scaleX = 1347.0 / 1280.0
	scaleY = 758.0 / 720.0
)

// FilterFor derives the watermark-crop filter for a source resolution. The
// 1280x720 case reproduces the operator command byte for byte: the ink margin
// scales with the frame (+5px/-2px at 720p).
func FilterFor(w, h int) string {
	sw, sh := int(float64(w)*scaleX+0.5), int(float64(h)*scaleY+0.5)
	xInk := int(float64(w) * 5.0 / 1280.0)
	yInk := int(float64(h) * 2.0 / 720.0)
	return fmt.Sprintf("scale=%d:%d:flags=lanczos,rotate=%s:ow=iw:oh=ih:c=black,crop=%d:%d:(iw-ow)/2+%d:(ih-oh)/2-%d,setsar=1/1",
		sw, sh, rotateRadians, w, h, xInk, yInk)
}

type probeResult struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	} `json:"streams"`
}

func (p Processor) ffmpegPath() string {
	if p.FFmpeg != "" {
		return p.FFmpeg
	}
	return "ffmpeg"
}

func (p Processor) ffprobePath() string {
	if p.FFProbe != "" {
		return p.FFProbe
	}
	return "ffprobe"
}

// Resolution probes the first video stream of a local file.
func (p Processor) Resolution(ctx context.Context, path string) (int, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, p.ffprobePath(), "-v", "error", "-protocol_whitelist", "file",
		"-select_streams", "v:0", "-show_entries", "stream=width,height", "-of", "json", path).Output()
	if err != nil {
		return 0, 0, fmt.Errorf("ffprobe: %w", err)
	}
	var probe probeResult
	if json.Unmarshal(out, &probe) != nil || len(probe.Streams) == 0 || probe.Streams[0].Width <= 0 || probe.Streams[0].Height <= 0 {
		return 0, 0, fmt.Errorf("ffprobe: no video dimensions")
	}
	return probe.Streams[0].Width, probe.Streams[0].Height, nil
}

// Process runs the watermark-crop chain. filter=="" derives it from the
// probed source resolution; the audio stream is stream-copied when present.
func (p Processor) Process(ctx context.Context, filter, in, out string) error {
	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	if filter == "" {
		w, h, err := p.Resolution(ctx, in)
		if err != nil {
			return err
		}
		filter = FilterFor(w, h)
	}
	cmd := exec.CommandContext(ctx, p.ffmpegPath(), "-y", "-i", in,
		"-map", "0:v:0", "-map", "0:a?",
		"-vf", filter,
		"-c:v", "libx264", "-preset", "medium", "-crf", "15",
		"-pix_fmt", "yuv420p", "-c:a", "copy",
		"-movflags", "+faststart", out)
	if buf, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("ffmpeg timeout after %s", p.Timeout)
		}
		return fmt.Errorf("ffmpeg: %w: %s", err, tail(string(buf), 512))
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		return fmt.Errorf("ffmpeg produced no output")
	}
	return nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
