// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"encoding/json"
	"strconv"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// attrs is a flat, typed view over an OTLP attribute set. Claude Code carries an
// event's fields as LogRecord attributes (plus resource-level attributes shared
// by every record); attrs merges them and exposes typed getters so the mapper
// never type-switches AnyValue by hand.
type attrs map[string]*commonpb.AnyValue

// newAttrs builds an attribute view from one or more OTLP KeyValue lists. Later
// lists win on key conflicts, so callers pass resource attributes first and the
// more specific record attributes last.
func newAttrs(lists ...[]*commonpb.KeyValue) attrs {
	a := attrs{}
	for _, list := range lists {
		for _, kv := range list {
			if kv == nil || kv.GetKey() == "" {
				continue
			}
			a[kv.GetKey()] = kv.GetValue()
		}
	}
	return a
}

// str returns the attribute as a string, or "" if absent. A non-string scalar is
// rendered (an int/bool/double becomes its textual form) so a value mis-typed by
// a producer is still usable.
func (a attrs) str(key string) string {
	return anyToString(a[key])
}

// has reports whether the attribute key is present at all (regardless of its
// value type). Dialect detection keys on PRESENCE: gen_ai.input.messages is a
// marker of the v1.37+ generation even when its (content) value is not read.
func (a attrs) has(key string) bool {
	_, ok := a[key]
	return ok
}

// intVal returns the attribute as an int64 and whether it was present and
// numeric. A string that parses as an integer is accepted (producers sometimes
// stringify counts).
func (a attrs) intVal(key string) (int64, bool) {
	v := a[key]
	if v == nil {
		return 0, false
	}
	switch v.GetValue().(type) {
	case *commonpb.AnyValue_IntValue:
		return v.GetIntValue(), true
	case *commonpb.AnyValue_DoubleValue:
		return int64(v.GetDoubleValue()), true
	case *commonpb.AnyValue_StringValue:
		if n, err := strconv.ParseInt(v.GetStringValue(), 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

// floatVal returns the attribute as a float64 and whether it was present and
// numeric (accepting an int or a numeric string).
func (a attrs) floatVal(key string) (float64, bool) {
	v := a[key]
	if v == nil {
		return 0, false
	}
	switch v.GetValue().(type) {
	case *commonpb.AnyValue_DoubleValue:
		return v.GetDoubleValue(), true
	case *commonpb.AnyValue_IntValue:
		return float64(v.GetIntValue()), true
	case *commonpb.AnyValue_StringValue:
		if f, err := strconv.ParseFloat(v.GetStringValue(), 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// boolVal returns the attribute as a bool and whether it was present and boolean
// (accepting "true"/"false" strings).
func (a attrs) boolVal(key string) (bool, bool) {
	v := a[key]
	if v == nil {
		return false, false
	}
	switch v.GetValue().(type) {
	case *commonpb.AnyValue_BoolValue:
		return v.GetBoolValue(), true
	case *commonpb.AnyValue_StringValue:
		if b, err := strconv.ParseBool(v.GetStringValue()); err == nil {
			return b, true
		}
	}
	return false, false
}

// objectVal returns the attribute decoded as a key/value object. Claude Code may
// carry a structured tool input either as a native OTLP kvlist or as a JSON
// string (when OTEL_LOG_TOOL_DETAILS=1); both are handled, returning nil if the
// attribute is absent or not object-shaped.
func (a attrs) objectVal(key string) map[string]any {
	v := a[key]
	if v == nil {
		return nil
	}
	switch v.GetValue().(type) {
	case *commonpb.AnyValue_KvlistValue:
		out := map[string]any{}
		for _, kv := range v.GetKvlistValue().GetValues() {
			out[kv.GetKey()] = anyToGo(kv.GetValue())
		}
		return out
	case *commonpb.AnyValue_StringValue:
		var out map[string]any
		if err := json.Unmarshal([]byte(v.GetStringValue()), &out); err == nil {
			return out
		}
	}
	return nil
}

// anyToString renders an OTLP AnyValue as a string. A nil value or an empty
// oneof yields "".
func anyToString(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return v.GetStringValue()
	case *commonpb.AnyValue_BoolValue:
		return strconv.FormatBool(v.GetBoolValue())
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(v.GetIntValue(), 10)
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(v.GetDoubleValue(), 'f', -1, 64)
	case *commonpb.AnyValue_BytesValue:
		return string(v.GetBytesValue())
	default:
		return ""
	}
}

// anyToGo converts an OTLP AnyValue to a plain Go value for an object map. It is
// best-effort: scalars map directly, an array maps to []any, a kvlist to
// map[string]any.
func anyToGo(v *commonpb.AnyValue) any {
	if v == nil {
		return nil
	}
	switch v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return v.GetStringValue()
	case *commonpb.AnyValue_BoolValue:
		return v.GetBoolValue()
	case *commonpb.AnyValue_IntValue:
		return v.GetIntValue()
	case *commonpb.AnyValue_DoubleValue:
		return v.GetDoubleValue()
	case *commonpb.AnyValue_ArrayValue:
		arr := v.GetArrayValue().GetValues()
		out := make([]any, 0, len(arr))
		for _, e := range arr {
			out = append(out, anyToGo(e))
		}
		return out
	case *commonpb.AnyValue_KvlistValue:
		out := map[string]any{}
		for _, kv := range v.GetKvlistValue().GetValues() {
			out[kv.GetKey()] = anyToGo(kv.GetValue())
		}
		return out
	default:
		return nil
	}
}
