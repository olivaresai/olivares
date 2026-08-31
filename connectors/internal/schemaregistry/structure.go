// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package schemaregistry

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Structure is the minimal-data shape extracted from a SCHEMA (not a payload): the
// declared record/message name and its top-level field NAMES. A field name is the
// data contract's metadata — "this contract has a field customer_id" — never a
// field VALUE. A connector turns this into topology edges (topic → contract,
// contract → field) without ever decoding a message body (docs/SECURITY-HARDENING.md).
type Structure struct {
	// Name is the top-level record/message/title name (e.g. "OrderCreated").
	Name string
	// Namespace is the schema namespace/package when declared (Avro/Protobuf).
	Namespace string
	// Fields are the top-level field/property names declared by the schema.
	Fields []string
}

// FullName joins namespace and name the way a contract reference reads
// ("com.acme.OrderCreated"); just the name when there is no namespace.
func (s Structure) FullName() string {
	if s.Namespace != "" && s.Name != "" {
		return s.Namespace + "." + s.Name
	}
	return s.Name
}

// StructuralRefs parses a resolved schema's definition for its structure. It is
// tolerant: an unparseable or unfamiliar schema yields a zero Structure rather than
// an error (the connector still emits the topic/group topology edge it knows). It
// reads only names — never sample data — so it cannot leak content.
func StructuralRefs(s Schema) Structure {
	switch strings.ToUpper(s.Type) {
	case "PROTOBUF":
		return protobufStructure(s.Definition)
	case "JSON":
		return jsonSchemaStructure(s.Definition)
	default: // AVRO (registry default)
		return avroStructure(s.Definition)
	}
}

// avroStructure pulls the record name/namespace and top-level field names from an
// Avro schema JSON. A union (top-level array) is scanned for its first record.
func avroStructure(def string) Structure {
	def = strings.TrimSpace(def)
	if def == "" {
		return Structure{}
	}
	raw := json.RawMessage(def)
	// A union schema is a JSON array; find the first object member.
	if len(def) > 0 && def[0] == '[' {
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) == nil {
			for _, m := range arr {
				t := strings.TrimSpace(string(m))
				if len(t) > 0 && t[0] == '{' {
					raw = m
					break
				}
			}
		}
	}
	var node struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Fields    []struct {
			Name string `json:"name"`
		} `json:"fields"`
	}
	if json.Unmarshal(raw, &node) != nil {
		return Structure{}
	}
	out := Structure{Name: node.Name, Namespace: node.Namespace}
	for _, f := range node.Fields {
		if f.Name != "" {
			out.Fields = append(out.Fields, f.Name)
		}
	}
	return out
}

// jsonSchemaStructure pulls the title and top-level property names from a JSON
// Schema document.
func jsonSchemaStructure(def string) Structure {
	var node struct {
		Title      string                     `json:"title"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal([]byte(def), &node) != nil {
		return Structure{}
	}
	out := Structure{Name: node.Title}
	for k := range node.Properties {
		out.Fields = append(out.Fields, k)
	}
	return out
}

// protobufMessageRe matches the first top-level `message Name {` in a .proto text.
var protobufMessageRe = regexp.MustCompile(`(?m)^\s*message\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{`)

// protobufPackageRe matches an optional `package a.b.c;` declaration.
var protobufPackageRe = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z_][A-Za-z0-9_.]*)\s*;`)

// protobufFieldRe matches a scalar/message field line `[label] type name = n;`,
// capturing the field NAME (submatch 1). It is intentionally loose: it extracts
// names for topology, not a full proto3 parser.
var protobufFieldRe = regexp.MustCompile(`(?m)^\s*(?:repeated\s+|optional\s+|required\s+)?[A-Za-z_][A-Za-z0-9_.<>, ]*\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*\d+\s*;`)

// protobufStructure pulls the package and the FIRST message's name and field names
// from a .proto definition with a lightweight scan (not a full compiler — it reads
// names for topology only).
func protobufStructure(def string) Structure {
	out := Structure{}
	if m := protobufPackageRe.FindStringSubmatch(def); m != nil {
		out.Namespace = m[1]
	}
	loc := protobufMessageRe.FindStringSubmatchIndex(def)
	if loc == nil {
		return out
	}
	out.Name = def[loc[2]:loc[3]]
	// Scan the body of this first message for field names, bounded by its braces.
	body := messageBody(def[loc[1]-1:]) // start at the opening brace
	for _, fm := range protobufFieldRe.FindAllStringSubmatch(body, -1) {
		out.Fields = append(out.Fields, fm[1])
	}
	return out
}

// messageBody returns the text inside the first balanced {...} of s (starting at the
// opening brace). It bounds the field scan to one message so a second message's
// fields are not mixed in.
func messageBody(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start+1 : i]
			}
		}
	}
	return s[start+1:]
}
