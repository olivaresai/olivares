// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aaa

import (
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/redact"
)

// TACACS+ protocol vocabulary (RFC 8907 "The TACACS+ Protocol", Informational,
// 2020 — the canonical TACACS+ RFC; RFC 9887 "TACACS+ over TLS 1.3", Proposed
// Standard, 2025, UPDATES it with a hardened transport but does not replace it).
// The protocol's packet bodies are: authentication START / REPLY / CONTINUE
// (§5), authorization REQUEST / REPLY (§6 — REPLY, not RESPONSE), and accounting
// "Account REQUEST" / "Accounting REPLY" (§7). The text accounting log this
// connector parses is the on-disk record of the accounting REQUEST packets.

// TACACS+ accounting REQUEST flags (RFC 8907 §7.1). The text accounting log
// renders these as start/stop/update; START and STOP are mutually exclusive.
const (
	tacacsAcctFlagStart    = 0x02 // TAC_PLUS_ACCT_FLAG_START
	tacacsAcctFlagStop     = 0x04 // TAC_PLUS_ACCT_FLAG_STOP
	tacacsAcctFlagWatchdog = 0x08 // TAC_PLUS_ACCT_FLAG_WATCHDOG
)

// acctFlagValue maps the text accounting flag to its RFC 8907 §7.1 wire value, so
// the finding detail records the protocol-accurate flag.
func acctFlagValue(flag string) int {
	switch flag {
	case "start":
		return tacacsAcctFlagStart
	case "stop":
		return tacacsAcctFlagStop
	case "update", "watchdog":
		return tacacsAcctFlagWatchdog
	default:
		return 0
	}
}

// parseTACACSLine parses one tac_plus accounting log record. The shrubbery/
// tac_plus-ng format is tab-delimited:
//
//	<date>\t<nas-ip>\t<user>\t<port>\t<rem_addr>\t<flag start|stop|update>\t<av-pairs...>
//
// where the av-pairs include service=, cmd=, priv-lvl=. TACACS+ is device
// administration by definition, so every record is a device-admin session signal.
// The command text (cmd=) can carry a secret, so it is redacted and hashed into
// the de-dup detail and NEVER placed in the displayable title (docs/SECURITY-HARDENING.md-3).
func parseTACACSLine(line string) (aaaEvent, bool) {
	fields := strings.Split(line, "\t")
	if len(fields) < 6 {
		// Fall back to whitespace splitting for space-delimited variants, keeping the
		// trailing av-pairs together.
		fields = looseFields(line)
		if len(fields) < 6 {
			return aaaEvent{}, false
		}
	}
	ev := aaaEvent{
		proto:       protoTACACS,
		nas:         strings.TrimSpace(fields[1]),
		user:        redact.Clean(strings.TrimSpace(fields[2])),
		device:      strings.TrimSpace(fields[4]),
		deviceAdmin: true, // TACACS+ is device administration by nature
	}
	flag := strings.ToLower(strings.TrimSpace(fields[5]))
	switch flag {
	case "start":
		ev.kind = kindAcctStart
	case "stop":
		ev.kind = kindAcctStop
	case "update", "watchdog":
		ev.kind = kindAcctInterim
	default:
		ev.kind = kindAccounting
	}

	avs := map[string]string{}
	for _, f := range fields[6:] {
		if k, v, ok := splitAV(f); ok {
			avs[strings.ToLower(k)] = v
		}
	}
	if ev.user == "" {
		return aaaEvent{}, false
	}
	// The executed command is PAYLOAD (it can carry a typed secret, e.g. Cisco
	// "enable secret 0 ...") and never travels — not even into the hash pre-image
	// (docs/SECURITY-HARDENING.md). Only a one-way fingerprint of it is kept, so identical commands
	// still de-duplicate without the content being recoverable.
	cmdFingerprint := ""
	if c := strings.TrimSpace(avs["cmd"]); c != "" {
		cmdFingerprint = redact.Hash(c)
	}
	ev.detail = fmt.Sprintf("tacacs|flag=0x%02x(%s)|nas=%s|user=%s|service=%s|priv-lvl=%s|cmd_sha=%s",
		acctFlagValue(flag), flag, ev.nas, ev.user, avs["service"], avs["priv-lvl"], cmdFingerprint)
	ev.service = avs["service"]
	ev.privLvl = avs["priv-lvl"]
	return ev, true
}

// splitAV parses an "attr=value" av-pair.
func splitAV(s string) (k, v string, ok bool) {
	s = strings.TrimSpace(s)
	i := strings.IndexByte(s, '=')
	if i <= 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// looseFields splits a space-delimited tac_plus line, rejoining the timestamp
// (which contains spaces) is out of scope — callers that use whitespace-delimited
// logs should prefer the tab format; this is a best-effort fallback that treats
// the first field as opaque and keeps trailing av-pairs intact.
func looseFields(line string) []string {
	return strings.Fields(line)
}
