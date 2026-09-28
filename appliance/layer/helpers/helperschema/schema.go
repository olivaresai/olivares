// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package helperschema is the pure half of the appliance's privileged-helper seam: the closed
// documents each helper reads on its standard input, their refusals, the admission rule each
// helper applies to the invoker the kernel attests, and the spool nonces a helper issues.
//
// The rule it enforces, in one line: standard input carries documents, arguments carry
// nothing, and the only file a helper opens is one whose name it derived itself. No document
// has a field that is a path, a unit, a mode or an invoker: a field the schema does not name is
// refused, never ignored, so a caller cannot declare who it is or what it may do. The package
// performs no I/O beyond reading the document it is handed.
package helperschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
)

// MaxDocument bounds a request document: a helper reads at most this many bytes.
const MaxDocument = 4096

// maxDepth bounds a document's nesting.
const maxDepth = 8

// The root helpers of the seam, by the name of their socket, /run/olivares-helpers/<name>.sock.
const (
	HelperPower         = "power"
	HelperSupportBundle = "support-bundle"
	// HelperCert is the certificate helper, olivares-portal-cert. It is a root helper because its
	// one admitted invoker is the repair console on tty1.
	HelperCert = "cert"
)

// Helpers returns the root helpers (ClassRoot), in a fixed order. The module helpers are
// ModuleHelpers; ClassOf knows every helper of the seam. HelperFirewallLocal is firewalllocal.go's.
func Helpers() []string {
	return []string{HelperPower, HelperSupportBundle, HelperCert, HelperFirewallLocal}
}

// SocketDir is the directory of the helpers' sockets. Each socket unit listens on
// SocketDir/<name>.sock with Accept=yes, so each connection starts one helper instance.
const SocketDir = "/run/olivares-helpers"

// InputError refuses a document. It names the field and the rule, never a value the caller
// sent, so a refusal can be logged and shown.
type InputError struct {
	Field  string
	Reason string
}

func (e *InputError) Error() string { return "input refused at " + e.Field + ": " + e.Reason }

func refuse(field, reason string) error { return &InputError{Field: field, Reason: reason} }

// ErrRead reports a document that could not be read at all: an input failure, not a refusal.
var ErrRead = errors.New("the request document could not be read")

// Request is one helper's closed document.
type Request interface {
	// Subcommand is the act the document asks for, from the helper's closed set.
	Subcommand() string
	// Operation is the operation id a mutating document carries, "" for one that changes
	// nothing.
	Operation() string
	// Validate refuses a value outside the closed set of its field.
	Validate() error
}

// Decode reads one request document from r into v and validates it. The document is one JSON
// object of at most MaxDocument bytes, with no null, no repeated key, no field the schema does
// not name and nothing after it. Each key must be byte-equal to a json tag of v's type, and is
// checked before encoding/json sees the document, because encoding/json matches a key to a
// field ignoring case and lets a later duplicate replace an earlier one: a case variant is
// refused, never folded. A read failure is ErrRead; every other failure is an *InputError.
func Decode(r io.Reader, v Request) error {
	data, err := io.ReadAll(io.LimitReader(r, MaxDocument+1))
	if err != nil {
		return ErrRead
	}
	if len(data) > MaxDocument {
		return refuse("$", "the document exceeds 4096 bytes")
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return refuse("$", "expected one JSON object")
	}
	strict := json.NewDecoder(bytes.NewReader(data))
	if err := strictValue(strict, "$", 0, fieldNames(v)); err != nil {
		return err
	}
	if _, err := strict.Token(); !errors.Is(err, io.EOF) {
		return refuse("$", "a second document follows the first")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return refuse("$", "a field the schema does not name, or a value of the wrong type")
	}
	return v.Validate()
}

// fieldNames returns the keys v's schema names: the json tag of each exported field of the
// struct v points to, byte for byte. A type with no tagged field names none.
func fieldNames(v Request) map[string]bool {
	names := map[string]bool{}
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return names
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if f.IsExported() && name != "" && name != "-" {
			names[name] = true
		}
	}
	return names
}

// strictValue reads one JSON value and refuses a null or a repeated object key at any depth,
// which encoding/json would otherwise accept silently. When names is not nil, the value is the
// document's own object, and each of its keys must be one of names, byte for byte.
func strictValue(d *json.Decoder, field string, depth int, names map[string]bool) error {
	if depth > maxDepth {
		return refuse(field, "the document nests too deeply")
	}
	token, err := d.Token()
	if err != nil {
		return refuse(field, "invalid or incomplete JSON")
	}
	if token == nil {
		return refuse(field, "null is not a value of this schema")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			keyToken, err := d.Token()
			if err != nil {
				return refuse(field, "invalid or incomplete JSON")
			}
			key, ok := keyToken.(string)
			if !ok {
				return refuse(field, "invalid object key")
			}
			if names != nil && !names[key] {
				return refuse(field, "a key that is not a field of this schema, byte for byte")
			}
			if seen[key] {
				return refuse(field, "a key is repeated")
			}
			seen[key] = true
			if err := strictValue(d, field+".<field>", depth+1, nil); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := strictValue(d, field+"[]", depth+1, nil); err != nil {
				return err
			}
		}
	default:
		return refuse(field, "invalid or incomplete JSON")
	}
	if _, err := d.Token(); err != nil {
		return refuse(field, "invalid or incomplete JSON")
	}
	return nil
}

// Usage is what a helper prints for --help: its exit contract and that it takes no path.
const Usage = "This helper reads one JSON document of at most 4096 bytes on its standard input, which is the " +
	"connection its socket unit passes, and answers one JSON document on its standard output. It takes no " +
	"argument and no path: a document field it does not name is refused. It admits the invoker the kernel " +
	"attests for the connection, never one the document names. Exit 0: performed or answered; 1: refused, " +
	"with no effect; 2: a usage, setup or input/output failure, with no effect.\n"

// ArgsAction is what a helper does with its command line.
type ArgsAction int

const (
	// ArgsServe serves the connection: there is no argument.
	ArgsServe ArgsAction = iota
	// ArgsHelp prints Usage and exits 0: the one argument is --help.
	ArgsHelp
)

// CheckArgs refuses every command line except none and --help. A helper takes no path, no
// mode and no invoker as an argument: its request is the document on its standard input.
func CheckArgs(args []string) (ArgsAction, error) {
	switch {
	case len(args) == 0:
		return ArgsServe, nil
	case len(args) == 1 && args[0] == "--help":
		return ArgsHelp, nil
	}
	return ArgsServe, errors.New("a helper takes no argument; its request is the document on its standard input")
}

// Results of a helper's answer.
const (
	// ResultPerformed: the effect was performed. For power it means the service manager
	// accepted the request, not that the host has finished going down.
	ResultPerformed = "performed"
	// ResultAnswered: a read-only subcommand answered; nothing changed.
	ResultAnswered = "answered"
	// ResultRefused: nothing was performed, and Code says why.
	ResultRefused = "refused"
	// ResultFailed: the effect's owner did not accept the request; nothing was performed.
	ResultFailed = "failed"
)

// Refusal codes. Each names the owner that refused and never a value the caller sent.
const (
	CodeInputRefused         = "input_refused"
	CodeNoConnectionIdentity = "no_connection_identity"
	CodeNotAdmitted          = "not_admitted"
	CodeVerbUnavailable      = "verb_unavailable"
	// CodePidfdUnproven refuses a mutating subcommand of a per-connection helper instance
	// until the appliance's own kernel is proven to give it the peer's pidfd.
	CodePidfdUnproven = "pidfd_unproven"
	CodeEffectFailed  = "effect_failed"
	// CodeConsumerUnavailable is the client's: the helper is absent, so nothing was asked.
	CodeConsumerUnavailable = "consumer_unavailable"
)

// Response is a helper's one answer document.
type Response struct {
	Result string `json:"result"`
	Code   string `json:"code,omitempty"`
	// Detail is fixed text chosen by the helper; it never repeats a value of the request.
	Detail string `json:"detail,omitempty"`
	// Nonce is a spool nonce the helper issued.
	Nonce string `json:"nonce,omitempty"`
	// Bundle is a support bundle the helper produced.
	Bundle json.RawMessage `json:"bundle,omitempty"`
}
