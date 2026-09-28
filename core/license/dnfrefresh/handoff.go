// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dnfrefresh

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Schema is the DNF handoff's schema.
const Schema = "olivares.ai/dnf-credential-handoff/v1"

// MaxHandoffBytes is the helper's bound on a handoff file.
const MaxHandoffBytes = 4096

const (
	// maxSetBytes bounds a set name.
	maxSetBytes = 64
	// maxCredentialBytes is the grammar's maximum: "oad1." + 1024 + "." + 43 = 1073.
	maxCredentialBytes = 1073
)

// ErrHandoffCustody is a handoff directory or temporary name that fails custody. Nothing is written.
var ErrHandoffCustody = errors.New("dnfrefresh: the handoff directory failed custody")

var (
	codePattern        = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	credentialAlphabet = regexp.MustCompile(`^oad1\.[A-Za-z0-9_.-]+$`)
)

// Document is the handoff of one cycle, the same fields as the APT handoff, in the same order.
// Class, Set, Suites, Context, Exp and Credential are null unless Outcome is issued.
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
		return nil, fmt.Errorf("dnfrefresh: the handoff is %d bytes, above the helper's %d", len(data), MaxHandoffBytes)
	}
	return data, nil
}

func (d Document) check() error {
	bad := func(what string) error { return errors.New("dnfrefresh: the handoff document " + what) }
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

func validSuites(suites []string) bool {
	joined := strings.Join(suites, " ")
	for _, s := range classSuites {
		if joined == s {
			return true
		}
	}
	return false
}

// Publish writes d as <dir>/<cycle>.json after checking it against the schema.
func Publish(dir string, d Document) error {
	data, err := d.Marshal()
	if err != nil {
		return err
	}
	return publish(dir, d.Cycle, data)
}
