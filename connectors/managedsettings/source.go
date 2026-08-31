// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// maxSettingsBytes caps the managed-settings.json read so a corrupt or hostile
// file cannot exhaust memory; a real managed policy is small.
const maxSettingsBytes = 1 << 20 // 1 MiB

// Source is the managed-settings verification SourceConnector. It is a BATCH
// source: Gather reads the live file once, emits the managed grants and any drift,
// and returns nil; the engine re-polls it on the schedule the operator configures
// (the connector never owns a ticker — docs/contracts/S02 §5).
type Source struct {
	cfg config
}

// Compile-time proof that Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a managed-settings connector with default configuration.
func New() *Source { return &Source{} }

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

// Open resolves and validates configuration. A bad config_path or a malformed
// expected_policy surfaces here, before Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return fmt.Errorf("managed-settings: %w", err)
	}
	s.cfg = c
	return nil
}

// Gather reads the live managed-settings.json and emits observations:
//   - a PERMITTED policy edge per allow rule (the managed grants feeding module III),
//   - a policy_drift finding per divergence from the authored intent (when an
//     expected policy is configured),
//   - a high-severity finding when the file is absent or invalid (host ungoverned).
//
// A missing or unparseable file is a FINDING, not a Gather error (the host being
// ungoverned is exactly what this connector exists to report), so Gather returns
// nil and the engine re-polls. Only a genuine, possibly-transient read fault (e.g.
// permission denied) is returned so the engine retries with backoff.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	now := time.Now().UTC()

	data, baseErr := readFileCapped(s.cfg.configPath)
	baseAbsent := errors.Is(baseErr, os.ErrNotExist)
	if baseErr != nil && !baseAbsent {
		// A non-not-exist error (permission denied, I/O) may be transient/misconfig;
		// surface it so the engine retries rather than masking it as a finding.
		return fmt.Errorf("managed-settings: read %s: %w", s.cfg.configPath, baseErr)
	}

	// Read the managed-settings.d/ drop-in fragments. A read fault on a PRESENT
	// directory is surfaced for retry; malformed fragments become Medium findings emitted
	// below. An absent directory yields nothing (the common case).
	frags, fragFindings, dropErr := loadDropin(s.cfg.dropinDir, s.cfg.scope, now)
	if dropErr != nil {
		return fmt.Errorf("managed-settings: read drop-in %s: %w", s.cfg.dropinDir, dropErr)
	}
	for _, f := range fragFindings {
		if eerr := sink.Emit(ctx, f); eerr != nil {
			return eerr
		}
	}

	// Resolve the host's EFFECTIVE managed config: the base file merged FIRST, then the
	// drop-in fragments deep-merged in alphabetical order (Claude Code's documented
	// semantics — see dropin.go). When no fragments are present the base bytes are parsed
	// directly (byte-identical to the pre path — no merge round-trip).
	var live managedSettings
	switch {
	case baseAbsent && len(frags) == 0:
		return sink.Emit(ctx, absenceFinding(s.cfg.scope, "is absent", now))
	case len(frags) == 0:
		l, perr := parseLive(data)
		if perr != nil {
			return sink.Emit(ctx, absenceFinding(s.cfg.scope, "is present but invalid JSON", now))
		}
		live = l
	default:
		base := map[string]any{}
		if !baseAbsent {
			obj, ok := jsonObject(data)
			if !ok {
				// A present-but-invalid base is itself a finding; the fragments still govern.
				if eerr := sink.Emit(ctx, absenceFinding(s.cfg.scope, "is present but invalid JSON", now)); eerr != nil {
					return eerr
				}
			} else {
				base = obj
			}
		}
		mergedBytes, merr := mergeEffective(base, frags)
		if merr != nil {
			return fmt.Errorf("managed-settings: merge drop-in: %w", merr)
		}
		l, perr := parseLive(mergedBytes)
		if perr != nil {
			// The merged document is well-formed by construction; report honestly if not.
			return sink.Emit(ctx, absenceFinding(s.cfg.scope, "merged to invalid JSON", now))
		}
		live = l
	}

	// PERMITTED edges come from the authored intent when configured (what the org
	// MEANS to grant), else from the live config (inventory what is in force).
	allow := live.liveAllowRules()
	if s.cfg.expected != nil {
		allow = s.cfg.expected.Permissions.Allow
	}
	for _, e := range permittedEdges(s.cfg.scope, allow, now) {
		if eerr := sink.Emit(ctx, e); eerr != nil {
			return eerr
		}
	}

	// Drift findings need an authored intent to diff against.
	if s.cfg.expected != nil {
		for _, f := range driftFindings(s.cfg.scope, *s.cfg.expected, live, now) {
			if ferr := sink.Emit(ctx, f); ferr != nil {
				return ferr
			}
		}
	}
	return nil
}

// Close releases resources (none held).
func (s *Source) Close(context.Context) error { return nil }

// readFileCapped reads up to maxSettingsBytes from path, preserving os.ErrNotExist
// so the caller can treat absence as a finding rather than a fault.
func readFileCapped(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied managed-settings path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxSettingsBytes))
}

// compile-time assurance the emitted observation types are the SDK's.
var (
	_ model.Observation = model.EdgeObservation{}
	_ model.Observation = model.FindingReport{}
)
