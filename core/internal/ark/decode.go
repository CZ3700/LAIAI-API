package ark

import (
	"bytes"
	"encoding/json"
	"io"
)

var createFields = []string{"model", "content", "duration", "frames", "resolution", "ratio", "seed", "generate_audio", "watermark", "return_last_frame", "draft", "service_tier", "omni_reference_task_type", "output_format", "execution_expires_after", "max_charge_usd", "expected_pricing_version"}

// Decode accepts one bounded JSON object, rejecting unknown/case-variant keys,
// duplicate keys at every depth, null values and extra JSON values. It does not
// perform model-specific capability or ownership checks; call Normalize next.
func Decode(r io.Reader, maxBytes int64) (CreateRequest, error) {
	var out CreateRequest
	if r == nil {
		return out, failure("invalid_request", "request body is required", "body", 400)
	}
	if maxBytes <= 0 || maxBytes > DefaultMaxRequestBytes {
		maxBytes = DefaultMaxRequestBytes
	}
	b, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return out, failure("invalid_request", "request body could not be read", "body", 400)
	}
	if int64(len(b)) > maxBytes {
		return out, failure("request_too_large", "request body exceeds the limit", "body", 413)
	}
	if !uniqueJSON(b) {
		return out, failure("invalid_request", "invalid or duplicate JSON fields", "body", 400)
	}
	obj, ok := closedObject(b, createFields)
	if !ok {
		return out, failure("invalid_request", "unknown, null, or invalid request fields", "body", 422)
	}
	if _, ok := obj["model"]; !ok {
		return out, failure("invalid_request", "model is required", "model", 422)
	}
	var content []json.RawMessage
	if json.Unmarshal(obj["content"], &content) != nil || len(content) == 0 || len(content) > 51 {
		return out, failure("invalid_request", "content must contain between 1 and 51 items", "content", 422)
	}
	for _, raw := range content {
		item, valid := closedObject(raw, []string{"type", "text", "image_url", "video_url", "audio_url", "draft_task", "role"})
		if !valid {
			return out, failure("invalid_request", "invalid content fields", "content", 422)
		}
		var kind string
		if json.Unmarshal(item["type"], &kind) != nil {
			return out, failure("invalid_request", "content type is required", "content", 422)
		}
		allowed := []string{"type", kind}
		if kind == "image_url" || kind == "video_url" || kind == "audio_url" {
			allowed = append(allowed, "role")
		}
		if !contains([]string{"text", "image_url", "video_url", "audio_url", "draft_task"}, kind) {
			return out, failure("invalid_request", "unsupported content type", "content", 422)
		}
		if _, valid := closedObject(raw, allowed); !valid {
			return out, failure("invalid_request", "content fields do not match its type", "content", 422)
		}
		if role, present := item["role"]; present {
			var value string
			if json.Unmarshal(role, &value) != nil || value == "" {
				return out, failure("invalid_request", "content role must not be empty", "content", 422)
			}
		}
		for _, name := range []string{"image_url", "video_url", "audio_url", "draft_task"} {
			if ref, present := item[name]; present {
				key := "url"
				if name == "draft_task" {
					key = "id"
				}
				fields, good := closedObject(ref, []string{key})
				if !good || len(fields) != 1 {
					return out, failure("invalid_request", "invalid content reference", "content", 422)
				}
			}
		}
	}
	if json.Unmarshal(b, &out) != nil {
		return CreateRequest{}, failure("invalid_request", "request field has an invalid type or range", "body", 422)
	}
	// Prevent explicit empty optional strings from silently becoming defaults.
	for _, p := range []*string{out.Resolution, out.Ratio, out.ServiceTier, out.OmniReferenceTaskType, out.OutputFormat, out.ExpectedPricingVersion} {
		if p != nil && *p == "" {
			return CreateRequest{}, failure("invalid_request", "optional strings must not be empty", "body", 422)
		}
	}
	return out, nil
}

func closedObject(raw []byte, allowed []string) (map[string]json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, false
	}
	for key, value := range obj {
		if !contains(allowed, key) || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, false
		}
	}
	return obj, true
}

func uniqueJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if scanJSON(d, 0) != nil {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func scanJSON(d *json.Decoder, depth int) error {
	if depth > 64 {
		return io.ErrUnexpectedEOF
	}
	tok, err := d.Token()
	if err != nil {
		return err
	}
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			keyToken, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return io.ErrUnexpectedEOF
			}
			seen[key] = true
			if err := scanJSON(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return io.ErrUnexpectedEOF
		}
	case '[':
		for d.More() {
			if err := scanJSON(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return io.ErrUnexpectedEOF
		}
	default:
		return io.ErrUnexpectedEOF
	}
	return nil
}

func contains(values []string, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}
