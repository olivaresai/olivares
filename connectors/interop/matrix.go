// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package interop is the machine-readable Tier-1 interop qualification matrix:
// the explicitly declared subset of connectors and each one's REAL verification level.
// It exists so a compatibility claim is per-connector and evidence-backed — a connector
// COUNT is not a compatibility claim. The badge escalates fixture-only ->
// conformance-defined (an integration-tagged job exists) -> continuously-verified (that
// job ran green against a live reference/vendor endpoint on last_verified). matrix_test
// enforces the honesty invariant: a job-backed badge requires a real integration test.
package interop

import (
	_ "embed"
	"encoding/json"
)

// Verification levels (see tier1-matrix.json policy.badges for the prose).
const (
	VerificationFixtureOnly          = "fixture-only"
	VerificationConformanceDefined   = "conformance-defined"
	VerificationContinuouslyVerified = "continuously-verified"
)

//go:embed tier1-matrix.json
var matrixJSON []byte

// Matrix is the whole declared qualification matrix.
type Matrix struct {
	Note    string  `json:"note"`
	Policy  Policy  `json:"policy"`
	Entries []Entry `json:"entries"`
}

// Policy is the breakage/deprecation posture and the badge definitions.
type Policy struct {
	DeprecationSLADays int               `json:"deprecation_sla_days"`
	Breakage           string            `json:"breakage"`
	Badges             map[string]string `json:"badges"`
}

// Entry is one declared connector and its real qualification level.
type Entry struct {
	Connector    string       `json:"connector"`
	Surface      string       `json:"surface"`
	Vendor       string       `json:"vendor"`
	APIVersion   string       `json:"api_version"`
	AuthModes    []string     `json:"auth_modes"`
	CloudLimits  string       `json:"cloud_limits"`
	Tier         string       `json:"tier"`
	Verification string       `json:"verification"`
	Conformance  *Conformance `json:"conformance"`
	// LastVerified is the date (YYYY-MM-DD) a conformance job last ran green against a
	// live endpoint, or nil if it never has. Required and non-nil only for
	// continuously-verified.
	LastVerified *string `json:"last_verified"`
}

// Conformance describes the live qualification job that backs a non-fixture badge.
type Conformance struct {
	Type string   `json:"type"`
	Env  []string `json:"env"`
	Job  string   `json:"job"`
}

// RequiresConformanceJob reports whether a verification level must be backed by an
// integration-tagged conformance job.
func RequiresConformanceJob(verification string) bool {
	return verification == VerificationConformanceDefined || verification == VerificationContinuouslyVerified
}

// Load parses the embedded matrix.
func Load() (Matrix, error) {
	var m Matrix
	if err := json.Unmarshal(matrixJSON, &m); err != nil {
		return Matrix{}, err
	}
	return m, nil
}
