package invocation

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

var videoIdentifier = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
var videoBearer = regexp.MustCompile(`(?i)\bBearer\s+[^\s"'<>]+`)
var videoURL = regexp.MustCompile(`https?://[^\s"'<>]+`)
var videoInlineSecret = regexp.MustCompile(`(?i)\b(authorization|proxy-authorization|cookie|set-cookie|api[_-]?key|api[_-]?secret|password|passwd|access[_-]?key(?:[_-]?id)?|secret[_-]?(?:access[_-]?)?key|bytedtoken|h5link|h5url|access[_-]?token|refresh[_-]?token|session[_-]?token|client[_-]?secret|token)\s*[:=]\s*(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;&<>]+)`)

func videoValidID(value string, optional bool) bool {
	return optional && value == "" || len(value) <= 64 && videoIdentifier.MatchString(value)
}

func videoRandomID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", ErrVideoStorage
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}

func videoHash(body []byte) string {
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

func videoAAD(meta VideoPayload) []byte {
	// JSON tuple avoids ambiguous string concatenation. ObjectRef and state may
	// change during upload/expiry; cryptographic identity never changes.
	fields := []any{"laiai:video-payload:v1", meta.OrgID, meta.PayloadID,
		meta.RequestID, meta.AttemptID, meta.Direction, meta.Purpose, meta.SchemaVersion,
		meta.RedactionVersion, meta.BodyFormat, meta.EncryptionKeyID, meta.ExpiresAt.UTC().UnixMilli()}
	// Existing ciphertext keeps its original AAD; new representation claims are authenticated.
	if meta.CaptureRepresentation != "" && meta.CaptureRepresentation != "legacy_unknown" {
		fields = append(fields, meta.CaptureRepresentation)
	}
	raw, _ := json.Marshal(fields)
	return raw
}

func (s *VideoStore) Prepare(input VideoPayloadInput) (*PreparedVideoPayload, error) {
	if input.CaptureRepresentation == "" {
		input.CaptureRepresentation = "legacy_unknown"
	}
	switch input.CaptureRepresentation {
	case "legacy_unknown", "legacy_canonical_json", "received_json_redacted", "effective_json_redacted", "response_json_redacted":
	default:
		return nil, ErrVideoInvalid
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	expires := input.ExpiresAt.UTC().Truncate(time.Millisecond)
	if input.OrgID == 0 || !videoValidID(input.RequestID, false) || !videoValidID(input.AttemptID, true) || input.SchemaVersion == 0 ||
		!expires.After(now) || expires.After(now.Add(s.maxRetention)) {
		return nil, ErrVideoInvalid
	}
	switch input.Direction {
	case VideoClientRequest, VideoEffectiveRequest, VideoUpstreamResponse, VideoClientResponse:
	default:
		return nil, ErrVideoInvalid
	}
	if input.Purpose != VideoPurposeExecution && input.Purpose != VideoPurposeHistory {
		return nil, ErrVideoInvalid
	}
	if input.Purpose == VideoPurposeExecution && (!input.CaptureEnabled || input.Direction != VideoEffectiveRequest) {
		return nil, ErrVideoRetentionDenied
	}
	if len(input.Body) > s.maxBytes {
		return nil, ErrVideoInvalid
	}
	id, err := videoRandomID("vpl_")
	if err != nil {
		return nil, err
	}
	correlation, _ := json.Marshal([]any{input.OrgID, input.RequestID, input.AttemptID, input.Direction, input.Purpose, input.SchemaVersion})
	p := &PreparedVideoPayload{metadata: VideoPayload{PayloadID: id, OrgID: input.OrgID,
		RequestID: input.RequestID, AttemptID: input.AttemptID, CorrelationKey: videoHash(correlation),
		Direction: input.Direction, Purpose: input.Purpose, SchemaVersion: input.SchemaVersion, CaptureRepresentation: input.CaptureRepresentation,
		RedactionVersion: "body-secrets-v1", BodyFormat: VideoFormatJSON, Nonce: []byte{}, CreatedAt: now, ExpiresAt: expires, CaptureStatus: VideoCaptureDisabled}}
	if !input.CaptureEnabled {
		return p, nil
	}
	plain, err := videoRedactJSON(input.Body, input.SecretValues)
	if err != nil {
		if input.Purpose != VideoPurposeHistory || input.Direction != VideoUpstreamResponse {
			return nil, err
		}
		p.metadata.BodyFormat = VideoFormatText
		text := videoRedactString(strings.ToValidUTF8(string(input.Body), "\uFFFD"), input.SecretValues)
		truncated := false
		for {
			plain, err = json.Marshal(struct {
				Format        string `json:"format"`
				Text          string `json:"text"`
				OriginalBytes int    `json:"original_bytes"`
				Truncated     bool   `json:"truncated"`
			}{VideoFormatText, text, len(input.Body), truncated})
			if err != nil {
				return nil, ErrVideoInvalid
			}
			if len(plain) <= s.maxBytes {
				break
			}
			clear(plain)
			if len(text) == 0 {
				return nil, ErrVideoInvalid
			}
			text = strings.ToValidUTF8(text[:len(text)/2], "")
			truncated = true
		}
	}
	defer clear(plain)
	if input.Purpose == VideoPurposeExecution {
		value, err := videoDecodeJSON(input.Body)
		if err != nil {
			return nil, err
		}
		canonical, err := json.Marshal(value)
		if err != nil {
			return nil, ErrVideoInvalid
		}
		equal := bytes.Equal(canonical, plain)
		clear(canonical)
		if !equal {
			return nil, ErrVideoRetentionDenied
		}
	}
	// Redaction and JSON escaping can expand the source; enforce the encrypted
	// envelope limit too, without truncating authoritative JSON silently.
	if len(plain) > s.maxBytes {
		return nil, ErrVideoInvalid
	}
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return nil, ErrVideoStorage
	}
	defer clear(dek)
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, ErrVideoCorrupt
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrVideoCorrupt
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrVideoStorage
	}
	wrapped, err := s.keyring.Encrypt(base64.RawStdEncoding.EncodeToString(dek))
	if err != nil {
		return nil, ErrVideoStorage
	}
	parts := strings.SplitN(wrapped, ":", 4)
	if len(parts) != 4 || len(parts[2]) > 128 || !videoIdentifier.MatchString(parts[2]) {
		return nil, ErrVideoCorrupt
	}
	p.metadata.EncryptionKeyID = parts[2]
	p.metadata.WrappedKey = wrapped
	p.metadata.Nonce = nonce
	p.metadata.PlaintextBytes = int64(len(plain))
	p.metadata.CaptureStatus = VideoCaptureStaged
	ciphertext := gcm.Seal(nil, nonce, plain, videoAAD(p.metadata))
	p.metadata.CiphertextSHA256 = videoHash(ciphertext)
	p.staging = VideoPayloadStaging{PayloadID: id, Ciphertext: ciphertext, NextAttemptAt: now, CreatedAt: now}
	return p, nil
}

func (s *VideoStore) decrypt(meta VideoPayload, ciphertext []byte) ([]byte, error) {
	if meta.CiphertextSHA256 == "" || videoHash(ciphertext) != meta.CiphertextSHA256 {
		return nil, ErrVideoCorrupt
	}
	wrapped, err := s.keyring.Decrypt(meta.WrappedKey)
	if err != nil {
		return nil, ErrVideoCorrupt
	}
	dek, err := base64.RawStdEncoding.DecodeString(wrapped)
	defer clear(dek)
	if err != nil || len(dek) != 32 {
		return nil, ErrVideoCorrupt
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, ErrVideoCorrupt
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(meta.Nonce) != gcm.NonceSize() {
		return nil, ErrVideoCorrupt
	}
	plain, err := gcm.Open(nil, meta.Nonce, ciphertext, videoAAD(meta))
	if err != nil || int64(len(plain)) != meta.PlaintextBytes {
		clear(plain)
		return nil, ErrVideoCorrupt
	}
	return plain, nil
}

func videoSecretName(name string) bool {
	name = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, name)
	switch name {
	case "authorization", "proxyauthorization", "cookie", "setcookie", "apikey", "apisecret", "password", "passwd",
		"accesskey", "accesskeyid", "secretaccesskey", "secretkey", "secret", "bytedtoken", "h5link", "h5url", "authorizationurl",
		"sessiontoken", "accesstoken", "refreshtoken", "idtoken", "clientsecret", "privatekey", "credential", "credentials",
		"paymentcredential", "cardnumber", "cvv", "token":
		return true
	}
	return false
}

func videoRedactString(value string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	value = videoBearer.ReplaceAllString(value, "Bearer [REDACTED]")
	value = videoInlineSecret.ReplaceAllString(value, "$1=[REDACTED]")
	return videoURL.ReplaceAllStringFunc(value, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[REDACTED_URL]"
		}
		if u.User != nil {
			u.User = nil
		}
		q := u.Query()
		changed := false
		for key := range q {
			if videoSecretName(key) {
				q.Del(key)
				changed = true
			}
		}
		if changed {
			u.RawQuery = q.Encode()
		}
		return u.String()
	})
}

func videoDecodeJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, ErrVideoInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, ErrVideoInvalid
	}
	return value, nil
}

func videoRedactJSON(raw []byte, secrets []string) ([]byte, error) {
	value, err := videoDecodeJSON(raw)
	if err != nil {
		return nil, err
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for key, child := range x {
				if videoSecretName(key) {
					delete(x, key)
				} else {
					x[key] = walk(child)
				}
			}
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
		case string:
			return videoRedactString(x, secrets)
		}
		return v
	}
	result, err := json.Marshal(walk(value))
	if err != nil {
		return nil, ErrVideoInvalid
	}
	return result, nil
}
