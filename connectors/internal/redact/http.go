// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package redact

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// OmittedHTTPError is the stable reason for an unreadable or oversized diagnostic.
const OmittedHTTPError = `{"error":"provider diagnostic omitted: incomplete or too large"}`

// A text assignment owns the rest of its line. In particular, Authorization
// and Cookie values can contain spaces or several credentials.
var httpAssignment = regexp.MustCompile(`(?im)(?:authorization|proxy[-_]authorization|(?:set[-_])?cookie|[a-z0-9_-]*(?:api[-_]?key|password|passwd|secret|token|credential|assertion)|request|headers|body|prompt|messages|debug|input|payload)["']?\s*[:=][^\r\n]*`)

// ReadHTTPError reads only a bounded rejection and removes credentials before
// producing a diagnostic. Partial reads and oversized bodies cannot be safely
// scrubbed: their last bytes may be just a prefix of a secret.
func ReadHTTPError(body io.Reader, limit int, req *http.Request, credentials ...string) string {
	raw, err := io.ReadAll(io.LimitReader(body, int64(limit)+1))
	if err != nil {
		return OmittedHTTPError
	}
	return HTTPError(raw, limit, req, credentials...)
}

// HTTPError cleans an already bounded provider rejection. It never reads the
// request body. Callers supply credentials sent in a form or JSON explicitly.
// Complete JSON stays JSON, including numeric protocol status codes. Successful
// response payloads must not be passed here.
func HTTPError(raw []byte, limit int, req *http.Request, credentials ...string) string {
	if len(raw) > limit {
		return OmittedHTTPError
	}
	known := append([]string(nil), credentials...)
	// An explicitly supplied URL can itself be the credential (webhooks). Its
	// path segments and every query value are then confidential, regardless of
	// the provider's names for them. Ordinary request URLs keep their public
	// path and pagination values below.
	for _, credential := range credentials {
		if u, err := url.Parse(credential); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			known = append(known, u.Path, u.EscapedPath(), u.RawQuery, u.Fragment)
			known = append(known, strings.Split(u.Path, "/")...)
			known = append(known, strings.Split(u.EscapedPath(), "/")...)
			for _, field := range strings.Split(u.RawQuery, "&") {
				if _, value, ok := strings.Cut(field, "="); ok {
					known = append(known, value)
				}
			}
			for _, values := range u.Query() {
				known = append(known, values...)
			}
		}
	}
	if req != nil {
		for _, values := range req.Header {
			known = append(known, values...)
		}
		if _, value, ok := strings.Cut(req.Header.Get("Authorization"), " "); ok {
			known = append(known, value)
			if id, secret, pair := strings.Cut(value, ":"); pair {
				known = append(known, id, secret)
			}
		}
		if username, password, ok := req.BasicAuth(); ok {
			known = append(known, username, password)
		}
		for _, cookie := range req.Cookies() {
			known = append(known, cookie.Value)
		}
		if req.URL != nil {
			if req.URL.User != nil {
				password, _ := req.URL.User.Password()
				known = append(known, req.URL.User.Username(), password)
			}
			for key, values := range req.URL.Query() {
				if sensitiveHTTPField(key) {
					known = append(known, values...)
				}
			}
		}
	}
	// OAuth form encoding and URL path encoding are observable representations
	// of the same credential. Do not read or replay a request body to find them.
	for _, credential := range append([]string(nil), known...) {
		known = append(known, url.QueryEscape(credential), url.PathEscape(credential))
	}
	// Longest first avoids leaving the tail when two credentials overlap.
	sort.Slice(known, func(i, j int) bool { return len(known[i]) > len(known[j]) })
	cleanText := func(text string) string {
		originalSize := len(text)
		for _, value := range known {
			if value != "" {
				text = strings.ReplaceAll(text, value, Placeholder)
			}
		}
		// The existing PEM recognizer matches only its header; omit the entire
		// string instead of preserving private-key material below that header.
		if strings.Contains(text, "PRIVATE KEY-----") {
			return Placeholder
		}
		text = httpAssignment.ReplaceAllStringFunc(text, func(field string) string {
			if pos := strings.IndexAny(field, ":="); pos >= 0 {
				return field[:pos+1] + Placeholder
			}
			return Placeholder
		})
		text = Clean(text)
		// Expanding a short secret into a marker must not push a complete JSON
		// verdict over its budget and erase its protocol status code.
		if len(text) > originalSize {
			return "*"
		}
		return text
	}

	// A provider may prepend a UTF-8 BOM to JSON. Normalize it only after the
	// wire budget check, then decode escapes before comparing credentials.
	normalized := bytes.TrimPrefix(bytes.TrimSpace(raw), []byte("\xef\xbb\xbf"))
	out := strings.TrimSpace(string(normalized))
	decoder := json.NewDecoder(bytes.NewReader(normalized))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) == nil {
		var trailing any
		if decoder.Decode(&trailing) == io.EOF {
			var cleanValue func(any, string, bool) any
			cleanValue = func(value any, field string, compact bool) any {
				switch v := value.(type) {
				case map[string]any:
					for key, child := range v {
						if !protocolHTTPField(key) && (compact || cleanText(key) != key) {
							delete(v, key) // Do not rename confidential keys into collisions.
						} else if sensitiveHTTPField(key) || (req != nil && req.Header.Get(key) != "") {
							delete(v, key)
						} else {
							v[key] = cleanValue(child, key, compact)
						}
					}
				case []any:
					for i, child := range v {
						v[i] = cleanValue(child, "", compact)
					}
				case string:
					if compact && field != "status" && field != "error" && field != "type" {
						return "*"
					}
					return cleanText(v)
				case json.Number:
					if field != "code" && field != "error_code" && field != "status" && field != "status_code" && cleanText(v.String()) != v.String() {
						return "*"
					}
				case bool:
					if text := strconv.FormatBool(v); field != "ok" && field != "success" && field != "errors" && cleanText(text) != text {
						return "*"
					}
				}
				return value
			}
			value = cleanValue(value, "", false)
			var cleaned bytes.Buffer
			encoder := json.NewEncoder(&cleaned)
			encoder.SetEscapeHTML(false)
			_ = encoder.Encode(value)
			if cleaned.Len()-1 > limit {
				// JSON can still expand Unicode separators. Retain the protocol
				// verdict while dropping verbose details that no longer fit.
				cleaned.Reset()
				_ = encoder.Encode(cleanValue(value, "", true))
			}
			// Always render the sanitized value. A duplicate JSON field may have
			// contained a secret before the decoder kept its later safe value.
			out = strings.TrimSuffix(cleaned.String(), "\n")
		} else {
			return OmittedHTTPError
		}
	} else {
		// Broken JSON may contain escaped secrets that text replacement cannot
		// decode. Do not relay a structurally incomplete diagnostic.
		if strings.ContainsAny(out, "{}[]\"\\") {
			return OmittedHTTPError
		}
		out = cleanText(out)
	}
	if len(out) > limit {
		return OmittedHTTPError
	}
	return out
}

// Public protocol names are structure, even when a short credential happens to
// occur inside one (for example, "de" in "code"). Their values are still scrubbed.
func protocolHTTPField(key string) bool {
	switch key {
	case "code", "error_code", "status", "status_code", "ok", "success", "error", "errors", "type", "text", "message", "request_id":
		return true
	}
	return false
}

func sensitiveHTTPField(key string) bool {
	key = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, key)
	switch key {
	case "auth", "authorization", "proxyauthorization", "cookie", "setcookie", "pwd",
		"request", "headers", "body", "requestbody", "requestheaders", "prompt", "messages", "input", "payload", "debug":
		return true
	}
	for _, fragment := range []string{"apikey", "accesskey", "privatekey", "password", "passwd", "secret", "token", "credential", "assertion"} {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}
