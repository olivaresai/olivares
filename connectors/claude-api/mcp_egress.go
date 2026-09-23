// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// mcp_egress.go is the MCP egress wire snapshot (C8 E2-3, the accepted MCP egress
// construction contract with its Root corrections). It owns the ONE parser for a request's
// declared remote MCP servers: it captures tools[] and mcp_servers[] once, validates them,
// describes each server to the egress gate by index and canonical origin only, and binds the
// accepted declaration — or its accepted absence — to the governed request, so every
// governed serializer (MarshalPrepared, CountTokens, MarshalPreparedBatch) verifies the bytes
// it actually produces. The direct submitters (CreateMessage, StreamMessage, CreateBatch,
// CreateBatchRaw) do not verify a binding and give a direct caller no egress authority; the
// proxy shell refuses to use them as a fallback for a bound decision.
//
// What an admission proves, and what it does not: an allowed request declared only origins
// the gate granted, and the upstream body carries exactly the captured declaration. It does
// not certify DNS resolution, provider redirects, the remote tool inventory, tool arguments
// or later remote execution. Private values (server and tool names, URL path and query,
// authorization tokens, per-tool configuration) stay in this module: the gate sees markers
// and canonical origins, and no error text carries a declared value.
package claudeapi

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

// ErrInvalidMCPOrigin is the class of every MCP origin normalization failure. Its messages
// carry a fixed reason, never the rejected value.
var ErrInvalidMCPOrigin = errors.New("claudeapi: invalid MCP origin")

// MCPEgressError is a refusal of the MCP egress protocol with one closed code. Its message is
// fixed: it never wraps parser text, a URL, a name or a token.
type MCPEgressError struct {
	Code MCPDenialCode
}

func (e *MCPEgressError) Error() string {
	return "claudeapi: MCP egress refused (" + string(e.Code) + ")"
}

func mcpRefusal(code MCPDenialCode) error { return &MCPEgressError{Code: code} }

func originError(reason string) error { return fmt.Errorf("%w: %s", ErrInvalidMCPOrigin, reason) }

// ---- exact origins ------------------------------------------------------------------------

const mcpDefaultPort = 443

// mcpIDNA validates A-labels with the repository-pinned x/net/idna lookup profile. A host is
// accepted only when its lowercased ASCII spelling is already what this profile emits. All
// options are strict, so their order cannot silently relax one (the trap core/webaddr/host.go
// records for its relaxed browser profile).
//
// This origin parser deliberately duplicates host policy that also lives in core/webaddr
// (host.go) and core/egress (egress.go): this Apache connector may not import the AGPL core,
// and the only shared home would be the Apache sdk/ tree. An x/net/idna version change or a
// numeric-host policy change must therefore be re-validated in three places, each by its own
// regression tables: TestMCPOriginNormalization* here, TestCanonicalHost* in core/egress, and
// TestParseCanonicalizes, TestNumericHostsABrowserCannotOpenAreRefused and
// TestGoldenTablesAreCurrent in core/webaddr.
var mcpIDNA = idna.New(idna.MapForLookup(), idna.StrictDomainName(true), idna.ValidateLabels(true),
	idna.BidiRule(), idna.VerifyDNSLength(true))

// MCPOriginFromURL validates a declared MCP server URL — absolute, hierarchical, HTTPS — and
// returns its canonical origin "https://" + host + ":" + effective port. The scheme compares
// case-insensitively; the forwarding URL itself is never rewritten. It rejects userinfo, any
// fragment delimiter, control or space characters, backslashes, percent escapes in the
// authority, malformed escapes elsewhere and an empty explicit port. Hosts are lowercased
// ASCII DNS names (Unicode input is rejected; A-labels must already be valid), strict dotted
// IPv4, or bracketed IPv6 without zone or IPv4 mapping. A last label that is a decimal or
// 0x-hexadecimal number is refused unless the whole host is strict IPv4. No DNS lookup runs.
func MCPOriginFromURL(raw string) (string, error) {
	origin, _, err := parseMCPOrigin(raw)
	return origin, err
}

// NormalizeMCPOrigin is the grant form of MCPOriginFromURL, for configured origins: the same
// parser, with no path beyond "/", no query delimiter and no fragment. The result carries no
// trailing slash.
func NormalizeMCPOrigin(raw string) (string, error) {
	origin, rest, err := parseMCPOrigin(raw)
	if err != nil {
		return "", err
	}
	if rest != "" && rest != "/" {
		return "", originError("a grant is an origin: no path or query")
	}
	return origin, nil
}

// parseMCPOrigin returns the canonical origin and what follows the authority (path and
// query, validated for escapes and otherwise opaque).
func parseMCPOrigin(raw string) (origin, rest string, err error) {
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; c <= ' ' || c == 0x7f || c == '\\' {
			return "", "", originError("control, space or backslash")
		}
	}
	if strings.IndexByte(raw, '#') >= 0 {
		return "", "", originError("fragment delimiter")
	}
	scheme, afterScheme, ok := strings.Cut(raw, ":")
	if !ok || !strings.EqualFold(scheme, "https") {
		return "", "", originError("scheme is not https")
	}
	hier, ok := strings.CutPrefix(afterScheme, "//")
	if !ok {
		return "", "", originError("not a hierarchical URL")
	}
	authority := hier
	if end := strings.IndexAny(hier, "/?"); end >= 0 {
		authority, rest = hier[:end], hier[end:]
	}
	if !validPercentEscapes(rest) {
		return "", "", originError("malformed percent escape")
	}
	if strings.ContainsAny(authority, "@%") {
		return "", "", originError("userinfo or escape in the authority")
	}
	host, port, err := splitMCPAuthority(authority)
	if err != nil {
		return "", "", err
	}
	return "https://" + host + ":" + strconv.Itoa(port), rest, nil
}

func validPercentEscapes(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		if i+2 >= len(s) || !isHexDigit(s[i+1]) || !isHexDigit(s[i+2]) {
			return false
		}
		i += 2
	}
	return true
}

func isHexDigit(c byte) bool {
	return ('0' <= c && c <= '9') || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F')
}

// splitMCPAuthority splits host[:port] and canonicalizes both halves.
func splitMCPAuthority(authority string) (host string, port int, err error) {
	hostPart, portPart, hasPort := authority, "", false
	if strings.HasPrefix(authority, "[") {
		end := strings.IndexByte(authority, ']')
		if end < 0 {
			return "", 0, originError("unterminated IPv6 literal")
		}
		hostPart = authority[1:end]
		switch after := authority[end+1:]; {
		case after == "":
		case after[0] == ':':
			portPart, hasPort = after[1:], true
		default:
			return "", 0, originError("data after the IPv6 literal")
		}
		if host, err = canonicalMCPIPv6(hostPart); err != nil {
			return "", 0, err
		}
	} else {
		if i := strings.LastIndexByte(authority, ':'); i >= 0 {
			hostPart, portPart, hasPort = authority[:i], authority[i+1:], true
		}
		if hostPart == "" {
			return "", 0, originError("empty host")
		}
		if host, err = canonicalMCPHost(hostPart); err != nil {
			return "", 0, err
		}
	}
	port = mcpDefaultPort
	if hasPort {
		if port, err = parseMCPPort(portPart); err != nil {
			return "", 0, err
		}
	}
	return host, port, nil
}

func parseMCPPort(p string) (int, error) {
	if p == "" {
		return 0, originError("empty explicit port")
	}
	for i := 0; i < len(p); i++ {
		if p[i] < '0' || p[i] > '9' {
			return 0, originError("port is not decimal")
		}
	}
	digits := strings.TrimLeft(p, "0")
	if digits == "" || len(digits) > 5 {
		return 0, originError("port out of range")
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n < 1 || n > 65535 {
		return 0, originError("port out of range")
	}
	return n, nil
}

func canonicalMCPIPv6(s string) (string, error) {
	addr, err := netip.ParseAddr(s)
	if err != nil || !addr.Is6() || addr.Is4In6() || addr.Zone() != "" {
		return "", originError("not a plain IPv6 address")
	}
	return "[" + addr.String() + "]", nil
}

// canonicalMCPHost accepts a lowercased ASCII DNS name or a strict dotted-quad IPv4 address.
func canonicalMCPHost(h string) (string, error) {
	for i := 0; i < len(h); i++ {
		if h[i] >= 0x80 {
			return "", originError("non-ASCII host; use its A-label")
		}
	}
	host := strings.ToLower(h)
	if strings.HasSuffix(host, ".") {
		return "", originError("trailing dot")
	}
	if len(host) > 253 {
		return "", originError("host too long")
	}
	labels := strings.Split(host, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return "", originError("invalid DNS label")
		}
		for i := 0; i < len(l); i++ {
			if c := l[i]; !('a' <= c && c <= 'z') && !('0' <= c && c <= '9') && c != '-' {
				return "", originError("invalid DNS label character")
			}
		}
	}
	if numericMCPLabel(labels[len(labels)-1]) {
		// A numeric final label is an IPv4 spelling: only the strict dotted quad is one.
		if addr, err := netip.ParseAddr(host); err == nil && addr.Is4() && len(labels) == 4 {
			return addr.String(), nil
		}
		return "", originError("numeric host is not strict IPv4")
	}
	ascii, err := mcpIDNA.ToASCII(host)
	if err != nil || ascii != host {
		return "", originError("invalid internationalized label")
	}
	return host, nil
}

// numericMCPLabel reports a label of decimal digits, or "0x" followed by hexadecimal digits.
func numericMCPLabel(l string) bool {
	digits, hex := l, false
	if strings.HasPrefix(l, "0x") {
		digits, hex = l[2:], true
	}
	if digits == "" {
		return hex
	}
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if !('0' <= c && c <= '9') && !(hex && isHexDigit(c)) {
			return false
		}
	}
	return true
}

// ---- the one wire parser ----------------------------------------------------------------

// mcpMaxDepth bounds the containers of one MCP object (a toolset nests at most three).
const mcpMaxDepth = 4

var errMCPWire = errors.New("claudeapi: malformed MCP wire value")

// decodeMCPValue decodes one serialized JSON value strictly: no duplicate object member at
// any depth, at most mcpMaxDepth nested containers, numbers kept as json.Number, and no
// trailing data. It is the only decoder of MCP values, at capture and at verification.
func decodeMCPValue(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := decodeMCPToken(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errMCPWire
	}
	return v, nil
}

func decodeMCPToken(dec *json.Decoder, depth int) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil // string, json.Number, bool or nil
	}
	if depth >= mcpMaxDepth {
		return nil, errMCPWire
	}
	switch delim {
	case '{':
		obj := map[string]any{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := kt.(string)
			if !ok {
				return nil, errMCPWire
			}
			if _, dup := obj[key]; dup {
				return nil, errMCPWire
			}
			if obj[key], err = decodeMCPToken(dec, depth+1); err != nil {
				return nil, err
			}
		}
		_, err = dec.Token() // '}'
		return obj, err
	case '[':
		arr := []any{}
		for dec.More() {
			v, err := decodeMCPToken(dec, depth+1)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		_, err = dec.Token() // ']'
		return arr, err
	}
	return nil, errMCPWire
}

// mcpClaim reports whether one serialized tools[] entry claims MCP: any root "type" member
// whose string value starts with "mcp_", or a root "mcp_server_name" member. Nested members
// (a custom tool's input_schema) claim nothing; a non-object claims nothing.
func mcpClaim(raw []byte) (bool, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return false, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return false, nil
	}
	claims := false
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return false, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return false, err
		}
		switch kt {
		case "mcp_server_name":
			claims = true
		case "type":
			var s string
			if json.Unmarshal(v, &s) == nil && strings.HasPrefix(s, "mcp_") {
				claims = true
			}
		}
	}
	return claims, nil
}

// claimsMCP classifies a Go tools[] value by what it serializes to. A typed MCPToolset claims
// MCP before serialization; a value that cannot be serialized or scanned is treated as a
// claim, so a view hides it and a rewrite carrying it is refused.
func claimsMCP(t any) bool {
	switch t.(type) {
	case MCPToolset, *MCPToolset:
		return true
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return true
	}
	claims, err := mcpClaim(raw)
	return claims || err != nil
}

func mcpMarker() map[string]any { return map[string]any{"type": mcpToolsetType} }

// isMCPMarker reports exactly {"type":"mcp_toolset"} as serialized.
func isMCPMarker(t any) bool {
	raw, err := json.Marshal(t)
	if err != nil {
		return false
	}
	v, err := decodeMCPValue(raw)
	m, ok := v.(map[string]any)
	return err == nil && ok && len(m) == 1 && m["type"] == mcpToolsetType
}

func cloneMCPValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[k] = cloneMCPValue(x)
		}
		return m
	case []any:
		a := make([]any, len(t))
		for i, x := range t {
			a[i] = cloneMCPValue(x)
		}
		return a
	default:
		return t // string, bool, nil or json.Number: immutable
	}
}

// onlyKeys reports whether every member of m is one of allowed.
func onlyKeys(m map[string]any, allowed ...string) bool {
	for k := range m {
		ok := false
		for _, a := range allowed {
			ok = ok || k == a
		}
		if !ok {
			return false
		}
	}
	return true
}

// validateMCPServer checks one mcp_servers[] entry and returns its name and origin.
func validateMCPServer(v any) (name, origin string, ok bool) {
	m, isObj := v.(map[string]any)
	if !isObj || !onlyKeys(m, "type", "name", "url", "authorization_token") || m["type"] != "url" {
		return "", "", false
	}
	name, isStr := m["name"].(string)
	rawURL, urlStr := m["url"].(string)
	if !isStr || name == "" || !urlStr {
		return "", "", false
	}
	if tok, present := m["authorization_token"]; present && tok != nil {
		if _, isStr := tok.(string); !isStr {
			return "", "", false
		}
	}
	origin, err := MCPOriginFromURL(rawURL)
	return name, origin, err == nil
}

// validateMCPToolset checks one MCP tools[] entry and returns the server it references.
func validateMCPToolset(v any) (server string, ok bool) {
	m, isObj := v.(map[string]any)
	if !isObj || !onlyKeys(m, "type", "mcp_server_name", "default_config", "configs", "cache_control") ||
		m["type"] != mcpToolsetType {
		return "", false
	}
	server, isStr := m["mcp_server_name"].(string)
	if !isStr || server == "" {
		return "", false
	}
	if dc, present := m["default_config"]; present && !validMCPToolConfig(dc) {
		return "", false
	}
	if cf, present := m["configs"]; present && cf != nil {
		configs, isObj := cf.(map[string]any)
		if !isObj {
			return "", false
		}
		for tool, c := range configs {
			if tool == "" || !validMCPToolConfig(c) {
				return "", false
			}
		}
	}
	if cc, present := m["cache_control"]; present && cc != nil {
		c, isObj := cc.(map[string]any)
		if !isObj || !onlyKeys(c, "type", "ttl") || c["type"] != cacheTypeEphemeral {
			return "", false
		}
		if ttl, present := c["ttl"]; present && ttl != "5m" && ttl != CacheControlTTL1h {
			return "", false
		}
	}
	return server, true
}

// validMCPToolConfig accepts an object whose only members are boolean enabled/defer_loading.
func validMCPToolConfig(v any) bool {
	m, isObj := v.(map[string]any)
	if !isObj || !onlyKeys(m, "enabled", "defer_loading") {
		return false
	}
	for _, x := range m {
		if _, isBool := x.(bool); !isBool {
			return false
		}
	}
	return true
}

// ---- the snapshot ---------------------------------------------------------------------------

type mcpSnapshotState uint8

const (
	mcpCaptured mcpSnapshotState = iota // captured; no gate input issued
	mcpIssued                           // gate input and coverage issued, once
	mcpApplied                          // a forwarding decision accepted and bound, once
	mcpSpent                            // a failed step: the snapshot admits nothing more
)

type mcpCapturedTool struct {
	index  int
	server string
	value  any
	canon  string
}

type mcpCapturedServer struct {
	name, origin string
	value        any
	canon        string
}

// MCPEgressSnapshot is the single-use private capture of one request's MCP declaration —
// or of its absence. Its storage is opaque: callers receive defensive copies only.
type MCPEgressSnapshot struct {
	req      MessageRequest // the rest of the request; MCP slots and servers are restored from below
	toolsets []mcpCapturedTool
	servers  []mcpCapturedServer
	dests    []MCPDestination
	entropy  io.Reader
	state    mcpSnapshotState
	coverage MCPEgressCoverage
	binding  *mcpBinding
}

// SnapshotMCPEgress captures req's tools[] and mcp_servers[] exactly once into an owned JSON
// graph and returns a detached request whose MCP values no caller alias can change. Every
// tools[] entry is serialized before it is classified, so a json.Marshaler cannot show a
// benign type now and emit MCP later; the captured graph is what is forwarded, and the
// caller's marshaler is not invoked again for it. A request without MCP yields a snapshot of
// its absence. Any malformed, ambiguous or unsupported declaration is an MCPEgressError with
// MCPDenyInvalidDeclaration and no snapshot.
func SnapshotMCPEgress(req MessageRequest) (MessageRequest, *MCPEgressSnapshot, error) {
	return snapshotMCPEgress(req, rand.Reader)
}

// snapshotMCPEgress takes the nonce source as an internal seam (tests inject a failing one).
func snapshotMCPEgress(req MessageRequest, entropy io.Reader) (MessageRequest, *MCPEgressSnapshot, error) {
	invalid := mcpRefusal(MCPDenyInvalidDeclaration)
	s := &MCPEgressSnapshot{req: req, entropy: entropy}
	s.req.mcp = nil
	if req.Tools != nil {
		s.req.Tools = make([]any, len(req.Tools))
	}
	for i, t := range req.Tools {
		raw, err := json.Marshal(t)
		if err != nil {
			return MessageRequest{}, nil, invalid
		}
		claims, err := mcpClaim(raw)
		if err != nil {
			return MessageRequest{}, nil, invalid
		}
		switch t.(type) {
		case MCPToolset, *MCPToolset:
			claims = true
		}
		if !claims {
			s.req.Tools[i] = t
			continue
		}
		v, err := decodeMCPValue(raw)
		if err != nil {
			return MessageRequest{}, nil, invalid
		}
		server, ok := validateMCPToolset(v)
		canon, err := json.Marshal(v)
		if !ok || err != nil {
			return MessageRequest{}, nil, invalid
		}
		s.toolsets = append(s.toolsets, mcpCapturedTool{index: i, server: server, value: v, canon: string(canon)})
	}
	if len(req.MCPServers) > MCPMaxDestinations {
		return MessageRequest{}, nil, invalid
	}
	for _, sv := range req.MCPServers {
		raw, err := json.Marshal(sv)
		if err != nil {
			return MessageRequest{}, nil, invalid
		}
		v, err := decodeMCPValue(raw)
		if err != nil {
			return MessageRequest{}, nil, invalid
		}
		name, origin, ok := validateMCPServer(v)
		canon, err := json.Marshal(v)
		if !ok || err != nil {
			return MessageRequest{}, nil, invalid
		}
		s.servers = append(s.servers, mcpCapturedServer{name: name, origin: origin, value: v, canon: string(canon)})
	}
	if !s.pairServers() {
		return MessageRequest{}, nil, invalid
	}
	if len(s.servers) > 0 {
		s.req.MCPServers = nil
	}
	return s.assemble(copyTools(s.req.Tools)), s, nil
}

// pairServers requires exactly one toolset per server and one server per toolset, matched by
// byte-identical names, and records the destinations in mcp_servers[] order.
func (s *MCPEgressSnapshot) pairServers() bool {
	byName := make(map[string]int, len(s.servers))
	toolFor := make([]int, len(s.servers))
	for j, sv := range s.servers {
		if _, dup := byName[sv.name]; dup {
			return false
		}
		byName[sv.name] = j
		toolFor[j] = -1
	}
	for _, ts := range s.toolsets {
		j, ok := byName[ts.server]
		if !ok || toolFor[j] >= 0 {
			return false
		}
		toolFor[j] = ts.index
	}
	for j, sv := range s.servers {
		if toolFor[j] < 0 {
			return false
		}
		s.dests = append(s.dests, MCPDestination{ServerIndex: uint32(j), ToolIndex: uint32(toolFor[j]), Origin: sv.origin})
	}
	return true
}

func copyTools(t []any) []any {
	if t == nil {
		return nil
	}
	return append(make([]any, 0, len(t)), t...)
}

// assemble returns the request with tools as its tools[] and fresh copies of the captured MCP
// values restored at their positions.
func (s *MCPEgressSnapshot) assemble(tools []any) MessageRequest {
	out := s.req
	out.Tools = tools
	for _, ts := range s.toolsets {
		out.Tools[ts.index] = cloneMCPValue(ts.value)
	}
	if len(s.servers) > 0 {
		out.MCPServers = make([]any, len(s.servers))
		for j, sv := range s.servers {
			out.MCPServers[j] = cloneMCPValue(sv.value)
		}
	}
	return out
}

// GateInput issues the gate-facing input once: tenant and actor as given, tools[] with every
// MCP slot reduced to the {"type":"mcp_toolset"} marker, and — when MCP is declared — the
// destinations and fresh coverage (a 32-byte nonce from the injected source and its
// MCPCoverageDigest). A failed or short nonce read is mcp_coverage_unavailable; a weak
// fallback nonce is never used. A second call is refused.
func (s *MCPEgressSnapshot) GateInput(tenant, actorRef string, unbindable bool) (ServerToolEgressInput, error) {
	if s == nil || s.state != mcpCaptured {
		return ServerToolEgressInput{}, mcpRefusal(MCPDenyCoverageUnavailable)
	}
	s.state = mcpSpent
	in := ServerToolEgressInput{Tenant: tenant, ActorRef: actorRef, UnbindableAgent: unbindable, Tools: copyTools(s.req.Tools)}
	for _, ts := range s.toolsets {
		in.Tools[ts.index] = mcpMarker()
	}
	if len(s.servers) > 0 {
		var nonce [32]byte
		if _, err := io.ReadFull(s.entropy, nonce[:]); err != nil {
			return ServerToolEgressInput{}, mcpRefusal(MCPDenyCoverageUnavailable)
		}
		in.MCP = &MCPEgressRequest{
			Coverage:     MCPEgressCoverage{Version: MCPEgressVersion, Nonce: nonce},
			Destinations: append([]MCPDestination(nil), s.dests...),
		}
		digest, err := MCPCoverageDigest(in)
		if err != nil {
			return ServerToolEgressInput{}, mcpRefusal(MCPDenyCoverageUnavailable)
		}
		in.MCP.Coverage.Digest = digest
		s.coverage = in.MCP.Coverage
	}
	s.state = mcpIssued
	return in, nil
}

// ApplyDecision accepts a Forward decision once and returns the governed request carrying
// an unexported, immutable binding of the accepted MCP declaration (or absence). A request
// that declares MCP requires the exact copy of its issued coverage and no denial code; a
// request without MCP must carry no acknowledgment; otherwise mcp_coverage_unavailable. The
// caller handles Forward=false before calling; such a decision is refused here too. A
// rewrite may change non-MCP slots only: for an MCP-bearing request the tool count is fixed
// and every MCP slot must be exactly the marker; no slot may newly claim MCP. MCP slots are
// restored solely from the capture. An illegal rewrite, or a second call, is
// mcp_binding_changed.
func (s *MCPEgressSnapshot) ApplyDecision(dec ServerToolEgressDecision) (MessageRequest, error) {
	if s == nil {
		return MessageRequest{}, mcpRefusal(MCPDenyCoverageUnavailable)
	}
	switch s.state {
	case mcpIssued:
	case mcpApplied:
		return MessageRequest{}, mcpRefusal(MCPDenyBindingChanged)
	default:
		return MessageRequest{}, mcpRefusal(MCPDenyCoverageUnavailable)
	}
	s.state = mcpSpent
	if !dec.Forward || dec.MCPDeny != "" {
		return MessageRequest{}, mcpRefusal(MCPDenyCoverageUnavailable)
	}
	if len(s.servers) > 0 {
		if dec.MCPAck == nil || *dec.MCPAck != s.coverage {
			return MessageRequest{}, mcpRefusal(MCPDenyCoverageUnavailable)
		}
	} else if dec.MCPAck != nil {
		return MessageRequest{}, mcpRefusal(MCPDenyCoverageUnavailable)
	}
	tools, ok := s.governedTools(dec)
	if !ok {
		return MessageRequest{}, mcpRefusal(MCPDenyBindingChanged)
	}
	out := s.assemble(tools)
	s.binding = s.newBinding(len(tools))
	out.mcp = s.binding
	s.state = mcpApplied
	return out, nil
}

func (s *MCPEgressSnapshot) governedTools(dec ServerToolEgressDecision) ([]any, bool) {
	if !dec.Rewritten {
		return copyTools(s.req.Tools), true
	}
	if len(s.toolsets) == 0 {
		for _, t := range dec.GovernedTools {
			if claimsMCP(t) {
				return nil, false
			}
		}
		return copyTools(dec.GovernedTools), true
	}
	if len(dec.GovernedTools) != len(s.req.Tools) {
		return nil, false
	}
	isMCP := make(map[int]bool, len(s.toolsets))
	for _, ts := range s.toolsets {
		isMCP[ts.index] = true
	}
	out := make([]any, len(dec.GovernedTools))
	for i, t := range dec.GovernedTools {
		switch {
		case isMCP[i] && !isMCPMarker(t):
			return nil, false
		case !isMCP[i] && claimsMCP(t):
			return nil, false
		case !isMCP[i]:
			out[i] = t
		}
	}
	return out, true
}

// CheckRequest verifies that req still carries THIS snapshot's accepted binding and that its
// MCP projection — every MCP value, its position, the tool count, and absence — is unchanged.
// A rebuilt request that dropped the binding, a binding from another snapshot, or any change
// is mcp_binding_changed. Run it after every later gate and before every serialization.
func (s *MCPEgressSnapshot) CheckRequest(req MessageRequest) error {
	if s == nil || s.binding == nil || req.mcp != s.binding {
		return mcpRefusal(MCPDenyBindingChanged)
	}
	body, err := json.Marshal(mcpFields{Tools: req.Tools, MCPServers: req.MCPServers})
	if err != nil {
		return mcpRefusal(MCPDenyBindingChanged)
	}
	return s.binding.verifyBody(body)
}

type mcpFields struct {
	Tools      []any `json:"tools,omitempty"`
	MCPServers []any `json:"mcp_servers,omitempty"`
}

// MCPGateTools returns the view of tools a later gate may receive: a new slice in which every
// entry that claims MCP (classified by its serialized value) is a fresh marker and every
// other entry is unchanged. Gates never receive the request's own MCP values.
func MCPGateTools(tools []any) []any {
	out := copyTools(tools)
	for i, t := range out {
		if claimsMCP(t) {
			out[i] = mcpMarker()
		}
	}
	return out
}

// ---- the accepted binding -------------------------------------------------------------------

// mcpBinding is the immutable accepted MCP projection of one admission. A governed request
// carries a pointer to it; pointer identity is the admission identity.
type mcpBinding struct {
	present   bool
	toolCount int
	slots     []mcpBoundSlot
	servers   []string
}

type mcpBoundSlot struct {
	index int
	canon string
}

func (s *MCPEgressSnapshot) newBinding(toolCount int) *mcpBinding {
	b := &mcpBinding{present: len(s.servers) > 0}
	if !b.present {
		return b
	}
	b.toolCount = toolCount
	for _, ts := range s.toolsets {
		b.slots = append(b.slots, mcpBoundSlot{index: ts.index, canon: ts.canon})
	}
	for _, sv := range s.servers {
		b.servers = append(b.servers, sv.canon)
	}
	return b
}

// verifyBody reads the MCP projection of one serialized Messages-shaped body (a request, a
// count_tokens body or one batch entry's params) with the capture parser and requires it to
// equal the binding.
func (b *mcpBinding) verifyBody(body []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return mcpRefusal(MCPDenyBindingChanged)
	}
	var tools, servers []json.RawMessage
	if raw, ok := top["tools"]; ok && json.Unmarshal(raw, &tools) != nil {
		return mcpRefusal(MCPDenyBindingChanged)
	}
	if raw, ok := top["mcp_servers"]; ok && json.Unmarshal(raw, &servers) != nil {
		return mcpRefusal(MCPDenyBindingChanged)
	}
	var slots []mcpBoundSlot
	for i, t := range tools {
		claims, err := mcpClaim(t)
		if err != nil {
			return mcpRefusal(MCPDenyBindingChanged)
		}
		if claims {
			canon, ok := canonicalMCP(t)
			if !ok {
				return mcpRefusal(MCPDenyBindingChanged)
			}
			slots = append(slots, mcpBoundSlot{index: i, canon: canon})
		}
	}
	if !b.present {
		if len(slots) != 0 || len(servers) != 0 {
			return mcpRefusal(MCPDenyBindingChanged)
		}
		return nil
	}
	if len(tools) != b.toolCount || len(slots) != len(b.slots) || len(servers) != len(b.servers) {
		return mcpRefusal(MCPDenyBindingChanged)
	}
	for i := range slots {
		if slots[i] != b.slots[i] {
			return mcpRefusal(MCPDenyBindingChanged)
		}
	}
	for i, sv := range servers {
		if canon, ok := canonicalMCP(sv); !ok || canon != b.servers[i] {
			return mcpRefusal(MCPDenyBindingChanged)
		}
	}
	return nil
}

func canonicalMCP(raw []byte) (string, bool) {
	v, err := decodeMCPValue(raw)
	if err != nil {
		return "", false
	}
	canon, err := json.Marshal(v)
	return string(canon), err == nil
}

// verify checks a frozen body and the beta set frozen with it.
func (b *mcpBinding) verify(body []byte, betas []string) error {
	if err := b.verifyBody(body); err != nil {
		return err
	}
	if !mcpBetaConsistent(betas, b.present) {
		return mcpRefusal(MCPDenyBindingChanged)
	}
	return nil
}

// mcpBetaConsistent requires the current MCP beta exactly once when MCP is bound, never
// otherwise, and the deprecated beta never.
func mcpBetaConsistent(betas []string, present bool) bool {
	current, deprecated := 0, 0
	for _, h := range betas {
		switch h {
		case MCPBetaHeader:
			current++
		case MCPBetaHeaderDeprecated:
			deprecated++
		}
	}
	want := 0
	if present {
		want = 1
	}
	return current == want && deprecated == 0
}

// verifyBatchBindings checks each entry's exact params bytes against that entry's OWN
// binding, in index order, and the envelope's MCP beta against the bound declarations. A
// governed batch binds every entry (an absence included), so an envelope mixing bound and
// unbound entries is a composition error and is refused; an envelope with no bound entry is
// the unbound legacy path.
func verifyBatchBindings(envelope []byte, requests []BatchRequest, betas []string) error {
	bound, allBound, present := false, true, false
	for _, r := range requests {
		if r.Params.mcp == nil {
			allBound = false
			continue
		}
		bound = true
		present = present || r.Params.mcp.present
	}
	if !bound {
		return nil
	}
	if !allBound {
		return mcpRefusal(MCPDenyBindingChanged)
	}
	var env struct {
		Requests []struct {
			Params json.RawMessage `json:"params"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(envelope, &env); err != nil || len(env.Requests) != len(requests) {
		return mcpRefusal(MCPDenyBindingChanged)
	}
	for i, r := range requests {
		if err := r.Params.mcp.verifyBody(env.Requests[i].Params); err != nil {
			return err
		}
	}
	if !mcpBetaConsistent(betas, present) {
		return mcpRefusal(MCPDenyBindingChanged)
	}
	return nil
}

// batchBound reports whether any entry carries an accepted binding.
func batchBound(requests []BatchRequest) bool {
	for _, r := range requests {
		if r.Params.mcp != nil {
			return true
		}
	}
	return false
}
