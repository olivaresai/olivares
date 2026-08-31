// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ebpf

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestFileEdge(t *testing.T) {
	at := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	e, ok := fileEdge("host/cat", "/etc/passwd", mayRead, at)
	require.True(t, ok)
	assert.Equal(t, originIdentity, e.OriginKind)
	assert.Equal(t, "host/cat", e.OriginRef)
	assert.Equal(t, resFile, e.ResourceKind)
	assert.Equal(t, "/etc/passwd", e.ResourceRef)
	assert.Equal(t, model.ModeRead, e.Mode)
	assert.Equal(t, model.SignalEBPF, e.Source)
	assert.Equal(t, model.ConfidenceApproximate, e.Confidence)
	assert.Equal(t, toolFilePermission, e.ToolRef)
	assert.Equal(t, at, e.ObservedAt)
	assert.Equal(t, model.ObsEdge, e.ObservationType())

	_, ok = fileEdge("host/cat", "", mayRead, at)
	assert.False(t, ok, "no path, no edge")
	_, ok = fileEdge("", "/etc/passwd", mayRead, at)
	assert.False(t, ok, "no origin, no edge")
}

// TestFileEdgeScrubsSecretInPath is the adversarial data-minimization check: a
// kernel-captured path that embeds a credential must be scrubbed before it
// becomes the persisted resource_ref (docs/SECURITY-HARDENING.md). Before the fix the raw path
// landed verbatim in the access-graph metadata.
func TestFileEdgeScrubsSecretInPath(t *testing.T) {
	at := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	secret := "AKIAIOSFODNN7EXAMPLE"
	e, ok := fileEdge("host/curl", "/tmp/dl/aws_access_key_id="+secret+"/blob", mayRead, at)
	require.True(t, ok)
	assert.NotContains(t, e.ResourceRef, secret, "secret in path must be redacted before persistence")
	assert.Contains(t, e.ResourceRef, "/tmp/dl/", "non-secret path structure is preserved")
}

func TestNetEdge(t *testing.T) {
	at := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	e, ok := netEdge("host/curl", "tcp://example.com:443", at)
	require.True(t, ok)
	assert.Equal(t, originIdentity, e.OriginKind)
	assert.Equal(t, resNet, e.ResourceKind)
	assert.Equal(t, "tcp://example.com:443", e.ResourceRef)
	assert.Equal(t, model.ModeReadWrite, e.Mode, "a socket is bidirectional")
	assert.Equal(t, model.SignalEBPF, e.Source)
	assert.Equal(t, toolTCPConnect, e.ToolRef)

	_, ok = netEdge("host/curl", "", at)
	assert.False(t, ok)
}
