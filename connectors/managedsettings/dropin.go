// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// dropin.go implements the read-side merge of the managed-settings.d/ DROP-IN directory
// — the last unrendered/unverified piece of the file-based managed tier. Without
// it the connector inventories and drift-checks only the base file, so a fleet that splits
// its policy across fragments is mis-reported (a constraint living in a fragment looks
// absent).
//
// VERIFIED 2026-06-16 (two independent reads of code.claude.com/docs/en/settings):
//
//	"File-based managed settings also support a drop-in directory at `managed-settings.d/`
//	 in the same system directory alongside `managed-settings.json`. ... Following the
//	 systemd convention, `managed-settings.json` is merged first as the base, then all
//	 `*.json` files in the drop-in directory are sorted alphabetically and merged on top.
//	 Later files override earlier ones for scalar values; arrays are concatenated and
//	 de-duplicated; objects are deep-merged. Hidden files starting with `.` are ignored."
//
// So the host's EFFECTIVE managed policy is deepMergeJSON(base, .d files in alphabetical
// order). This is an endpoint-managed/file-tier feature ONLY — it does not cross the
// server-managed boundary (the two managed tiers never merge; first non-empty wins).

// dropinDirName is the conventional drop-in directory name (a sibling of the base file).
const dropinDirName = "managed-settings.d"

// maxDropinFiles bounds how many fragments are merged so a hostile/runaway directory
// cannot exhaust work; a real policy split is a handful of files.
const maxDropinFiles = 1024

// dropinFragment is one parsed *.json fragment from the drop-in directory.
type dropinFragment struct {
	name  string         // base filename (for deterministic ordering + finding titles)
	value map[string]any // the fragment decoded as a generic JSON object
}

// loadDropin reads the managed-settings.d/ directory, returning the valid fragments in
// MERGE ORDER (alphabetical by filename — numeric prefixes like 10-/20- sort within it)
// and a Medium finding per malformed/unreadable/over-limit fragment. An ABSENT directory
// is not an error (the common case): it yields no fragments and no findings. Only a
// genuine read fault on a PRESENT directory is returned, so the engine retries with
// backoff rather than masking it.
func loadDropin(dir, scope string, at time.Time) (frags []dropinFragment, findings []model.FindingReport, err error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil, nil
	}
	entries, rerr := os.ReadDir(dir)
	switch {
	case errors.Is(rerr, os.ErrNotExist):
		return nil, nil, nil
	case rerr != nil:
		return nil, nil, rerr
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		// Hidden files (systemd convention), directories, and non-JSON files are ignored.
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if len(frags) >= maxDropinFiles {
			findings = append(findings, dropinFinding(scope, name, "was skipped — drop-in file limit reached", at))
			break
		}
		data, derr := readFileCapped(filepath.Join(dir, name))
		if derr != nil {
			findings = append(findings, dropinFinding(scope, name, "is unreadable — fragment not merged", at))
			continue
		}
		obj, ok := jsonObject(data)
		if !ok {
			findings = append(findings, dropinFinding(scope, name, "is not a JSON object — fragment not merged", at))
			continue
		}
		frags = append(frags, dropinFragment{name: name, value: obj})
	}
	return frags, findings, nil
}

// jsonObject decodes bytes as a JSON object. An empty body is a valid (empty) fragment; a
// non-object (array/scalar/null) or malformed body yields ok=false — a managed-settings
// fragment must be an object to deep-merge.
func jsonObject(data []byte) (map[string]any, bool) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]any{}, true
	}
	var v any
	if json.Unmarshal(data, &v) != nil {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}

// mergeEffective computes the host's EFFECTIVE managed-settings document by deep-merging
// the base object and the drop-in fragments in order (base first, then each fragment in
// alphabetical filename order). base may be nil (an absent/empty base). It returns the
// merged JSON bytes, ready for parseLive.
func mergeEffective(base map[string]any, frags []dropinFragment) ([]byte, error) {
	var merged any = base
	if base == nil {
		merged = map[string]any{}
	}
	for _, f := range frags {
		merged = deepMergeJSON(merged, f.value)
	}
	return json.Marshal(merged)
}

// deepMergeJSON merges overlay onto base with Claude Code's documented, TYPE-DEPENDENT
// semantics: two OBJECTS deep-merge key-by-key (overlay's value recursively merged onto
// base's); two ARRAYS concatenate and de-duplicate (base order first, then overlay entries
// not already present); any other combination (scalars, or a type mismatch) takes the
// OVERLAY value — the later (alphabetically higher) file wins.
func deepMergeJSON(base, overlay any) any {
	bm, bok := base.(map[string]any)
	om, ook := overlay.(map[string]any)
	if bok && ook {
		out := make(map[string]any, len(bm)+len(om))
		for k, v := range bm {
			out[k] = v
		}
		for k, v := range om {
			if existing, ok := out[k]; ok {
				out[k] = deepMergeJSON(existing, v)
			} else {
				out[k] = v
			}
		}
		return out
	}
	ba, bok2 := base.([]any)
	oa, ook2 := overlay.([]any)
	if bok2 && ook2 {
		return concatDedupJSON(ba, oa)
	}
	return overlay
}

// concatDedupJSON concatenates two JSON arrays and removes duplicates, preserving
// first-seen order (base entries first, then overlay entries not already present).
// Equality is by canonical JSON encoding, so two equal objects with different key order
// de-duplicate (Go's json.Marshal sorts object keys).
func concatDedupJSON(a, b []any) []any {
	out := make([]any, 0, len(a)+len(b))
	seen := make(map[string]struct{}, len(a)+len(b))
	for _, list := range [][]any{a, b} {
		for _, v := range list {
			key := canonicalJSON(v)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

// canonicalJSON returns a stable encoding of a JSON value for equality comparison. Go's
// json.Marshal sorts object keys, so logically-equal objects encode identically.
func canonicalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// dropinFinding reports a malformed/unreadable/skipped drop-in fragment: the host's
// effective managed policy may be incomplete (a fragment the org deployed is not in
// force). The base file still governs, so this is Medium — distinct from the HIGH
// whole-file absenceFinding.
func dropinFinding(scope, file, reason string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingKindDrift,
		Severity:    model.SeverityMedium,
		SubjectKind: originManagedPolicy,
		SubjectRef:  scope,
		Title:       "managed-settings.d/" + file + " " + reason,
		DetailHash:  redact.Hash(scope + "|dropin|" + file + "|" + reason),
		OccurredAt:  at,
	}
}
