// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode"
)

type requestBodyFieldError struct {
	message string
	cause   error
}

func (e *requestBodyFieldError) Error() string { return e.message }
func (e *requestBodyFieldError) Unwrap() error { return e.cause }

// RequestBodyErrorMessage adds only the safe field diagnostic classified by
// DecodeRequestBody. Other failures retain the route's existing wording; raw
// decoder errors can contain Go type names, custom unmarshaler data or I/O details.
func RequestBodyErrorMessage(err error, fallback string) string {
	var field *requestBodyFieldError
	if errors.As(err, &field) {
		return fallback + ": " + field.message
	}
	return fallback
}

func classifyRequestBodyError(err error, body []byte, v any) error {
	diagnostic := json.NewDecoder(bytes.NewReader(body))
	diagnostic.UseNumber()
	var mismatch *json.UnmarshalTypeError
	if errors.As(err, &mismatch) {
		// Custom decoders may return offsets relative to only their own value.
		// Inspect type metadata, never execute a decoder again or render Field.
		if _, direct := err.(*json.UnmarshalTypeError); !direct || requestBodyUsesCustomDecoder(reflect.TypeOf(v), mismatch.Field) {
			return err
		}
		path := jsonErrorPath(diagnostic, nil, "", "", mismatch.Offset)
		if path == "" {
			path = "$"
		}
		return &requestBodyFieldError{message: "invalid value for field " + quotedJSONPath(path), cause: err}
	}
	// encoding/json has no exported unknown-field error type. Match its exact
	// prefix and unquote the field, rather than sending the raw error to clients.
	if field, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		if name, parseErr := strconv.Unquote(field); parseErr == nil {
			path := jsonErrorPath(diagnostic, reflect.TypeOf(v), "", name, 0)
			if path == "" {
				path = name
			}
			return &requestBodyFieldError{message: "unknown field " + quotedJSONPath(path), cause: err}
		}
	}
	return err
}

// Field is used only to detect custom decoders whose offsets are unreliable.
// Its components can include both JSON names and promoted Go names.
func requestBodyUsesCustomDecoder(typ reflect.Type, field string) bool {
	for {
		seen := make(map[reflect.Type]bool)
		for typ != nil {
			if typ.Implements(reflect.TypeFor[json.Unmarshaler]()) || reflect.PointerTo(typ).Implements(reflect.TypeFor[json.Unmarshaler]()) {
				return true
			}
			switch typ.Kind() {
			case reflect.Pointer, reflect.Map, reflect.Array, reflect.Slice:
				if seen[typ] {
					return false // A container cycle has no custom leaf decoder.
				}
				seen[typ] = true
				typ = typ.Elem()
				continue
			}
			break
		}
		if typ == nil {
			return true
		}
		if field == "" {
			return false
		}
		if typ.Kind() != reflect.Struct {
			return true
		}
		// A JSON tag may itself contain dots.
		if child := jsonFieldType(typ, field); child != nil {
			typ, field = child, ""
			continue
		}
		key, remaining, _ := strings.Cut(field, ".")
		child := jsonFieldType(typ, key)
		if child == nil {
			f, ok := typ.FieldByName(key)
			if !ok {
				return true
			}
			child = f.Type
		}
		typ, field = child, remaining
	}
}

// Bound diagnostics as well as the body. Quoting keeps control characters out
// of messages and never includes the value supplied for a field.
func quotedJSONPath(path string) string {
	const limit = 256
	if len(path) > limit {
		path = path[:limit] + "…"
	}
	return strconv.QuoteToASCII(path)
}

// Locate type failures by byte offset, never UnmarshalTypeError.Field: that
// property can include Go names for promoted fields. Unknown-field errors have
// only a leaf name, so follow the destination's JSON fields on that error path.
// Validation remains the standard decoder's job; this walk is diagnostics only.
func jsonErrorPath(dec *json.Decoder, typ reflect.Type, path, name string, offset int64) string {
	if len(path) > 256 {
		var skip json.RawMessage
		_ = dec.Decode(&skip)
		return ""
	}
	for typ != nil && typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if offset == 0 {
		if typ != nil && reflect.PointerTo(typ).Implements(reflect.TypeFor[json.Unmarshaler]()) {
			// Do not infer a custom value's fields. Consume it so later siblings
			// can still identify the standard decoder's unknown field.
			var skip json.RawMessage
			_ = dec.Decode(&skip)
			return ""
		}
		if typ == nil || typ.Kind() == reflect.Interface {
			var skip json.RawMessage
			_ = dec.Decode(&skip)
			return ""
		}
	}
	before := dec.InputOffset()
	token, err := dec.Token()
	if err != nil {
		return ""
	}
	if offset > before && offset <= dec.InputOffset() {
		if path == "" {
			return "$"
		}
		return path
	}
	switch token {
	case json.Delim('{'):
		for dec.More() {
			before := dec.InputOffset()
			token, err := dec.Token()
			if err != nil {
				return ""
			}
			key, ok := token.(string)
			if !ok {
				return ""
			}
			childPath := jsonMemberPath(path, key)
			// A typed map may reject the member name itself (e.g. a nonnumeric key).
			if offset > before && offset <= dec.InputOffset() {
				return childPath
			}
			var childType reflect.Type
			if typ != nil {
				switch typ.Kind() {
				case reflect.Struct:
					childType = jsonFieldType(typ, key)
					if key == name && childType == nil {
						return childPath
					}
				case reflect.Map:
					childType = typ.Elem()
				}
			}
			if found := jsonErrorPath(dec, childType, childPath, name, offset); found != "" {
				return found
			}
		}
		_, _ = dec.Token()
	case json.Delim('['):
		for i := 0; dec.More(); i++ {
			var childType reflect.Type
			if typ != nil && (typ.Kind() == reflect.Array || typ.Kind() == reflect.Slice) {
				childType = typ.Elem()
			}
			if found := jsonErrorPath(dec, childType, fmt.Sprintf("%s[%d]", path, i), name, offset); found != "" {
				return found
			}
		}
		_, _ = dec.Token()
	}
	return ""
}

func jsonMemberPath(parent, name string) string {
	for i, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || i > 0 && c >= '0' && c <= '9') {
			return parent + "[" + strconv.QuoteToASCII(name) + "]"
		}
	}
	if name == "" {
		return parent + `[""]`
	}
	if parent == "" {
		return name
	}
	return parent + "." + name
}

// Resolve JSON names rather than Go names: encoding/json promotes anonymous
// structs, selects the shallowest field, and favors a tagged field at equal
// depth. Conflicting fields at the same priority are not accepted by it.
func jsonFieldType(typ reflect.Type, key string) reflect.Type {
	type candidate struct {
		typ               reflect.Type
		depth, order      int
		tagged, ambiguous bool
	}
	fields := make(map[string]candidate)
	active := make(map[reflect.Type]bool)
	order := 0
	var walk func(reflect.Type, int)
	walk = func(t reflect.Type, depth int) {
		if active[t] {
			return
		}
		active[t] = true
		defer delete(active, t)
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			underlying := f.Type
			for underlying.Kind() == reflect.Pointer {
				underlying = underlying.Elem()
			}
			if f.PkgPath != "" && !(f.Anonymous && underlying.Kind() == reflect.Struct) {
				continue
			}
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			// encoding/json falls back to the Go field name for invalid JSON tags.
			if strings.IndexFunc(name, func(c rune) bool {
				return !unicode.IsLetter(c) && !unicode.IsDigit(c) && !strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", c)
			}) >= 0 {
				name = ""
			}
			if name == "" && f.Anonymous && underlying.Kind() == reflect.Struct {
				walk(underlying, depth+1)
				continue
			}
			tagged := name != ""
			if name == "" {
				name = f.Name
			}
			order++
			next := candidate{typ: f.Type, depth: depth, order: order, tagged: tagged}
			previous, exists := fields[name]
			switch {
			case !exists || depth < previous.depth || depth == previous.depth && tagged && !previous.tagged:
				fields[name] = next
			case depth == previous.depth && tagged == previous.tagged:
				previous.ambiguous = true
				fields[name] = previous
			}
		}
	}
	walk(typ, 0)
	if exact, ok := fields[key]; ok {
		if exact.ambiguous {
			return nil
		}
		return exact.typ
	}
	var folded candidate
	for name, f := range fields {
		if !f.ambiguous && strings.EqualFold(name, key) && (folded.typ == nil || f.order < folded.order) {
			folded = f
		}
	}
	return folded.typ
}
