// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandboxrt

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
)

// This file defines the GUEST-HARNESS I/O contract the OS-level backends
// (gvisor.go, firecracker.go) speak to whatever runs INSIDE the isolated
// instance. It is the same shape of external dependency the deploy executor's
// backends have on the `tofu`/`git` binaries: the backend CONSTRUCTS the
// invocation and PARSES the result, and is fully exercised in tests with a fake
// cmdRunner — while the real guest program ("sandbox-harness", an operator-
// provisioned entrypoint baked into the base image) and the real runsc/
// firecracker spawn are the production path, gated behind Preflight.
//
// The contract: the backend writes a harnessJob JSON file into the instance's
// (read-only) bundle and the harness writes a harnessResult JSON to stdout. The
// harness resolves steps against mocks (deterministic, like the in-proc default)
// and, for a probe, delivers the payload to the target THROUGH the proxy
// (HTTP(S)_PROXY) and returns the response — never reaching anything but the
// allowlisted target, because the instance has no other egress.

// harnessJob is the serialized job handed to the guest harness.
type harnessJob struct {
	Steps     []harnessStep `json:"steps,omitempty"`
	Mocks     []harnessMock `json:"mocks,omitempty"`
	Probe     *harnessProbe `json:"probe,omitempty"`
	Target    string        `json:"target,omitempty"`
	ProxyURL  string        `json:"proxy_url,omitempty"`
	TimeoutMS int64         `json:"timeout_ms,omitempty"`
}

type harnessStep struct {
	Key   string `json:"key"`
	Input string `json:"input"`
}

type harnessMock struct {
	Resource string `json:"resource"`
	Response string `json:"response"`
}

type harnessProbe struct {
	ID      string `json:"id"`
	Surface string `json:"surface"`
	Payload string `json:"payload"`
}

// harnessResult is the JSON the guest harness writes to stdout.
type harnessResult struct {
	Steps    []harnessStepOutput `json:"steps"`
	Response string              `json:"response,omitempty"`
	Reached  bool                `json:"reached,omitempty"`
	Error    string              `json:"error,omitempty"`
}

type harnessStepOutput struct {
	Key     string `json:"key"`
	Output  string `json:"output"`
	MockHit bool   `json:"mock_hit"`
}

// encodeHarnessJob serializes a Job (+ the proxy address the instance must use)
// into the guest-harness input. proxyAddr "" leaves no proxy (deny-all egress;
// the harness makes no network calls).
func encodeHarnessJob(job Job, proxyAddr string) ([]byte, error) {
	hj := harnessJob{Target: job.Target}
	if proxyAddr != "" {
		hj.ProxyURL = "http://" + proxyAddr
	}
	if job.Timeout > 0 {
		hj.TimeoutMS = job.Timeout.Milliseconds()
	}
	for _, s := range job.Steps {
		hj.Steps = append(hj.Steps, harnessStep(s))
	}
	for _, m := range job.Mocks {
		hj.Mocks = append(hj.Mocks, harnessMock(m))
	}
	if job.Probe != nil {
		hj.Probe = &harnessProbe{ID: job.Probe.ID, Surface: job.Probe.Surface, Payload: job.Probe.Payload}
	}
	return json.MarshalIndent(hj, "", "  ")
}

// decodeHarnessResult parses the guest harness's stdout into the neutral pieces
// the backend reports. A malformed or empty result is an execution fault (the
// instance produced nothing usable) — never silently a pass.
func decodeHarnessResult(stdout []byte) ([]StepOutput, string, bool, error) {
	trimmed := strings.TrimSpace(string(stdout))
	if trimmed == "" {
		return nil, "", false, fmt.Errorf("sandboxrt: guest harness produced no output")
	}
	var hr harnessResult
	if err := json.Unmarshal([]byte(trimmed), &hr); err != nil {
		return nil, "", false, fmt.Errorf("sandboxrt: guest harness output is not valid JSON")
	}
	if hr.Error != "" {
		return nil, "", false, fmt.Errorf("sandboxrt: guest harness error: %s", hr.Error)
	}
	out := make([]StepOutput, 0, len(hr.Steps))
	for _, s := range hr.Steps {
		out = append(out, StepOutput(s))
	}
	return out, hr.Response, hr.Reached, nil
}

// instanceSeq is a process-wide monotonic counter so each ephemeral instance gets
// a unique, non-random, deterministic-within-process id (no rand needed).
var instanceSeq atomic.Uint64

// newInstanceID derives a unique, filesystem-safe instance id from a backend name
// and the job's run id.
func newInstanceID(backend, runID string) string {
	n := instanceSeq.Add(1)
	return fmt.Sprintf("%s-%s-%d", backend, sanitizeID(runID), n)
}

// sanitizeID reduces a ref to a short, filesystem-safe token (a-z0-9-_), so an
// instance dir / jail name is always valid and non-sensitive.
func sanitizeID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= 48 {
			break
		}
	}
	if b.Len() == 0 {
		return "run"
	}
	return b.String()
}
