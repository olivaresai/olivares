// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package policy is the host firewall's closed policy document and its one renderer: the
// nftables table inet olivares, which the firewall owner alone loads.
//
// Every row comes from a closed source: the operator's SSH port, the product's ports
// (base.ProductPorts, never the document), the console's 9443 row on the management
// interfaces when the console is enabled for them, link-local DHCPv6 server replies on the
// DHCPv6 client interfaces, and one row per optional application port, each added by that
// application's own firewall act. Ports, protocols, ICMPv6 types and interface names come from
// closed sets; no document field is a path, a rule or a shell string. The input and forward
// chains drop what no row admits, accept established and related traffic, drop invalid
// traffic, admit loopback and keep IPv6 neighbor and router discovery.
package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/olivaresai/olivares/appliance/layer/base"
)

// SchemaVersion is the policy document's schema.
const SchemaVersion = "olivares-firewall-policy/v1"

// The console's listen scopes, as the published selection names them.
const (
	ListenLocal      = "local"
	ListenManagement = "management"
)

// Bounds of the document.
const (
	// MaxDocument bounds an encoded document.
	MaxDocument = 16384
	// MaxInterfaces bounds each interface list, as the answers bound host.management_interfaces.
	MaxInterfaces = 16
	// MaxAppRows bounds the optional applications' rows.
	MaxAppRows = 32
	// EveryInterface is an application row's interface list that admits its port everywhere.
	EveryInterface = "*"
	// ConsolePort is the console's row.
	ConsolePort = "9443/tcp"
	// DHCPv6ClientPort is the port DHCPv6 server replies reach a client on.
	DHCPv6ClientPort = "546/udp"
	// TableComment prefixes the policy digest the loaded table carries, so a measurement of the
	// kernel's table names the document it was rendered from.
	TableComment = "olivares-firewall-policy "
)

// Portal is the console's selection the 9443 row follows: portal.enabled, portal.listen and
// host.management_interfaces.
type Portal struct {
	Enabled              bool     `json:"enabled"`
	Listen               string   `json:"listen"`
	ManagementInterfaces []string `json:"management_interfaces"`
}

// AppRow is one optional application's port row: the application's slug, one port and the
// interfaces it answers on ("*" alone for every interface).
type AppRow struct {
	App        string   `json:"app"`
	Port       string   `json:"port"`
	Interfaces []string `json:"interfaces"`
}

// Document is the closed policy document. Every field is required; lists may be empty.
type Document struct {
	SchemaVersion          string   `json:"schema_version"`
	SSHPort                int      `json:"ssh_port"`
	Portal                 Portal   `json:"portal"`
	DHCPv6ClientInterfaces []string `json:"dhcpv6_client_interfaces"`
	Apps                   []AppRow `json:"apps"`
}

// MeasuredRow is one admitted port and where, as the firewall's measurement publishes it and the
// console's probe reads it.
type MeasuredRow struct {
	Port       string   `json:"port"`
	Interfaces []string `json:"interfaces"`
}

// InputError refuses a document. It names the field and the rule, never a value.
type InputError struct {
	Field  string
	Reason string
}

func (e *InputError) Error() string { return "policy refused at " + e.Field + ": " + e.Reason }

func refuse(field, reason string) error { return &InputError{Field: field, Reason: reason} }

// Validate refuses a document outside the schema.
func (d Document) Validate() error {
	switch {
	case d.SchemaVersion != SchemaVersion:
		return refuse("$.schema_version", "expected "+SchemaVersion)
	case d.SSHPort < 1 || d.SSHPort > 65535:
		return refuse("$.ssh_port", "expected a port from 1 to 65535")
	case d.Portal.Listen != ListenLocal && d.Portal.Listen != ListenManagement:
		return refuse("$.portal.listen", "expected local or management")
	case !interfaceNames(d.Portal.ManagementInterfaces, 0):
		return refuse("$.portal.management_interfaces", "expected at most sixteen distinct interface names")
	case d.Portal.Listen == ListenManagement && len(d.Portal.ManagementInterfaces) == 0:
		return refuse("$.portal.management_interfaces", "management needs one management interface or more")
	case !interfaceNames(d.DHCPv6ClientInterfaces, 0):
		return refuse("$.dhcpv6_client_interfaces", "expected at most sixteen distinct interface names")
	case d.Apps == nil || len(d.Apps) > MaxAppRows:
		return refuse("$.apps", "expected a list of at most 32 rows")
	}
	reserved := map[string]bool{strconv.Itoa(d.SSHPort) + "/tcp": true, ConsolePort: true, DHCPv6ClientPort: true}
	for _, port := range base.ProductPorts {
		reserved[port] = true
	}
	seen := map[string]bool{}
	for _, row := range d.Apps {
		switch {
		case !slug(row.App):
			return refuse("$.apps[].app", "expected an application slug")
		case !validPort(row.Port):
			return refuse("$.apps[].port", "expected <1-65535>/tcp or <1-65535>/udp")
		case reserved[row.Port]:
			return refuse("$.apps[].port", "the port belongs to SSH, the product, the console or DHCPv6")
		case seen[row.Port]:
			return refuse("$.apps[].port", "one row per port")
		case !(len(row.Interfaces) == 1 && row.Interfaces[0] == EveryInterface) && !interfaceNames(row.Interfaces, 1):
			return refuse("$.apps[].interfaces", `expected "*" alone or one to sixteen distinct interface names`)
		}
		seen[row.Port] = true
	}
	return nil
}

// Decode reads one document of at most MaxDocument bytes: one JSON object with no null, no
// repeated key, no member the schema does not name and nothing after it, then validates it.
func Decode(data []byte) (Document, error) {
	if len(data) > MaxDocument || !utf8.Valid(data) {
		return Document{}, refuse("$", "expected one UTF-8 document of at most 16384 bytes")
	}
	strict := json.NewDecoder(bytes.NewReader(data))
	if err := noNullNoRepeat(strict, 0); err != nil {
		return Document{}, err
	}
	if _, err := strict.Token(); !errors.Is(err, io.EOF) {
		return Document{}, refuse("$", "a second document follows the first")
	}
	var d Document
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil {
		return Document{}, refuse("$", "a member the schema does not name, or a value of the wrong type")
	}
	var exact map[string]json.RawMessage
	if err := json.Unmarshal(data, &exact); err != nil {
		return Document{}, refuse("$", "expected one JSON object")
	}
	for _, name := range []string{"schema_version", "ssh_port", "portal", "dhcpv6_client_interfaces", "apps"} {
		if _, ok := exact[name]; !ok {
			return Document{}, refuse("$."+name, "required")
		}
	}
	if err := d.Validate(); err != nil {
		return Document{}, err
	}
	return normalize(d), nil
}

// noNullNoRepeat reads one JSON value and refuses a null or a repeated object key at any depth,
// which encoding/json would otherwise accept silently.
func noNullNoRepeat(d *json.Decoder, depth int) error {
	if depth > 8 {
		return refuse("$", "the document nests too deeply")
	}
	token, err := d.Token()
	if err != nil {
		return refuse("$", "invalid or incomplete JSON")
	}
	if token == nil {
		return refuse("$", "null is not a value of this schema")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			name, isName := key.(string)
			if err != nil || !isName || seen[name] {
				return refuse("$", "a key is repeated or invalid")
			}
			seen[name] = true
		}
		if err := noNullNoRepeat(d, depth+1); err != nil {
			return err
		}
	}
	if _, err := d.Token(); err != nil {
		return refuse("$", "invalid or incomplete JSON")
	}
	return nil
}

// normalize sorts every interface list and the application rows, so equal policies have equal
// encodings and equal digests.
func normalize(d Document) Document {
	d.Portal.ManagementInterfaces = sortedCopy(d.Portal.ManagementInterfaces)
	d.DHCPv6ClientInterfaces = sortedCopy(d.DHCPv6ClientInterfaces)
	apps := make([]AppRow, 0, len(d.Apps))
	for _, row := range d.Apps {
		apps = append(apps, AppRow{App: row.App, Port: row.Port, Interfaces: sortedCopy(row.Interfaces)})
	}
	slices.SortFunc(apps, func(a, b AppRow) int {
		if c := strings.Compare(a.App, b.App); c != 0 {
			return c
		}
		return strings.Compare(a.Port, b.Port)
	})
	d.Apps = apps
	return d
}

func sortedCopy(names []string) []string {
	out := slices.Clone(names)
	if out == nil {
		out = []string{}
	}
	slices.Sort(out)
	return out
}

// Canonical encodes a valid document with its lists sorted, the form its digest covers.
func Canonical(d Document) ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(normalize(d))
}

// Digest names a document: "sha256:" and the hex SHA-256 of its canonical encoding, or "" for a
// document outside the schema.
func Digest(d Document) string {
	encoded, err := Canonical(d)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// discoveryRule keeps IPv6 neighbor and router discovery; without it SLAAC and neighbor
// resolution stop at the first load.
const discoveryRule = "icmpv6 type { nd-neighbor-solicit, nd-neighbor-advert, nd-router-advert } accept"

// hookLine is a base chain's hook and default policy.
func hookLine(chain string) string {
	return "type filter hook " + chain + " priority filter; policy drop;"
}

// stateRules open every base chain: replies are accepted and invalid packets dropped before any
// other rule.
var stateRules = []string{"ct state established,related accept", "ct state invalid drop"}

// InputRules returns the input chain's rules, in order, without its hook line.
func InputRules(d Document) []string {
	d = normalize(d)
	rules := append(slices.Clone(stateRules), `iif "lo" accept`, discoveryRule)
	if len(d.DHCPv6ClientInterfaces) > 0 {
		rules = append(rules, "iifname "+nameSet(d.DHCPv6ClientInterfaces)+" ip6 saddr fe80::/10 udp sport 547 udp dport 546 accept")
	}
	rules = append(rules, portRule(strconv.Itoa(d.SSHPort)+"/tcp", nil))
	for _, port := range base.ProductPorts {
		rules = append(rules, portRule(port, nil))
	}
	if consoleExposed(d.Portal) {
		rules = append(rules, portRule(ConsolePort, d.Portal.ManagementInterfaces))
	}
	for _, row := range d.Apps {
		var where []string
		if !(len(row.Interfaces) == 1 && row.Interfaces[0] == EveryInterface) {
			where = row.Interfaces
		}
		rules = append(rules, portRule(row.Port, where))
	}
	return rules
}

// ForwardRules returns the forward chain's rules without its hook line: replies only.
func ForwardRules() []string { return slices.Clone(stateRules) }

// consoleExposed reports whether the selection admits 9443 on the management interfaces.
func consoleExposed(p Portal) bool {
	return p.Enabled && p.Listen == ListenManagement && len(p.ManagementInterfaces) > 0
}

// portRule admits port ("<n>/<protocol>") on the named interfaces, or on every interface for none.
func portRule(port string, interfaces []string) string {
	number, protocol, _ := strings.Cut(port, "/")
	rule := protocol + " dport " + number + " accept"
	if len(interfaces) == 0 {
		return rule
	}
	return "iifname " + nameSet(interfaces) + " " + rule
}

// nameSet renders an anonymous set of validated interface names.
func nameSet(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, `"`+name+`"`)
	}
	return "{ " + strings.Join(quoted, ", ") + " }"
}

// Render renders the whole table as one nft transaction that creates the table if it is absent,
// deletes it and creates it again, so the old rules never mix with the new ones. The table's
// comment carries the digest. A document outside the schema renders nothing.
func Render(d Document) ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	d = normalize(d)
	var b strings.Builder
	b.WriteString("table inet olivares\ndelete table inet olivares\ntable inet olivares {\n")
	b.WriteString("\tcomment \"" + TableComment + Digest(d) + "\"\n")
	for _, chain := range []struct {
		name  string
		rules []string
	}{{"input", InputRules(d)}, {"forward", ForwardRules()}} {
		b.WriteString("\tchain " + chain.name + " {\n")
		b.WriteString("\t\t" + hookLine(chain.name) + "\n")
		for _, rule := range chain.rules {
			b.WriteString("\t\t" + rule + "\n")
		}
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	return []byte(b.String()), nil
}

// Rows lists the ports a valid document admits and where, as the measurement publishes them:
// SSH and the product ports on every interface, DHCPv6 replies on the client interfaces, 9443
// on the management interfaces when exposed, and each application row. A document outside the
// schema admits nothing.
func Rows(d Document) []MeasuredRow {
	if d.Validate() != nil {
		return nil
	}
	d = normalize(d)
	rows := []MeasuredRow{{Port: strconv.Itoa(d.SSHPort) + "/tcp", Interfaces: []string{EveryInterface}}}
	for _, port := range base.ProductPorts {
		rows = append(rows, MeasuredRow{Port: port, Interfaces: []string{EveryInterface}})
	}
	if len(d.DHCPv6ClientInterfaces) > 0 {
		rows = append(rows, MeasuredRow{Port: DHCPv6ClientPort, Interfaces: slices.Clone(d.DHCPv6ClientInterfaces)})
	}
	if consoleExposed(d.Portal) {
		rows = append(rows, MeasuredRow{Port: ConsolePort, Interfaces: slices.Clone(d.Portal.ManagementInterfaces)})
	}
	for _, row := range d.Apps {
		rows = append(rows, MeasuredRow{Port: row.Port, Interfaces: slices.Clone(row.Interfaces)})
	}
	return rows
}

// interfaceNames reports whether names holds from least to MaxInterfaces distinct interface names.
func interfaceNames(names []string, least int) bool {
	if names == nil && least == 0 {
		return true
	}
	if len(names) < least || len(names) > MaxInterfaces {
		return false
	}
	for _, name := range names {
		if !InterfaceName(name) {
			return false
		}
	}
	sorted := sortedCopy(names)
	return len(slices.Compact(sorted)) == len(names)
}

// InterfaceName reports whether name is a kernel interface name this policy renders: one to
// fifteen letters, digits, '.', '-' or '_', starting with a letter or digit. It can never close
// the quoted string it is rendered in.
func InterfaceName(name string) bool {
	if len(name) < 1 || len(name) > 15 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '-' || c == '_'):
		default:
			return false
		}
	}
	return true
}

// validPort accepts "<1-65535>/tcp" and "<1-65535>/udp" with no leading zero.
func validPort(port string) bool {
	number, protocol, ok := strings.Cut(port, "/")
	if !ok || (protocol != "tcp" && protocol != "udp") || number == "" || len(number) > 5 || number[0] == '0' {
		return false
	}
	n, err := strconv.Atoi(number)
	return err == nil && n >= 1 && n <= 65535 && strconv.Itoa(n) == number
}

// slug accepts an application slug: a lowercase letter, then up to 31 lowercase letters,
// digits or hyphens.
func slug(s string) bool {
	if s == "" || len(s) > 32 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// AppSlug reports whether s is an application slug.
func AppSlug(s string) bool { return slug(s) }

// Change is one row of a plan's difference, with what it means for the operator's session.
type Change struct {
	Kind string      `json:"kind"`
	Row  MeasuredRow `json:"row"`
	Note string      `json:"note,omitempty"`
}

// Diff lists the rows to adds and from removes. Removing or narrowing the console's row, which
// carries a remote operator's session, is noted "reverts unless confirmed".
func Diff(from, to Document) []Change {
	before, after := Rows(from), Rows(to)
	key := func(r MeasuredRow) string { return r.Port + " " + strings.Join(r.Interfaces, ",") }
	var changes []Change
	for _, r := range before {
		if !slices.ContainsFunc(after, func(a MeasuredRow) bool { return key(a) == key(r) }) {
			c := Change{Kind: "removed", Row: r}
			if r.Port == ConsolePort || r.Port == strconv.Itoa(from.SSHPort)+"/tcp" {
				c.Note = "reverts unless confirmed"
			}
			changes = append(changes, c)
		}
	}
	for _, r := range after {
		if !slices.ContainsFunc(before, func(b MeasuredRow) bool { return key(b) == key(r) }) {
			changes = append(changes, Change{Kind: "added", Row: r})
		}
	}
	return changes
}
