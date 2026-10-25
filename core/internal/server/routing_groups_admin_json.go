package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const routingAdminBodyLimit = 16 << 10

var errRoutingAdminInput = errors.New("invalid routing group request")

// Validate tokens before decoding: encoding/json alone permits duplicate keys,
// case-insensitive field names and null values in scalar destinations.
func routingAdminJSON(c *gin.Context, out any, fields ...string) error {
	return routingAdminJSONNullable(c, out, nil, fields...)
}

// Existing commands supply no nullable paths. Only the default command permits
// two explicit top-level nulls; a same-named nested value remains forbidden.
func routingAdminJSONNullable(c *gin.Context, out any, nullable map[string]bool, fields ...string) error {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, routingAdminBodyLimit+1))
	if err != nil || len(body) > routingAdminBodyLimit || !utf8.Valid(body) {
		return errRoutingAdminInput
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if err := routingAdminJSONValueAt(d, 0, true, "", nullable); err != nil {
		return errRoutingAdminInput
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return errRoutingAdminInput
	}
	return routingAdminObject(body, out, fields...)
}

func routingAdminJSONValue(d *json.Decoder, depth int, object bool) error {
	return routingAdminJSONValueAt(d, depth, object, "", nil)
}

func routingAdminJSONValueAt(d *json.Decoder, depth int, object bool, path string, nullable map[string]bool) error {
	if depth > 8 {
		return errRoutingAdminInput
	}
	token, err := d.Token()
	if err != nil {
		return errRoutingAdminInput
	}
	if token == nil {
		if !object && depth == 1 && nullable[path] {
			return nil
		}
		return errRoutingAdminInput
	}
	delim, composite := token.(json.Delim)
	if object && (!composite || delim != '{') {
		return errRoutingAdminInput
	}
	if !composite {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for d.More() {
			key, err := d.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] {
				return errRoutingAdminInput
			}
			seen[name] = true
			if err := routingAdminJSONValueAt(d, depth+1, false, path+"/"+name, nullable); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := routingAdminJSONValueAt(d, depth+1, false, path+"/[]", nullable); err != nil {
				return err
			}
		}
	default:
		return errRoutingAdminInput
	}
	end, err := d.Token()
	if err != nil || delim == '{' && end != json.Delim('}') || delim == '[' && end != json.Delim(']') {
		return errRoutingAdminInput
	}
	return nil
}

func routingAdminObject(body []byte, out any, fields ...string) error {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(body, &values); err != nil || values == nil {
		return errRoutingAdminInput
	}
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	for key := range values {
		if !allowed[key] {
			return errRoutingAdminInput
		}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return errRoutingAdminInput
	}
	return nil
}

func routingAdminQuery(c *gin.Context, fields ...string) (url.Values, error) {
	if len(c.Request.URL.RawQuery) > routingAdminBodyLimit {
		return nil, errRoutingAdminInput
	}
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return nil, errRoutingAdminInput
	}
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	for key, values := range values {
		if !allowed[key] || len(values) != 1 {
			return nil, errRoutingAdminInput
		}
	}
	return values, nil
}

func routingAdminUint(raw string, zero bool) (uint64, error) {
	if raw == "" {
		return 0, errRoutingAdminInput
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, errRoutingAdminInput
		}
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || !zero && n == 0 {
		return 0, errRoutingAdminInput
	}
	return n, nil
}
