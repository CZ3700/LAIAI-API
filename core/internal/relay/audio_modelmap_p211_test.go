package relay

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"testing"
)

// 构造一份含 model 字段 + 文件的 multipart body。
func buildMultipart(t *testing.T, model string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("model", model)
	fw, _ := w.CreateFormFile("file", "audio.mp3")
	_, _ = fw.Write([]byte("FAKEAUDIOBYTES"))
	_ = w.Close()
	return buf.Bytes(), w.FormDataContentType()
}

// 读回 multipart 的某字段值。
func readField(t *testing.T, body []byte, ct, field string) string {
	t.Helper()
	_, params, err := mime.ParseMediaType(ct)
	if err != nil {
		t.Fatalf("parse ct: %v", err)
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		p, e := mr.NextPart()
		if e != nil {
			return ""
		}
		if p.FormName() == field && p.FileName() == "" {
			v, _ := io.ReadAll(p)
			return string(v)
		}
	}
}

// P2-11：渠道有映射时，transcription 的 multipart model 字段被重写为上游名，文件 part 保留。
func TestMappedMultipartBodyRewritesModel(t *testing.T) {
	body, ct := buildMultipart(t, "whisper-pro")
	ch := &Channel{ModelMapping: `{"whisper-pro":"whisper-1"}`}

	newBody, newCT := mappedMultipartBody(ch, body, ct, "whisper-pro")
	if got := readField(t, newBody, newCT, "model"); got != "whisper-1" {
		t.Fatalf("model field = %q, want whisper-1 (mapped)", got)
	}
	// 文件 part 仍在且内容不变
	if got := readFileField(t, newBody, newCT); got != "FAKEAUDIOBYTES" {
		t.Fatalf("file content = %q, want preserved", got)
	}
}

// P2-11：无映射 → 原样透传（body 与 Content-Type 不变）。
func TestMappedMultipartBodyPassthroughWhenNoMapping(t *testing.T) {
	body, ct := buildMultipart(t, "whisper-1")
	ch := &Channel{ModelMapping: ""}
	newBody, newCT := mappedMultipartBody(ch, body, ct, "whisper-1")
	if !bytes.Equal(newBody, body) || newCT != ct {
		t.Fatal("no-mapping case must pass through body+ct unchanged")
	}
}

func readFileField(t *testing.T, body []byte, ct string) string {
	t.Helper()
	_, params, _ := mime.ParseMediaType(ct)
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		p, e := mr.NextPart()
		if e != nil {
			return ""
		}
		if p.FileName() != "" {
			v, _ := io.ReadAll(p)
			return string(v)
		}
	}
}
