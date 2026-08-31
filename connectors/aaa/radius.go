// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aaa

import (
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/redact"
)

// RADIUS packet Code values the connector observes (RFC 2865 §3, RFC 2866 §3).
// The full registry also defines Accounting-Response=5 and Access-Challenge=11,
// which are server responses the connector does not ingest, so they are not named.
const (
	radiusAccessRequest = 1 // Access-Request
	radiusAccessAccept  = 2 // Access-Accept
	radiusAccessReject  = 3 // Access-Reject
	radiusAccountingReq = 4 // Accounting-Request
)

// RADIUS attribute Type values (RFC 2865 §5, RFC 2866 §5). User-Password (Type 2)
// is listed ONLY so the connector can assert it never reads it (docs/SECURITY-HARDENING.md): the
// password is obfuscated with the shared secret and is a credential, never an
// identity.
const (
	attrUserName       = 1  // User-Name
	attrUserPassword   = 2  // User-Password — NEVER read (credential, obfuscated)
	attrNASIPAddress   = 4  // NAS-IP-Address
	attrServiceType    = 6  // Service-Type
	attrCallingStation = 31 // Calling-Station-Id
	attrNASIdentifier  = 32 // NAS-Identifier
	attrAcctStatusType = 40 // Acct-Status-Type
	attrAcctSessionID  = 44 // Acct-Session-Id
)

// Acct-Status-Type values (RFC 2866 §5.1).
const (
	acctStart   = 1 // Start
	acctStop    = 2 // Stop
	acctInterim = 3 // Interim-Update
	acctOn      = 7 // Accounting-On
	acctOff     = 8 // Accounting-Off
)

// Service-Type values that denote device administration (RFC 2865 §5.6): an
// Administrative-User or NAS-Prompt-User session is privileged device-admin, not
// ordinary network access — the governance-relevant RADIUS signal.
const (
	svcAdministrativeUser = 6 // Administrative-User
	svcNASPromptUser      = 7 // NAS-Prompt-User
)

// radiusHeaderLen is Code(1)+Identifier(1)+Length(2)+Authenticator(16).
const radiusHeaderLen = 20

// decodeRADIUS parses a RADIUS packet into a normalized AAA event, reading only
// non-sensitive identity/accounting attributes. It NEVER reads User-Password (the
// shared-secret-obfuscated credential) and never needs the shared secret to read
// an accounting packet (the Authenticator is skipped, not verified — observation,
// not authentication: docs/SECURITY-HARDENING.md). A malformed packet is an error, not a panic.
func decodeRADIUS(pkt []byte) (aaaEvent, error) {
	if len(pkt) < radiusHeaderLen {
		return aaaEvent{}, fmt.Errorf("radius: short packet (%d bytes)", len(pkt))
	}
	code := pkt[0]
	length := int(binary.BigEndian.Uint16(pkt[2:4]))
	if length < radiusHeaderLen || length > len(pkt) {
		return aaaEvent{}, fmt.Errorf("radius: bad declared length %d (have %d)", length, len(pkt))
	}

	ev := aaaEvent{proto: protoRADIUS}
	var statusType, serviceType int
	var sessionID string
	// Walk the attribute TLVs after the 20-byte header (the Authenticator is part of
	// the header and is intentionally not inspected).
	for i := radiusHeaderLen; i+2 <= length; {
		typ := pkt[i]
		alen := int(pkt[i+1])
		if alen < 2 || i+alen > length {
			break // malformed attribute; stop (already parsed what we safely can)
		}
		val := pkt[i+2 : i+alen]
		switch typ {
		case attrUserName:
			ev.user = redact.Clean(string(val))
		case attrNASIPAddress:
			if len(val) == 4 {
				ev.nas = net.IP(val).String()
			}
		case attrNASIdentifier:
			if ev.nas == "" {
				ev.nas = redact.Clean(string(val))
			}
		case attrCallingStation:
			ev.device = redact.Clean(string(val))
		case attrAcctStatusType:
			if len(val) == 4 {
				statusType = int(binary.BigEndian.Uint32(val))
			}
		case attrServiceType:
			if len(val) == 4 {
				serviceType = int(binary.BigEndian.Uint32(val))
			}
		case attrAcctSessionID:
			sessionID = redact.Clean(string(val))
		case attrUserPassword:
			// Explicitly ignored: it is the obfuscated credential, never an identity.
		}
		i += alen
	}

	ev.kind, ev.deviceAdmin = classifyRADIUS(int(code), statusType, serviceType)
	ev.detail = fmt.Sprintf("radius|code=%d|status=%d|svc=%d|user=%s|nas=%s|sess=%s", code, statusType, serviceType, ev.user, ev.nas, sessionID)
	return ev, nil
}

// classifyRADIUS maps a packet code (+ accounting/service sub-type) to the event
// kind and whether it is a device-admin session.
func classifyRADIUS(code, statusType, serviceType int) (kind string, deviceAdmin bool) {
	admin := serviceType == svcAdministrativeUser || serviceType == svcNASPromptUser
	switch code {
	case radiusAccessReject:
		return kindAuthReject, admin
	case radiusAccessAccept:
		return kindAuthAccept, admin
	case radiusAccountingReq:
		switch statusType {
		case acctStart:
			return kindAcctStart, admin
		case acctStop:
			return kindAcctStop, admin
		case acctInterim:
			return kindAcctInterim, admin
		case acctOn, acctOff:
			return kindAcctNASState, admin
		default:
			return kindAccounting, admin
		}
	default:
		return kindOther, admin
	}
}

// parseRADIUSDetailRecord turns one FreeRADIUS "detail" file record (a timestamp
// header line followed by tab-indented "Attribute = value" lines) into an AAA
// event. The detail file is accounting/auth metadata the server already wrote to
// disk; the connector reads it read-only and never the obfuscated User-Password.
func parseRADIUSDetailRecord(header string, attrs []string) (aaaEvent, bool) {
	ev := aaaEvent{proto: protoRADIUS, detail: "radius-detail"}
	var statusType, serviceType, packetType int
	for _, line := range attrs {
		k, v, ok := splitAttr(line)
		if !ok {
			continue
		}
		switch k {
		case "User-Name":
			ev.user = redact.Clean(unquote(v))
		case "NAS-IP-Address":
			ev.nas = unquote(v)
		case "NAS-Identifier":
			if ev.nas == "" {
				ev.nas = redact.Clean(unquote(v))
			}
		case "Calling-Station-Id":
			ev.device = redact.Clean(unquote(v))
		case "Acct-Status-Type":
			statusType = radiusEnum(v, map[string]int{"Start": acctStart, "Stop": acctStop, "Interim-Update": acctInterim, "Accounting-On": acctOn, "Accounting-Off": acctOff})
		case "Service-Type":
			serviceType = radiusEnum(v, map[string]int{"Administrative-User": svcAdministrativeUser, "NAS-Prompt-User": svcNASPromptUser})
		case "Packet-Type":
			packetType = radiusEnum(v, map[string]int{"Access-Request": radiusAccessRequest, "Access-Accept": radiusAccessAccept, "Access-Reject": radiusAccessReject, "Accounting-Request": radiusAccountingReq})
		case "User-Password", "Password", "CHAP-Password":
			// Never read: credential, not identity (docs/SECURITY-HARDENING.md).
		}
	}
	code := packetType
	if code == 0 {
		code = radiusAccountingReq // a detail record with no Packet-Type is accounting
	}
	ev.kind, ev.deviceAdmin = classifyRADIUS(code, statusType, serviceType)
	if ev.user == "" {
		return aaaEvent{}, false
	}
	ev.detail = fmt.Sprintf("radius-detail|%s|code=%d|status=%d|svc=%d|user=%s|nas=%s", strings.TrimSpace(header), code, statusType, serviceType, ev.user, ev.nas)
	return ev, true
}

// radiusEnum resolves a FreeRADIUS attribute value that may be a name or a number.
func radiusEnum(v string, names map[string]int) int {
	v = strings.TrimSpace(unquote(v))
	if n, ok := names[v]; ok {
		return n
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n
	}
	return 0
}

// splitAttr parses a "Key = value" detail line (leading whitespace already kept by
// the caller's record assembly).
func splitAttr(line string) (key, val string, ok bool) {
	line = strings.TrimSpace(line)
	i := strings.Index(line, " = ")
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+3:]), true
}

// unquote strips surrounding double quotes from a detail value.
func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return v[1 : len(v)-1]
	}
	return v
}
