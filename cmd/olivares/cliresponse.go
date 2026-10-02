// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// readCLIResponse is the buffered refusal seam. Each caller keeps its read bound
// and status policy. Successful bytes, including raw files, remain unchanged.
// Secrets come from the actual request, after flag/env/context resolution.
func readCLIResponse(resp *http.Response, req *http.Request, limit int64, accepted bool) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err == nil && accepted {
		return raw, nil
	}
	// Refusals cannot safely repeat a body credential that fell outside the
	// bounded request inspection. Accepted responses above remain byte-exact.
	if req != nil && req.Body != nil && strings.HasPrefix(req.Header.Get("Content-Type"), "application/json") &&
		(req.GetBody == nil || req.ContentLength < 0 || req.ContentLength > maxBootstrapCLIResponseSize) {
		if err != nil {
			return nil, &cliSafeError{message: "could not read response; details withheld because request credentials could not be inspected", cause: err}
		}
		return []byte("response details withheld because request credentials could not be inspected"), nil
	}
	secrets := cliRequestSecrets(req)
	if err != nil {
		return nil, redactCoded(err, secrets...)
	}
	// A capped excerpt may end in a credential prefix. Withhold the excerpt rather
	// than matching only the complete credential and exposing its partial value.
	if int64(len(raw)) == limit && len(secrets) > 0 {
		return []byte("response details withheld because the error body reached its byte limit"), nil
	}
	return redactCLIResponse(raw, secrets), nil
}

// Refusals are formatted while their actual request credentials are still
// available. The error keeps that context through wrapping to the final write.
func readCLIHTTPResponse(resp *http.Response, req *http.Request, limit int64, accepted bool, refusal func(int, []byte) error) ([]byte, error) {
	raw, err := readCLIResponse(resp, req, limit, accepted)
	if err != nil || accepted {
		return raw, err
	}
	return raw, guardCLIRefusalError(refusal(resp.StatusCode, raw), resp.StatusCode, cliRequestSecrets(req))
}

func wrapCLIResponseReadError(err error, message string) error {
	var refusal *cliRefusalError
	if errors.As(err, &refusal) {
		return err
	}
	return exitcode.New(exitcode.Server, fmt.Errorf("%s: %w", message, err))
}

type cliRefusalError struct {
	cause   error
	status  int
	secrets []string
}

func guardCLIRefusalError(err error, status int, secrets []string, readErrors ...error) error {
	if err == nil {
		return nil
	}
	for _, readErr := range readErrors {
		if readErr != nil {
			err = &cliSafeError{message: err.Error(), cause: errors.Join(err, readErr)}
		}
	}
	return &cliRefusalError{cause: err, status: status, secrets: secrets}
}

func (e *cliRefusalError) withheld() string {
	return fmt.Sprintf("response details withheld (HTTP %d)", e.status)
}

func (e *cliRefusalError) Error() string {
	message := e.cause.Error()
	if cliOutputContainsCredential(message, e.secrets) {
		return e.withheld()
	}
	return message
}

func (e *cliRefusalError) Unwrap() error { return e.cause }

// Check the exact bytes after the final formatter, including any outer error
// wrapper. Both output modes return refusals here; successful output is separate.
func printCLIError(out io.Writer, err error) error { return printCLIErrorAs(out, err, false) }

// cliErrorJSON is the -o json form of an error: one line on stderr with the message and,
// for an engine refusal, its status and code.
func cliErrorJSON(message string, err error) []byte {
	e := map[string]any{"message": message}
	var refusal *apiRefusal
	if errors.As(err, &refusal) {
		e["status"] = refusal.status
		if refusal.code != "" {
			e["code"] = refusal.code
		}
	}
	b, _ := json.Marshal(map[string]any{"error": e})
	return append(b, '\n')
}

// printCLIErrorAs writes err as "Error: <message>" or, with asJSON, as one JSON line.
func printCLIErrorAs(out io.Writer, err error, asJSON bool) error {
	body := fmt.Appendln(nil, "Error:", err)
	if asJSON {
		body = cliErrorJSON(err.Error(), err)
	}
	var first *cliRefusalError
	var secrets []string
	var visit func(error)
	visit = func(cause error) {
		if cause == nil {
			return
		}
		if refusal, ok := cause.(*cliRefusalError); ok {
			if first == nil {
				first = refusal
			}
			secrets = append(secrets, refusal.secrets...)
		}
		// Redaction closes public unwrapping of unsafe text. Its privately held
		// cause still carries the request credentials needed for this last check.
		if redacted, ok := cause.(*cliRedactedError); ok {
			visit(redacted.cause)
		}
		if joined, ok := cause.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				visit(child)
			}
		} else {
			visit(errors.Unwrap(cause))
		}
	}
	visit(err)
	if first != nil && cliOutputContainsCredential(string(body), secrets) {
		body = fmt.Appendln(nil, "Error:", first.withheld())
		if asJSON {
			body = cliErrorJSON(first.withheld(), nil)
		}
	}
	_, writeErr := out.Write(body)
	return writeErr
}

// Match credential bytes independently of JSON scalar type. Decode valid JSON
// escapes wherever they occur, including mixed Unicode escapes and nested JSON
// strings, so matching is against the final diagnostic rather than its DTO.
// A lower-trust escape chain may expose only one escape per pass. An incomplete
// scan withholds details just like a credential match; it never permits output.
func cliOutputContainsCredential(output string, secrets []string) bool {
	withhold, _ := scanCLIOutputCredentials(output, secrets)
	return withhold
}

const (
	maxCLIOutputDecodePasses = 8
	cliOutputWorkPerByte     = 64
)

// Charge the input bytes of every full-string search, quoting pass, JSON decode
// and comparison before doing that work. Each operation has bounded work per
// input byte; quoted JSON is at most six bytes per source byte plus two quotes.
type cliOutputScan struct {
	decodePasses   int
	bytesProcessed uint64
	maxBytes       uint64
	limited        bool
}

func (s *cliOutputScan) consume(length int) bool {
	if uint64(length) > s.maxBytes-s.bytesProcessed {
		s.limited = true
		return false
	}
	s.bytesProcessed += uint64(length)
	return true
}

func scanCLIOutputCredentials(output string, secrets []string) (bool, cliOutputScan) {
	scan := cliOutputScan{maxBytes: uint64(len(output)) * cliOutputWorkPerByte}
	if len(secrets) == 0 {
		return false, scan
	}
	if scan.maxBytes/cliOutputWorkPerByte != uint64(len(output)) {
		scan.limited = true
		return true, scan
	}
	for {
		for _, secret := range secrets {
			if secret != "" && (!scan.consume(len(output)) || strings.Contains(output, secret)) {
				return true, scan
			}
		}
		if !scan.consume(len(output)) {
			return true, scan
		}
		if !strings.Contains(output, `\`) {
			return false, scan
		}
		if scan.decodePasses == maxCLIOutputDecodePasses {
			scan.limited = true
			return true, scan
		}
		decoded, complete := decodeCLIOutputEscapes(output, &scan)
		if !complete || !scan.consume(len(output)) {
			return true, scan
		}
		if decoded == output {
			return false, scan
		}
		output = decoded
	}
}

func decodeCLIOutputEscapes(output string, scan *cliOutputScan) (string, bool) {
	if !scan.consume(len(output)) {
		return "", false
	}
	scan.decodePasses++
	// Quote literal bytes while retaining valid escape sequences for the JSON
	// decoder. Unknown escapes remain literal; surrogate pairs use JSON semantics.
	quoted := make([]byte, 0, len(output)+2)
	quoted = append(quoted, '"')
	for i := 0; i < len(output); {
		c := output[i]
		escapeLen := 0
		if c == '\\' && i+1 < len(output) {
			if strings.ContainsRune(`"\/bfnrt`, rune(output[i+1])) {
				escapeLen = 2
			} else if output[i+1] == 'u' && i+6 <= len(output) {
				valid := true
				for _, digit := range output[i+2 : i+6] {
					valid = valid && strings.ContainsRune("0123456789abcdefABCDEF", digit)
				}
				if valid {
					escapeLen = 6
				}
			}
		}
		if escapeLen != 0 {
			quoted = append(quoted, output[i:i+escapeLen]...)
			i += escapeLen
			continue
		}
		switch {
		case c == '"' || c == '\\':
			quoted = append(quoted, '\\', c)
		case c < 0x20:
			const hex = "0123456789abcdef"
			quoted = append(quoted, '\\', 'u', '0', '0', hex[c>>4], hex[c&15])
		default:
			quoted = append(quoted, c)
		}
		i++
	}
	quoted = append(quoted, '"')
	if !scan.consume(len(quoted)) {
		return "", false
	}
	var decoded string
	if json.Unmarshal(quoted, &decoded) != nil {
		return output, true
	}
	return decoded, true
}

func cliRequestSecrets(req *http.Request) []string {
	if req == nil {
		return nil
	}
	var secrets []string
	add := func(value string) {
		if value == "" {
			return
		}
		secrets = append(secrets, value)
		encoded, _ := json.Marshal(value)
		secrets = append(secrets, string(encoded[1:len(encoded)-1]), url.QueryEscape(value))
	}
	authorization := req.Header.Get("Authorization")
	add(authorization)
	add(strings.TrimPrefix(authorization, "Bearer "))
	if req.URL != nil {
		add(req.URL.Query().Get("token"))
	}
	if req.GetBody != nil && strings.HasPrefix(req.Header.Get("Content-Type"), "application/json") {
		body, err := req.GetBody()
		if err == nil {
			defer body.Close()
			var value any
			if json.NewDecoder(io.LimitReader(body, maxBootstrapCLIResponseSize+1)).Decode(&value) == nil {
				collectCLIRequestSecrets(value, add)
			}
		}
	}
	return secrets
}

func collectCLIRequestSecrets(value any, add func(string)) {
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			switch strings.ToLower(key) {
			case "password", "setup_token", "token", "access_token", "refresh_token", "api_key", "credential", "secret", "private_key":
				if secret, ok := item.(string); ok {
					add(secret)
				}
			}
			collectCLIRequestSecrets(item, add)
		}
	case []any:
		for _, item := range value {
			collectCLIRequestSecrets(item, add)
		}
	}
}

// Decode before redaction so a credential cannot survive inside a JSON escape.
// The existing redactor also preserves its short-credential withholding rule.
func redactCLIResponse(raw []byte, secrets []string) []byte {
	if len(secrets) == 0 {
		return raw
	}
	var value, tail any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	// Literal matching cannot prove an unparseable body safe: escaped strings
	// can remain recoverable in an incomplete value or before an invalid suffix.
	if decoder.Decode(&value) != nil || decoder.Decode(&tail) != io.EOF {
		return []byte("response details withheld")
	}
	value = redactCLIResponseValue(value, secrets)
	var safe bytes.Buffer
	encoder := json.NewEncoder(&safe)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return []byte("response details withheld")
	}
	result := bytes.TrimSuffix(safe.Bytes(), []byte("\n"))
	if cliOutputContainsCredential(string(result), secrets) {
		return []byte("response details withheld")
	}
	return result
}

func redactCLIResponseValue(value any, secrets []string) any {
	switch value := value.(type) {
	case string:
		return redactCLISecrets(value, secrets...)
	case map[string]any:
		safe := make(map[string]any, len(value))
		for key, item := range value {
			safe[redactCLISecrets(key, secrets...)] = redactCLIResponseValue(item, secrets)
		}
		return safe
	case []any:
		for i, item := range value {
			value[i] = redactCLIResponseValue(item, secrets)
		}
	}
	return value
}

// An omitted list means the ordinary JSON/read contract, HTTP 200. Mutations
// pass their actual accepted statuses instead of treating every 2xx as success.
func cliStatusAccepted(status int, accepted ...int) bool {
	if len(accepted) == 0 {
		return status == http.StatusOK
	}
	for _, want := range accepted {
		if status == want {
			return true
		}
	}
	return false
}
