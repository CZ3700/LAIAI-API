package relay

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"go.uber.org/goleak"
)

func TestReadResponseBodyLimit(t *testing.T) {
	if body, err := readResponseBody(strings.NewReader("1234"), 4); err != nil || string(body) != "1234" {
		t.Fatalf("body at limit = %q, %v", body, err)
	}
	if body, err := readResponseBody(strings.NewReader("12345"), 4); err == nil || body != nil {
		t.Fatalf("oversized body = %q, %v", body, err)
	}
}

func TestZstdResponseDecompressedLimit(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	var compressed bytes.Buffer
	encoder, err := zstd.NewWriter(&compressed, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(encoder, strings.Repeat("test", 1024)); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{Header: http.Header{"Content-Encoding": {"zstd"}}, Body: io.NopCloser(&compressed)}
	reader := wrapCompressedBody(resp)
	defer reader.Close()
	if _, err := readResponseBody(reader, 1024); err == nil {
		t.Fatal("expected decompressed response limit")
	}
}
