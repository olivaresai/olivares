// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package helperschema

import "strings"

// The certificate helper's closed set. begin issues a nonce under which the Appliance Console
// spools a certificate; install installs the spooled certificate with a key the document names by
// reference; generate creates a new self-signed pair naming the portal's current origin and
// addresses, which the helper reads itself.
const (
	CertBegin    = "begin"
	CertInstall  = "install"
	CertGenerate = "generate"
)

// CertRequest is olivares-portal-cert's document: {"op": "begin" | "generate", "operation_id"} or
// {"op": "install", "nonce", "key_ref", "operation_id"}. Every subcommand changes the host and
// carries its operation id. No field names a file, a host name, an address or a key: install
// derives its file from a nonce the helper issued and resolves its key from a reference, and
// generate names what the host states when it runs.
type CertRequest struct {
	Op          string `json:"op"`
	Nonce       string `json:"nonce,omitempty"`
	KeyRef      string `json:"key_ref,omitempty"`
	OperationID string `json:"operation_id"`
}

// Subcommand implements Request.
func (r CertRequest) Subcommand() string { return r.Op }

// Operation implements Request.
func (r CertRequest) Operation() string { return r.OperationID }

// Validate implements Request. A nonce is a nonce's shape and a key reference a reference's, or the
// document is refused; neither refusal repeats the value.
func (r CertRequest) Validate() error {
	switch r.Op {
	case CertBegin, CertGenerate:
		if r.Nonce != "" {
			return refuse("$.nonce", "only install takes a nonce, one the helper issued")
		}
		if r.KeyRef != "" {
			return refuse("$.key_ref", "only install takes a key reference")
		}
		return requireOperationID(r.OperationID)
	case CertInstall:
		if !nonceShape(r.Nonce) {
			return refuse("$.nonce", "expected a nonce this helper issued: 64 lowercase hexadecimal digits")
		}
		if !KeyRefShape(r.KeyRef) {
			return refuse("$.key_ref", "expected a key reference, store:<name>; a key itself is never sent")
		}
		return requireOperationID(r.OperationID)
	case "":
		return refuse("$.op", "required: begin, install or generate")
	}
	return refuse("$.op", "expected begin, install or generate")
}

// keyRefPrefix is the one form a key reference takes: the helper's protected key store.
const keyRefPrefix = "store:"

// KeyRefShape reports whether s is a key reference: "store:" and a name of 1 to 63 lowercase
// letters, digits and inner hyphens. A key, a path or any other text is not one.
func KeyRefShape(s string) bool {
	name, ok := strings.CutPrefix(s, keyRefPrefix)
	if !ok || len(name) < 1 || len(name) > 63 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}

// KeyRefName returns the store name a key reference names, and false for anything that is not a
// key reference.
func KeyRefName(ref string) (string, bool) {
	if !KeyRefShape(ref) {
		return "", false
	}
	return strings.TrimPrefix(ref, keyRefPrefix), true
}

// CertRules is olivares-portal-cert's admission: generate for the repair console on tty1 alone.
// begin and install admit no invoker until the Appliance Console's certificate audience is adopted
// with its own row; the socket of a root helper is root's alone, so that row names the socket the
// Appliance Console reaches.
func CertRules() []Rule {
	return []Rule{
		{Subcommand: CertBegin, Mutating: true},
		{Subcommand: CertInstall, Mutating: true},
		{Subcommand: CertGenerate, Mutating: true, Invokers: []Invoker{RepairConsole}},
	}
}
