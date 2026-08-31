// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codexmanagedconfig

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// maxConfigBytes caps each TOML read so a corrupt or hostile file cannot exhaust memory;
// a real managed config is small.
const maxConfigBytes = 1 << 20 // 1 MiB

// Source is the Codex managed-config verification SourceConnector. It is a BATCH source:
// Gather reads the live system-tier requirements.toml + managed_config.toml once, emits
// the allowed MCP servers / egress domains as PERMITTED edges and any drift/absence
// findings, and returns nil; the engine re-polls it on the operator's schedule (the
// connector never owns a ticker — docs/contracts/S02 §5).
type Source struct {
	cfg config
}

// Compile-time proof that Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a Codex managed-config connector with default configuration.
func New() *Source { return &Source{} }

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

// Open resolves and validates configuration. A bad path or a malformed expected_policy
// surfaces here, before Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return fmt.Errorf("codex-managed-config: %w", err)
	}
	s.cfg = c
	return nil
}

// Gather reads the live system-tier files and emits observations:
//   - a PERMITTED edge per allowed MCP server and per allowed egress domain,
//   - a drift finding per divergence from the authored intent (when an expected policy is configured),
//   - an absence/invalid finding when a file is missing or unparseable.
//
// A missing or unparseable file is a FINDING, not a Gather error (the host being
// unconstrained is exactly what this connector exists to report), so Gather returns nil
// and the engine re-polls. Only a genuine, possibly-transient read fault (e.g. permission
// denied) is returned so the engine retries with backoff.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	now := time.Now().UTC()
	expected := s.cfg.expected
	reqAuthored := expected != nil && expected.Requirements.hasAny()
	defAuthored := expected != nil && expected.Defaults.hasAny()

	// --- requirements.toml (the constraint layer) -------------------------------
	reqData, reqErr := readFileCapped(s.cfg.requirementsPath)
	reqAbsent := errors.Is(reqErr, os.ErrNotExist)
	if reqErr != nil && !reqAbsent {
		return fmt.Errorf("codex-managed-config: read %s: %w", s.cfg.requirementsPath, reqErr)
	}
	var (
		reqWire  requirementsWire
		reqMD    toml.MetaData
		reqValid bool
	)
	switch {
	case reqAbsent:
		if err := sink.Emit(ctx, requirementsAbsence(s.cfg.scope, "is absent", reqAuthored, now)); err != nil {
			return err
		}
	default:
		w, md, perr := parseRequirements(reqData)
		if perr != nil {
			if err := sink.Emit(ctx, requirementsAbsence(s.cfg.scope, "is present but invalid TOML", reqAuthored, now)); err != nil {
				return err
			}
		} else {
			reqWire, reqMD, reqValid = w, md, true
		}
	}

	// --- managed_config.toml (the managed-defaults layer) -----------------------
	mcData, mcErr := readFileCapped(s.cfg.managedConfigPath)
	mcAbsent := errors.Is(mcErr, os.ErrNotExist)
	if mcErr != nil && !mcAbsent {
		return fmt.Errorf("codex-managed-config: read %s: %w", s.cfg.managedConfigPath, mcErr)
	}
	var (
		mcWire  managedConfigWire
		mcMD    toml.MetaData
		mcValid bool
	)
	switch {
	case mcAbsent:
		// An absent managed_config is a finding only when the org authored defaults that
		// are not deployed; an empty managed-defaults state is normal (not a finding).
		if defAuthored {
			if err := sink.Emit(ctx, managedConfigAbsence(s.cfg.scope, "is absent", now)); err != nil {
				return err
			}
		}
	default:
		w, md, perr := parseManagedConfig(mcData)
		if perr != nil {
			if err := sink.Emit(ctx, managedConfigAbsence(s.cfg.scope, "is present but invalid TOML", now)); err != nil {
				return err
			}
		} else {
			mcWire, mcMD, mcValid = w, md, true
		}
	}

	// --- PERMITTED edges (from the authored intent when configured, else live) ---
	mcp, domains := s.permittedInputs(expected, reqWire, reqValid, mcWire, mcValid)
	for _, e := range permittedEdges(s.cfg.scope, mcp, domains, now) {
		if err := sink.Emit(ctx, e); err != nil {
			return err
		}
	}

	// --- drift (needs an authored intent to diff against) -----------------------
	if reqAuthored && reqValid {
		for _, f := range requirementsDrift(s.cfg.scope, expected.Requirements, reqWire, reqMD, now) {
			if err := sink.Emit(ctx, f); err != nil {
				return err
			}
		}
	}
	if defAuthored && mcValid {
		for _, f := range managedConfigDrift(s.cfg.scope, expected.Defaults, mcWire, mcMD, now) {
			if err := sink.Emit(ctx, f); err != nil {
				return err
			}
		}
	}
	return nil
}

// permittedInputs resolves the MCP-server and egress-domain sets to emit as PERMITTED
// edges: the AUTHORED intent when configured (what the org MEANS to permit), else the LIVE
// files (inventory what is in force).
func (s *Source) permittedInputs(expected *Policy, reqWire requirementsWire, reqValid bool, mcWire managedConfigWire, mcValid bool) (mcp []MCPServer, domains []string) {
	if expected != nil {
		if expected.Requirements.AllowedMCPServers != nil {
			mcp = *expected.Requirements.AllowedMCPServers
		}
		if expected.Defaults.Network != nil {
			domains = expected.Defaults.Network.AllowedDomains
		}
		return mcp, domains
	}
	if reqValid {
		mcp = liveMCPServers(reqWire.MCPServers)
	}
	if mcValid && mcWire.ExperimentalNetwork != nil {
		domains = mcWire.ExperimentalNetwork.AllowedDomains
	}
	return mcp, domains
}

// Close releases resources (none held).
func (s *Source) Close(context.Context) error { return nil }

// readFileCapped reads up to maxConfigBytes from path, preserving os.ErrNotExist so the
// caller can treat absence as a finding rather than a fault.
func readFileCapped(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied managed-config path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxConfigBytes))
}

// compile-time assurance the emitted observation types are the SDK's.
var (
	_ model.Observation = model.EdgeObservation{}
	_ model.Observation = model.FindingReport{}
)
