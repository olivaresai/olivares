// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	cedar "github.com/cedar-policy/cedar-go"
	cedartypes "github.com/cedar-policy/cedar-go/types"
	cedarast "github.com/cedar-policy/cedar-go/x/exp/ast"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// The counted formats. Each counted surface has exactly one representation
// owner: the JSON token stream for the managed surfaces and the Cedar AST for
// the policy engines.
const (
	formatJSON  = "json"
	formatCedar = "cedar"
)

// The reasons of a ContentError.
const (
	reasonSyntax = "syntax"
	reasonUTF8   = "utf8"
)

// ContentError refuses counted content its format's representation owner
// cannot read. Format is "json" or "cedar"; Reason is "syntax" or "utf8". It is
// not retryable: the content must change first.
type ContentError struct {
	Format string
	Reason string
}

func (e ContentError) Error() string {
	return "governance: counted " + e.Format + " content cannot be read: " + e.Reason
}

// surfaceDocument returns content as the matcher reads it: the stored bytes and
// every string the surface's representation owner decodes from them. Before any
// decoding it refuses content above lim.MaxContentBytes, then content that is
// not valid UTF-8, so no string is replaced and no decoded string is longer than
// its source span. The JSON owner emits every object key and string value of
// every top-level value; the Cedar owner emits each distinct string of the
// policies once, in source order.
func surfaceDocument(surface, content string, lim auth.MatchLimits) (auth.Document, error) {
	format, ok := countedFormat(surface)
	if !ok {
		return auth.Document{}, fmt.Errorf("governance: surface %q has no counted representation: %w", surface, model.ErrUnknownKind)
	}
	if len(content) > lim.MaxContentBytes {
		return auth.Document{}, auth.CapacityError{Dimension: auth.CapacityContentBytes}
	}
	if !utf8.ValidString(content) {
		return auth.Document{}, ContentError{Format: format, Reason: reasonUTF8}
	}
	sink := decodedStrings{lim: lim, distinct: format == formatCedar}
	var err error
	switch format {
	case formatJSON:
		err = jsonDocumentStrings(content, &sink)
	case formatCedar:
		err = cedarDocumentStrings(surface, content, &sink)
	}
	if err != nil {
		return auth.Document{}, err
	}
	return auth.Document{Raw: content, Decoded: sink.out}, nil
}

// countedFormat returns the format of a counted surface, and false for a
// surface whose content is not counted.
func countedFormat(surface string) (string, bool) {
	for _, e := range surfaces.entries {
		if e.name != surface || e.content == nil || !e.content.Counted() {
			continue
		}
		switch e.family {
		case surfaceFamilyManaged:
			return formatJSON, true
		case surfaceFamilyCedar:
			return formatCedar, true
		}
	}
	return "", false
}

// decodedStrings collects the decoded strings of one document under its limits.
type decodedStrings struct {
	lim      auth.MatchLimits
	distinct bool
	seen     map[string]struct{}
	out      []string
	bytes    int
}

// emit adds one decoded string. An empty string names nothing and is skipped.
// With distinct set, a string already emitted is skipped too.
func (d *decodedStrings) emit(s string) error {
	if s == "" {
		return nil
	}
	if d.distinct {
		if d.seen == nil {
			d.seen = make(map[string]struct{})
		}
		if _, dup := d.seen[s]; dup {
			return nil
		}
		d.seen[s] = struct{}{}
	}
	if len(d.out) >= d.lim.MaxDecodedStrings {
		return auth.CapacityError{Dimension: auth.CapacityDecodedStrings}
	}
	if len(s) > d.lim.MaxDecodedBytes-d.bytes {
		return auth.CapacityError{Dimension: auth.CapacityDecodedBytes}
	}
	d.bytes += len(s)
	d.out = append(d.out, s)
	return nil
}

// jsonDocumentStrings reads content as a stream of JSON values to its end and emits
// every object key and string value. Any syntax error, including bytes after a
// value that are not JSON and a value left open at the end, refuses it.
func jsonDocumentStrings(content string, d *decodedStrings) error {
	dec := json.NewDecoder(strings.NewReader(content))
	dec.UseNumber()
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			if depth != 0 {
				return ContentError{Format: formatJSON, Reason: reasonSyntax}
			}
			return nil
		}
		if err != nil {
			return ContentError{Format: formatJSON, Reason: reasonSyntax}
		}
		switch v := tok.(type) {
		case json.Delim:
			if v == '{' || v == '[' {
				depth++
			} else {
				depth--
			}
		case string:
			if err := d.emit(v); err != nil {
				return err
			}
		}
	}
}

// cedarDocumentStrings parses content as a Cedar policy set and emits, once each, the
// annotation values, the entity types and ids of the scopes, and every string
// the condition bodies carry: attribute names, like-pattern literals, entity
// types of is tests, record keys and every string, entity type and entity id of
// a value, sets and records included. The parser reuses subtrees (a `has` chain
// repeats its left operand), so the walk visits some strings more than once;
// d emits each distinct string once.
func cedarDocumentStrings(surface, content string, d *decodedStrings) error {
	set, err := cedar.NewPolicySetFromBytes(surface, []byte(content))
	if err != nil {
		return ContentError{Format: formatCedar, Reason: reasonSyntax}
	}
	type sourced struct {
		offset int
		policy *cedar.Policy
	}
	var policies []sourced
	for _, p := range set.All() {
		policies = append(policies, sourced{offset: p.AST().Position.Offset, policy: p})
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].offset < policies[j].offset })
	for _, s := range policies {
		p := s.policy.AST()
		for _, a := range p.Annotations {
			if err := d.emit(string(a.Value)); err != nil {
				return err
			}
		}
		for _, scope := range []cedarast.IsScopeNode{p.Principal, p.Action, p.Resource} {
			if err := emitScope(scope, d); err != nil {
				return err
			}
		}
		for _, c := range p.Conditions {
			var walkErr error
			cedarast.Inspect(cedarast.NewNode(c.Body), func(n cedarast.IsNode) bool {
				if walkErr != nil {
					return false
				}
				walkErr = emitNode(n, d)
				return walkErr == nil
			})
			if walkErr != nil {
				return walkErr
			}
		}
	}
	return nil
}

func emitScope(scope cedarast.IsScopeNode, d *decodedStrings) error {
	switch s := scope.(type) {
	case cedarast.ScopeTypeEq:
		return emitEntity(s.Entity, d)
	case cedarast.ScopeTypeIn:
		return emitEntity(s.Entity, d)
	case cedarast.ScopeTypeInSet:
		for _, e := range s.Entities {
			if err := emitEntity(e, d); err != nil {
				return err
			}
		}
	case cedarast.ScopeTypeIs:
		return d.emit(string(s.Type))
	case cedarast.ScopeTypeIsIn:
		if err := d.emit(string(s.Type)); err != nil {
			return err
		}
		return emitEntity(s.Entity, d)
	}
	return nil
}

func emitNode(n cedarast.IsNode, d *decodedStrings) error {
	switch v := n.(type) {
	case cedarast.NodeTypeAccess:
		return d.emit(string(v.Value))
	case cedarast.NodeTypeHas:
		return d.emit(string(v.Value))
	case cedarast.NodeTypeLike:
		return emitPattern(v.Value, d)
	case cedarast.NodeTypeIs:
		return d.emit(string(v.EntityType))
	case cedarast.NodeTypeIsIn:
		return d.emit(string(v.EntityType))
	case cedarast.NodeTypeRecord:
		for _, e := range v.Elements {
			if err := d.emit(string(e.Key)); err != nil {
				return err
			}
		}
	case cedarast.NodeValue:
		return emitValue(v.Value, d)
	}
	return nil
}

func emitValue(v cedartypes.Value, d *decodedStrings) error {
	switch x := v.(type) {
	case cedartypes.String:
		return d.emit(string(x))
	case cedartypes.EntityUID:
		return emitEntity(x, d)
	case cedartypes.Set:
		for item := range x.All() {
			if err := emitValue(item, d); err != nil {
				return err
			}
		}
	case cedartypes.Record:
		var keys []string
		for k := range x.Keys() {
			keys = append(keys, string(k))
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := d.emit(k); err != nil {
				return err
			}
			if item, ok := x.Get(cedartypes.String(k)); ok {
				if err := emitValue(item, d); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func emitEntity(e cedartypes.EntityUID, d *decodedStrings) error {
	if err := d.emit(string(e.Type)); err != nil {
		return err
	}
	return d.emit(string(e.ID))
}

// emitPattern emits the literal chunks of a like pattern, read from its JSON
// form: the pattern keeps its chunks private.
func emitPattern(p cedartypes.Pattern, d *decodedStrings) error {
	raw, err := p.MarshalJSON()
	if err != nil {
		return ContentError{Format: formatCedar, Reason: reasonSyntax}
	}
	literals, err := patternLiterals(raw)
	if err != nil {
		return err
	}
	for _, literal := range literals {
		if err := d.emit(literal); err != nil {
			return err
		}
	}
	return nil
}

// patternLiterals returns the literal chunks of a like pattern's JSON form: an
// array whose parts are each the marker "Wildcard" or an object with exactly
// one key, "Literal", and a string value. It reads the form token by token, so
// a repeated key is seen rather than merged. Any other form, including a
// repeated or extra key and anything after the array, refuses as a Cedar syntax
// error, so a change in that form can never drop a literal silently.
func patternLiterals(raw []byte) ([]string, error) {
	refuse := ContentError{Format: formatCedar, Reason: reasonSyntax}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
		return nil, refuse
	}
	var out []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, refuse
		}
		switch v := tok.(type) {
		case string:
			if v != "Wildcard" {
				return nil, refuse
			}
		case json.Delim:
			literal, ok := literalPart(dec, v)
			if !ok {
				return nil, refuse
			}
			out = append(out, literal)
		default:
			return nil, refuse
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim(']') {
		return nil, refuse
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, refuse
	}
	return out, nil
}

// literalPart reads the rest of one {"Literal": <string>} part after its
// opening delimiter: the one key, its string value and the closing brace.
func literalPart(dec *json.Decoder, open json.Delim) (string, bool) {
	if open != '{' {
		return "", false
	}
	if key, err := dec.Token(); err != nil || key != "Literal" {
		return "", false
	}
	value, err := dec.Token()
	literal, isString := value.(string)
	if err != nil || !isString {
		return "", false
	}
	end, err := dec.Token()
	return literal, err == nil && end == json.Delim('}')
}
