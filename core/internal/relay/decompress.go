package relay

import (
	"compress/flate"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/klauspost/compress/zstd"
)

type zstdReadCloser struct {
	dec  *zstd.Decoder
	body io.ReadCloser
}

func (z *zstdReadCloser) Read(p []byte) (int, error) {
	return z.dec.Read(p)
}

func (z *zstdReadCloser) Close() error {
	z.dec.Close()
	return z.body.Close()
}

type multiReadCloser struct {
	reader io.Reader
	closer io.Closer
	body   io.Closer
}

func (m *multiReadCloser) Read(p []byte) (int, error) {
	return m.reader.Read(p)
}

func (m *multiReadCloser) Close() error {
	err1 := m.closer.Close()
	err2 := m.body.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

// wrapCompressedBody inspects Content-Encoding and transparently decompresses zstd, gzip, or deflate.
func wrapCompressedBody(resp *http.Response) io.ReadCloser {
	if resp == nil || resp.Body == nil {
		return nil
	}
	enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	switch enc {
	case "zstd":
		dec, err := zstd.NewReader(resp.Body, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(32<<20))
		if err != nil {
			return resp.Body
		}
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
		return &zstdReadCloser{dec: dec, body: resp.Body}
	case "gzip":
		gr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return resp.Body
		}
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
		return &multiReadCloser{reader: gr, closer: gr, body: resp.Body}
	case "deflate":
		fr := flate.NewReader(resp.Body)
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
		return &multiReadCloser{reader: fr, closer: fr, body: resp.Body}
	default:
		return resp.Body
	}
}

const maxJSONResponseBytes = 64 << 20

func readResponseBody(body io.Reader, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("upstream response body exceeds limit")
	}
	return raw, nil
}
