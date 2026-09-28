// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostops

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// DecodeClosed accepts one UTF-8 JSON object, at most 16 KiB and eight container
// levels. Duplicate names, null, trailing data and unknown members are refused.
func DecodeClosed(data []byte, dst any) error {
	if len(data) < 2 || len(data) > 16384 || !utf8.Valid(data) {
		return errors.New("input_refused")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("input_refused")
	}
	if err := jsonContainer(dec, '{', 1); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("input_refused")
	}
	dec = json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.New("input_refused")
	}
	return nil
}

func jsonContainer(d *json.Decoder, kind json.Delim, depth int) error {
	if depth > 8 {
		return errors.New("input_refused")
	}
	seen := map[string]bool{}
	for d.More() {
		if kind == '{' {
			key, err := d.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] {
				return errors.New("input_refused")
			}
			seen[name] = true
		}
		token, err := d.Token()
		if err != nil || token == nil {
			return errors.New("input_refused")
		}
		if delim, ok := token.(json.Delim); ok {
			if delim != '{' && delim != '[' {
				return errors.New("input_refused")
			}
			if err := jsonContainer(d, delim, depth+1); err != nil {
				return err
			}
		}
	}
	close, err := d.Token()
	want := json.Delim('}')
	if kind == '[' {
		want = ']'
	}
	if err != nil || close != want {
		return errors.New("input_refused")
	}
	return nil
}
