// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package answers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxInputBytes = 64 * 1024

// ErrCannotRead reports an input transport failure without exposing the underlying error.
var ErrCannotRead = errors.New("cannot read answers input")

// ValidationError reports only schema-owned paths and fixed reasons, never input values.
type ValidationError struct{ Path, Reason string }

func (e *ValidationError) Error() string { return e.Path + ": " + e.Reason }

func invalid(path, reason string) error { return &ValidationError{Path: path, Reason: reason} }

func readDocument(r io.Reader) (document, error) {
	var d document
	if r == nil {
		return d, ErrCannotRead
	}
	b, err := io.ReadAll(io.LimitReader(r, maxInputBytes+1))
	if err != nil {
		return d, ErrCannotRead
	}
	if len(b) > maxInputBytes {
		return d, invalid("$", "input exceeds 64 KiB")
	}
	if !utf8.Valid(b) {
		return d, invalid("$", "input must be UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	if err := decodeValue(decoder, reflect.ValueOf(&d).Elem(), "$", 0); err != nil {
		return document{}, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return document{}, invalid("$", "expected exactly one JSON document")
	}
	return d, nil
}

// schemaField returns a field's JSON name and whether a document may omit it.
func schemaField(field reflect.StructField) (name string, optional bool) {
	name, option, _ := strings.Cut(field.Tag.Get("json"), ",")
	return name, option == "omitempty"
}

// decodeValue derives the structural schema from the private document types. Fields are
// required unless tagged omitempty; an optional field is a pointer or a slice, so absence
// stays distinguishable from a zero value. Reading tokens avoids encoding/json's
// last-duplicate-wins behavior and keeps unknown field names and parser diagnostics out
// of user-visible errors.
func decodeValue(d *json.Decoder, target reflect.Value, path string, depth int) error {
	if depth > 8 {
		return invalid(path, "nesting exceeds 8 levels")
	}
	if target.Kind() == reflect.Pointer {
		value := reflect.New(target.Type().Elem())
		if err := decodeValue(d, value.Elem(), path, depth); err != nil {
			return err
		}
		target.Set(value)
		return nil
	}
	token, err := d.Token()
	if err != nil {
		return invalid(path, "invalid or incomplete JSON")
	}
	switch target.Kind() {
	case reflect.String:
		value, ok := token.(string)
		if !ok {
			return invalid(path, "expected string")
		}
		target.SetString(value)
	case reflect.Bool:
		value, ok := token.(bool)
		if !ok {
			return invalid(path, "expected boolean")
		}
		target.SetBool(value)
	case reflect.Struct:
		if token != json.Delim('{') {
			return invalid(path, "expected object")
		}
		typ := target.Type()
		fields := make(map[string]int, typ.NumField())
		for i := 0; i < typ.NumField(); i++ {
			name, _ := schemaField(typ.Field(i))
			fields[name] = i
		}
		seen := make(map[string]bool, len(fields))
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return invalid(path, "invalid object key")
			}
			name, ok := key.(string)
			if !ok {
				return invalid(path, "expected object key")
			}
			i, known := fields[name]
			if !known {
				return invalid(path, "unknown field")
			}
			if seen[name] {
				return invalid(path, "duplicate object key")
			}
			seen[name] = true
			if err := decodeValue(d, target.Field(i), path+"."+name, depth+1); err != nil {
				return err
			}
		}
		if end, err := d.Token(); err != nil || end != json.Delim('}') {
			return invalid(path, "invalid object ending")
		}
		for i := 0; i < typ.NumField(); i++ {
			name, optional := schemaField(typ.Field(i))
			if !seen[name] && !optional {
				return invalid(path+"."+name, "required field is missing")
			}
		}
	case reflect.Slice:
		if token != json.Delim('[') {
			return invalid(path, "expected array")
		}
		values := reflect.MakeSlice(target.Type(), 0, 0)
		for d.More() {
			if values.Len() >= 16 {
				return invalid(path, "array exceeds 16 entries")
			}
			value := reflect.New(target.Type().Elem()).Elem()
			if err := decodeValue(d, value, path+"["+strconv.Itoa(values.Len())+"]", depth+1); err != nil {
				return err
			}
			values = reflect.Append(values, value)
		}
		if end, err := d.Token(); err != nil || end != json.Delim(']') {
			return invalid(path, "invalid array ending")
		}
		target.Set(values)
	default:
		return invalid(path, "unsupported schema type")
	}
	return nil
}
