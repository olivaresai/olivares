// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package redact

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode"
)

// The existing key vocabulary also identifies credential-bearing free text.
// A partial text scrub cannot safely infer mixed shell quoting or interpolation.
var jsonSensitiveAssignmentRe = regexp.MustCompile(`(?i)` + sensitiveKeyPattern + `["']?\s*[:=]`)

// CleanJSON removes entire sensitive values, including structured values and
// credential-bearing strings. Other values stay complete. The caller bounds
// input; numbers retain their exact spelling. This is not an approval preview.
func CleanJSON(raw []byte) ([]byte, error) {
	return cleanJSON(raw, nil)
}

// ReviewableJSON shares the scrubber but refuses masking that would conceal
// argument behavior. A caller supplies the shared shell-review guard.
func ReviewableJSON(raw []byte, review func(string, string) (string, error)) ([]byte, error) {
	if review == nil {
		return nil, errors.New("redact: review guard is required")
	}
	return cleanJSON(raw, review)
}

func cleanJSON(raw []byte, review func(string, string) (string, error)) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("redact: expected one JSON value")
	}
	cleaned, err := cleanJSONValue(value, review)
	if err != nil {
		return nil, err
	}
	return json.Marshal(cleaned)
}

func cleanJSONValue(value any, review func(string, string) (string, error)) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			// Renaming a confidential key could merge two fields and hide part
			// of the approved plan. Refuse the unreviewable preview instead.
			if ContainsSecret(key) || jsonSensitiveAssignmentRe.MatchString(key) {
				return nil, errors.New("redact: a sensitive JSON key cannot be shown")
			}
			// Reuse the shared key detector's vocabulary, with a fixed sentinel
			// value. JSON owns the whole value; text delimiters cannot truncate it.
			if jsonSensitiveAssignmentRe.MatchString(key + "=redact-sentinel") {
				masked, err := maskJSONValue(child, true, review)
				if err != nil {
					return nil, err
				}
				v[key] = masked
			} else {
				cleaned, err := cleanJSONValue(child, review)
				if err != nil {
					return nil, err
				}
				v[key] = cleaned
			}
		}
	case []any:
		for i, child := range v {
			cleaned, err := cleanJSONValue(child, review)
			if err != nil {
				return nil, err
			}
			v[i] = cleaned
		}
	case string:
		if ContainsSecret(v) || jsonSensitiveAssignmentRe.MatchString(v) {
			return maskJSONValue(v, false, review)
		}
		return v, nil
	}
	return value, nil
}

func maskJSONValue(value any, credential bool, review func(string, string) (string, error)) (any, error) {
	if review == nil {
		return Placeholder, nil // General redaction may hide arbitrary content.
	}
	text, ok := value.(string)
	if !ok {
		return nil, errors.New("redact: masked argument structure is not reviewable")
	}
	// A field name alone proves nothing. Only a complete known credential
	// shape in a credential field can disappear from a human's effect review.
	if credential && literalJSONSecret(text) {
		return Placeholder, nil
	}
	if _, err := review(text, Placeholder); err != nil {
		return nil, err
	}
	return nil, errors.New("redact: masking hides nonsecret argument content")
}

func literalJSONSecret(text string) bool {
	// A bearer recognizer accepts whitespace, but an executable line break
	// cannot qualify as literal credential data in a masked argument.
	if strings.IndexFunc(text, unicode.IsControl) >= 0 {
		return false
	}
	for _, pattern := range wholeMatchPatterns {
		// This recognizer is only a PEM header, not the complete literal key.
		if pattern.label == "private-key" {
			continue
		}
		match := pattern.re.FindStringIndex(text)
		if len(match) == 2 && match[0] == 0 && match[1] == len(text) {
			return true
		}
	}
	return false
}
