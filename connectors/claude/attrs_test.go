// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

func TestNewAttrsMergePrecedence(t *testing.T) {
	resource := []*commonpb.KeyValue{kvStr("k", "resource"), kvStr("only_res", "r")}
	record := []*commonpb.KeyValue{kvStr("k", "record")}
	a := newAttrs(resource, record)
	if got := a.str("k"); got != "record" {
		t.Errorf("record attribute must win: got %q", got)
	}
	if got := a.str("only_res"); got != "r" {
		t.Errorf("resource attribute lost: got %q", got)
	}
	if got := a.str("absent"); got != "" {
		t.Errorf("absent key must be empty: got %q", got)
	}
}

func TestAttrsTypedGetters(t *testing.T) {
	a := newAttrs([]*commonpb.KeyValue{
		kvInt("n", 42),
		kvStr("ns", "7"),
		kvDouble("f", 1.5),
		kvBool("b", true),
		kvStr("bs", "true"),
	})
	if n, ok := a.intVal("n"); !ok || n != 42 {
		t.Errorf("intVal int = %d,%v", n, ok)
	}
	if n, ok := a.intVal("ns"); !ok || n != 7 {
		t.Errorf("intVal string-coerced = %d,%v", n, ok)
	}
	if _, ok := a.intVal("absent"); ok {
		t.Error("intVal absent should be false")
	}
	if f, ok := a.floatVal("f"); !ok || f != 1.5 {
		t.Errorf("floatVal = %v,%v", f, ok)
	}
	if f, ok := a.floatVal("n"); !ok || f != 42 {
		t.Errorf("floatVal int-coerced = %v,%v", f, ok)
	}
	if b, ok := a.boolVal("b"); !ok || !b {
		t.Errorf("boolVal = %v,%v", b, ok)
	}
	if b, ok := a.boolVal("bs"); !ok || !b {
		t.Errorf("boolVal string-coerced = %v,%v", b, ok)
	}
}

func TestAttrsObjectFromKvlist(t *testing.T) {
	a := newAttrs([]*commonpb.KeyValue{
		kvObj("tool_input", kvStr("file_path", "/etc/hosts"), kvInt("limit", 10)),
	})
	obj := a.objectVal("tool_input")
	if obj == nil {
		t.Fatal("objectVal returned nil for a kvlist")
	}
	if obj["file_path"] != "/etc/hosts" {
		t.Errorf("file_path = %v", obj["file_path"])
	}
}

func TestAttrsObjectFromJSONString(t *testing.T) {
	a := newAttrs([]*commonpb.KeyValue{
		kvStr("tool_input", `{"url":"https://x.test/a"}`),
	})
	obj := a.objectVal("tool_input")
	if obj == nil || obj["url"] != "https://x.test/a" {
		t.Errorf("objectVal from JSON string = %v", obj)
	}
}

func TestAttrsObjectAbsent(t *testing.T) {
	a := newAttrs(nil)
	if a.objectVal("nope") != nil {
		t.Error("objectVal of absent key should be nil")
	}
}
