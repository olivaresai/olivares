// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/store"
)

const (
	auditSpoolMaxBytesEnv = "OLIVARES_AUDIT_SPOOL_MAX_BYTES"
	auditSpoolOnFullEnv   = "OLIVARES_AUDIT_SPOOL_ON_FULL"
)

// loadAuditSpoolConfig resolves ADR-0024 Q2's logical spool budget. An unset
// budget keeps the optional guard off, while a configured invalid budget refuses
// startup. An invalid exhaustion mode falls back to block so a typo can never
// weaken the deny-closed default (the same stay-safe posture as the
// policy-staleness loader).
func loadAuditSpoolConfig(getenv func(string) string, log *slog.Logger) (maxBytes int64, mode store.AuditSpoolMode, err error) {
	mode = store.AuditSpoolBlock
	if raw := getenv(auditSpoolMaxBytesEnv); raw != "" {
		value := strings.TrimSpace(raw)
		n, err := parseSpoolBytes(value)
		if err != nil {
			return 0, mode, fmt.Errorf("%s is set but invalid (%q): %w; refusing to start", auditSpoolMaxBytesEnv, raw, err)
		}
		maxBytes = n
	}

	switch raw := strings.ToLower(strings.TrimSpace(getenv(auditSpoolOnFullEnv))); raw {
	case "", string(store.AuditSpoolBlock):
		mode = store.AuditSpoolBlock
	case string(store.AuditSpoolDegrade):
		mode = store.AuditSpoolDegrade
	default:
		log.Error("audit: invalid OLIVARES_AUDIT_SPOOL_ON_FULL; using block", "value", raw)
		mode = store.AuditSpoolBlock
	}

	if maxBytes > 0 {
		log.Info("audit: spool budget enabled", "mode", mode, "max_bytes", maxBytes)
	}
	return maxBytes, mode, nil
}

// parseSpoolBytes parses a positive integer byte count, optionally with a
// binary-unit suffix KB, MB, GB or TB (powers of 1024, case-insensitive).
func parseSpoolBytes(raw string) (int64, error) {
	normalized := strings.ToUpper(raw)
	digits := normalized
	multiplier := int64(1)
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"KB", 1 << 10}, {"MB", 1 << 20}, {"GB", 1 << 30}, {"TB", 1 << 40}} {
		if strings.HasSuffix(normalized, u.suffix) {
			digits = strings.TrimSuffix(normalized, u.suffix)
			multiplier = u.mult
			break
		}
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 || n > (1<<63-1)/multiplier {
		return 0, fmt.Errorf("want a positive integer byte count, optionally with a KB, MB, GB or TB suffix (powers of 1024)")
	}
	return n * multiplier, nil
}

// auditMetaBlindingEnv selects the metadata-commitment WRITE rule;
// auditMetaBlindingAutoWord is the operator-facing spelling of the mode whose
// stored value is the empty string.
const (
	auditMetaBlindingEnv      = "OLIVARES_AUDIT_META_BLINDING"
	auditMetaBlindingAutoWord = "auto"
)

// loadAuditMetaBlinding resolves the metadata-commitment write rule from the
// operator's environment. It is the ONLY way an operator reaches the setting, and
// without it the store would always run the ledger-following default with no way
// to actuate — which would make the whole write gate decorative.
//
// An invalid value REFUSES startup rather than falling back. The spool loader
// above degrades a typo to its safe mode because both of its modes are legitimate
// running states, but this setting is different in kind: turning blinding on is
// IRREVERSIBLE for the rows it seals, so an operator who typed something they
// believed meant "on" must not be told nothing and left running the old rule, nor
// silently actuated a door that does not reopen. Refusing names the typo while it
// is still free to fix.
func loadAuditMetaBlinding(getenv func(string) string, log *slog.Logger) (store.AuditBlindingMode, error) {
	switch raw := strings.ToLower(strings.TrimSpace(getenv(auditMetaBlindingEnv))); raw {
	case "":
		return store.AuditBlindingAuto, nil
	// The constant's own value is the EMPTY string, so the literal word has to be
	// accepted here for an operator to be able to state the default on purpose —
	// otherwise "auto" would be a documented value that refuses startup.
	case auditMetaBlindingAutoWord:
		log.Info("audit: metadata blinding follows the ledger (auto): a ledger that already holds blinded rows keeps blinding, an empty one starts blinded, and one holding only pre-blinding rows keeps the legacy rule until this is set to \"on\"")
		return store.AuditBlindingAuto, nil
	case string(store.AuditBlindingOn):
		log.Info("audit: metadata blinding is ON for new events. This is IRREVERSIBLE for every row it seals: a node still running a binary without blinding support will report those rows as a hash mismatch. Every node must be upgraded before this is set")
		return store.AuditBlindingOn, nil
	case string(store.AuditBlindingOff):
		log.Warn("audit: metadata blinding is OFF for new events, so each exported metadata commitment is a deterministic function of that record's metadata alone and can be confirmed by guessing it")
		return store.AuditBlindingOff, nil
	default:
		return "", fmt.Errorf("%s is set but invalid (%q): want %q, %q or %q; refusing to start rather than guess at a setting whose actuation is irreversible",
			auditMetaBlindingEnv, raw, auditMetaBlindingAutoWord, store.AuditBlindingOn, store.AuditBlindingOff)
	}
}
