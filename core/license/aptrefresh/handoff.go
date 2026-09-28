// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package aptrefresh

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Schema is the handoff's schema (Interface Q3 r2 §3.10 I5, r3 §3.10 I5).
const Schema = "olivares.ai/apt-credential-handoff/v2"

// MaxHandoffBytes is the helper's bound on a handoff file.
const MaxHandoffBytes = 4096

const (
	// maxSetBytes bounds a set name.
	maxSetBytes = 64
	// maxCredentialBytes is the resource bound of a credential (Interface Q3 r2 §3.19); the grammar
	// itself allows at most 1073 bytes.
	maxCredentialBytes = 1100
)

// ErrHandoffCustody is a handoff directory or temporary name that fails custody. Nothing is written.
var ErrHandoffCustody = errors.New("aptrefresh: the handoff directory failed custody")

var (
	// codePattern is a closed code: a connect-v1 refusal code or unknown.
	codePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	// credentialAlphabet is a download credential's prefix and alphabet; CheckAnswer holds the grammar.
	credentialAlphabet = regexp.MustCompile(`^oad1\.[A-Za-z0-9_.-]+$`)
)

// Document is the handoff of one cycle, exactly the Interface's fields in its order. Class, Set, Suites,
// Context, Exp and Credential are null unless Outcome is issued; Invocation is null unless it is valid.
type Document struct {
	Schema     string   `json:"schema"`
	Cycle      string   `json:"cycle"`
	Invocation *string  `json:"invocation"`
	Outcome    Outcome  `json:"outcome"`
	Code       *string  `json:"code"`
	Class      *string  `json:"class"`
	Set        *string  `json:"set"`
	Suites     []string `json:"suites"`
	Context    *string  `json:"context"`
	Exp        *int64   `json:"exp"`
	Credential *string  `json:"credential"` // never printed
	At         string   `json:"at"`
}

// Issued is the handoff of an answer that passed CheckAnswer.
func Issued(cycle string, invocation *string, a Answer, at time.Time) Document {
	class, set, ctx, exp, cred := a.Class, a.Set, a.Context, a.Exp, a.Credential
	return Document{Schema: Schema, Cycle: cycle, Invocation: invocation, Outcome: OutcomeIssued, Class: &class, Set: &set,
		Suites: slices.Clone(a.Suites), Context: &ctx, Exp: &exp, Credential: &cred, At: stamp(at)}
}

// NotIssued is the handoff of any other outcome; code is "" when the outcome has none.
func NotIssued(cycle string, invocation *string, o Outcome, code string, at time.Time) Document {
	d := Document{Schema: Schema, Cycle: cycle, Invocation: invocation, Outcome: o, At: stamp(at)}
	if code != "" {
		d.Code = &code
	}
	return d
}

func stamp(t time.Time) string { return t.UTC().Truncate(time.Second).Format(time.RFC3339) }

// Marshal returns the document's exact bytes, or an error when the document is outside the schema.
func (d Document) Marshal() ([]byte, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if len(data) > MaxHandoffBytes {
		return nil, fmt.Errorf("aptrefresh: the handoff is %d bytes, above the helper's %d", len(data), MaxHandoffBytes)
	}
	return data, nil
}

func (d Document) check() error {
	bad := func(what string) error { return errors.New("aptrefresh: the handoff document " + what) }
	switch {
	case d.Schema != Schema:
		return bad("has another schema")
	case CheckCycle(d.Cycle) != nil:
		return bad("names an invalid cycle")
	case d.Invocation != nil && !id128Pattern.MatchString(*d.Invocation):
		return bad("names an invalid invocation")
	case !outcomes[d.Outcome]:
		return bad("names an unknown outcome")
	case d.Code != nil && !codePattern.MatchString(*d.Code):
		return bad("names an invalid code")
	}
	if _, err := time.Parse(time.RFC3339, d.At); err != nil {
		return bad("has no RFC 3339 time")
	}
	present := []bool{d.Class != nil, d.Set != nil, d.Suites != nil, d.Context != nil, d.Exp != nil, d.Credential != nil}
	if d.Outcome != OutcomeIssued {
		if slices.Contains(present, true) {
			return bad("carries an issued field but is not issued")
		}
		return nil
	}
	switch {
	case slices.Contains(present, false):
		return bad("is issued without every issued field")
	case d.Code != nil:
		return bad("is issued with a code")
	case classSuites[*d.Class] == "":
		return bad("names an unknown class")
	case len(*d.Set) > maxSetBytes || !setPattern.MatchString(*d.Set):
		return bad("names an invalid set")
	case !validSuites(d.Suites):
		return bad("names invalid suites")
	case !id128Pattern.MatchString(*d.Context):
		return bad("names an invalid context")
	case *d.Exp < 1:
		return bad("names an invalid exp")
	case len(*d.Credential) > maxCredentialBytes || !credentialAlphabet.MatchString(*d.Credential):
		return bad("carries a value that is not a download credential")
	}
	return nil
}

// validSuites reports the suites of some class, in their sorted order.
func validSuites(suites []string) bool {
	joined := strings.Join(suites, " ")
	for _, s := range classSuites {
		if joined == s {
			return true
		}
	}
	return false
}

// Publish writes d as <dir>/<cycle>.json after checking it against the schema. The product passes
// HandoffDir; see publish for the custody rules.
func Publish(dir string, d Document) error {
	data, err := d.Marshal()
	if err != nil {
		return err
	}
	return publish(dir, d.Cycle, data)
}
