// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package filedrift compares a published managed config file with the file a
// host reports, for the tools whose managed layer is a plain JSON or TOML file
// (OpenCode, Gemini CLI, Grok Build). Every key the authored document sets must
// be set to the same value on the host; keys the host adds are the host's own
// (the tools merge a managed layer over user config), so they are not drift.
// Arrays compare in order, because most are an argv or a sequence, except at the key
// paths a Spec names as allowlists, which compare as sets.
//
// A finding names the key path, never a value: a value can be a command line or
// a URL, and the finding stream is minimal-data.
package filedrift

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/olivaresai/olivares/connectors/internal/json5"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// findingKindDrift is the kind every managed-config drift fact shares with the
// Claude Code and Codex connectors, so one console filter shows all of them.
const findingKindDrift = "policy_drift"

// MaxFindings bounds the findings of one comparison.
const MaxFindings = 100

// Spec names the tool a comparison is for: Origin is the finding's subject kind
// and Tool the label that starts every title. ExactTables lists key paths whose
// entries are an allowlist: there a host entry the authored document lacks is
// drift too, because it widens the list. SetArrays lists key paths whose array is
// an allowlist, where the same entries in another order are not drift.
type Spec struct {
	Origin      string
	Tool        string
	ExactTables []string
	SetArrays   []string
}

// JSON compares two JSON documents. The observed file may be JSONC (comments and
// trailing commas), as OpenCode and Gemini CLI read it; the authored one is
// strict. A malformed authored document is an error: it must never have been
// published. A missing or malformed observed file is a finding, never a pass.
func JSON(sp Spec, scope string, authored, observed []byte, at time.Time) ([]model.FindingReport, error) {
	want, err := strictObject(authored)
	if err != nil {
		return nil, fmt.Errorf("%s: authored document is invalid: %w", sp.Tool, err)
	}
	return compare(sp, scope, want, observed, at, func(b []byte) (map[string]any, error) {
		var m map[string]any
		return m, json5.Unmarshal(b, &m)
	}), nil
}

// TOML compares two TOML documents with the same rules as JSON.
func TOML(sp Spec, scope string, authored, observed []byte, at time.Time) ([]model.FindingReport, error) {
	var want map[string]any
	if _, err := toml.Decode(string(authored), &want); err != nil {
		return nil, fmt.Errorf("%s: authored document is invalid: %w", sp.Tool, err)
	}
	return compare(sp, scope, want, observed, at, func(b []byte) (map[string]any, error) {
		var m map[string]any
		_, err := toml.Decode(string(b), &m)
		return m, err
	}), nil
}

func strictObject(b []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("want a JSON object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("data after the JSON object")
	}
	return m, nil
}

func compare(sp Spec, scope string, want map[string]any, observed []byte, at time.Time, decode func([]byte) (map[string]any, error)) []model.FindingReport {
	finding := func(key, title string) model.FindingReport {
		return model.FindingReport{
			Kind:        findingKindDrift,
			Severity:    model.SeverityHigh,
			SubjectKind: sp.Origin,
			SubjectRef:  scope,
			Title:       sp.Tool + ": " + title,
			DetailHash:  redact.Hash(scope + "|" + key + "|" + title),
			OccurredAt:  at,
		}
	}
	if strings.TrimSpace(string(observed)) == "" {
		return []model.FindingReport{finding("", "managed config is absent on the host (the host is ungoverned)")}
	}
	got, err := decode(observed)
	if err != nil || got == nil { // JSON null decodes to nothing: not a managed file
		return []model.FindingReport{finding("", "managed config is present but invalid (the host is ungoverned)")}
	}
	var out []model.FindingReport
	for _, table := range sp.ExactTables {
		w, wok := lookup(want, strings.Split(table, "."))
		h, hok := lookup(got, strings.Split(table, "."))
		wm, wmap := w.(map[string]any)
		hm, hmap := h.(map[string]any)
		if !wok || !hok || !wmap || !hmap {
			continue // an absent or mistyped table is reported by the leaf check below
		}
		for _, k := range sortedKeys(hm) {
			if _, ok := wm[k]; !ok {
				path := table + "." + k
				out = append(out, finding(path, path+" is on the host but not in the published policy"))
			}
		}
	}
	for _, leaf := range leaves(nil, want) {
		path := strings.Join(leaf.path, ".")
		have, ok := lookup(got, leaf.path)
		_, wantEmptyTable := leaf.value.(map[string]any)
		_, haveTable := have.(map[string]any)
		switch {
		case !ok:
			out = append(out, finding(path, path+" is NOT set on the host"))
		case wantEmptyTable && haveTable:
			// A published empty table only has to exist; its entries are the
			// host's own unless the path is an ExactTables allowlist (above).
		case !same(leaf.value, have, slices.Contains(sp.SetArrays, path)):
			out = append(out, finding(path, path+" differs from the published policy"))
		}
	}
	if len(out) > MaxFindings {
		more := len(out) - MaxFindings
		out = append(out[:MaxFindings], finding("", fmt.Sprintf("%d more differences are not listed", more)))
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// leaf is one value of the published document at a key path. The path stays a list of
// keys: a key that itself contains a dot (an MCP server named "a.b") is one segment.
type leaf struct {
	path  []string
	value any
}

// leaves flattens objects into key paths in sorted order; everything else (scalars,
// arrays, empty objects) is one leaf.
func leaves(prefix []string, m map[string]any) []leaf {
	var out []leaf
	for _, k := range sortedKeys(m) {
		p := append(slices.Clone(prefix), k)
		if sub, ok := m[k].(map[string]any); ok && len(sub) > 0 {
			out = append(out, leaves(p, sub)...)
			continue
		}
		out = append(out, leaf{p, m[k]})
	}
	return out
}

// lookup walks the observed document one key at a time, exactly as the tools read it: a
// flat key that spells a nested path ("permission.bash" beside "permission") is a different
// key and does not count.
func lookup(m map[string]any, path []string) (any, bool) {
	var cur any = m
	for _, k := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = obj[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func same(a, b any, asSet bool) bool {
	as, aok := a.([]any)
	bs, bok := b.([]any)
	if aok && bok && asSet {
		return reflect.DeepEqual(sortedEncoding(as), sortedEncoding(bs))
	}
	return reflect.DeepEqual(a, b)
}

// sortedEncoding renders each element canonically (fmt prints maps in key order) and sorts
// the renderings, so two arrays with the same entries in any order are equal. It cannot fail:
// a value JSON could not encode (a TOML nan) still renders.
func sortedEncoding(items []any) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = fmt.Sprintf("%#v", it)
	}
	sort.Strings(out)
	return out
}
