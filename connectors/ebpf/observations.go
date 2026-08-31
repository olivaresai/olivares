// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ebpf

import (
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// Observation vocabulary for the eBPF backstop. OriginKind is always "identity":
// the kernel attributes an access to a non-human runtime identity (process/cgroup/
// container), never to a resolved agent (see doc.go). The resource kinds are
// domain-qualified to parallel the SDK's examples ("postgres.table", "s3.bucket").
const (
	originIdentity = "identity"

	resFile = "file.path"
	resNet  = "net.endpoint"

	toolFilePermission = "security_file_permission"
	toolTCPConnect     = "tcp_connect"

	// findingAntiEvasion is the FindingReport.Kind this connector emits for the
	// anti-evasion gap marker. Consume edges and this finding kind.
	findingAntiEvasion = "anti_evasion"
)

// fileEdge builds the access edge for a kernel-observed file permission check: a
// workload identity touched a path, read/write classified from the MAY_* mask. It
// returns false when there is no path to attribute the access to. Confidence is
// Approximate: the access is kernel ground-truth, but the agent attribution is
// pending — see doc.go. The observation is returned by value.
func fileEdge(origin, filePath string, mask int, at time.Time) (model.EdgeObservation, bool) {
	if origin == "" || filePath == "" {
		return model.EdgeObservation{}, false
	}
	// Scrub the kernel-captured path before it becomes a persisted resource_ref: a
	// path component can embed a credential (a token in a /tmp download path built
	// from a credentialed URL, a key in a path segment). Every other file-bearing
	// connector scrubs the path (claude resource.go); the eBPF backstop must too,
	// or the §3 "redact before persist" invariant breaks for this source.
	return model.EdgeObservation{
		OriginKind:   originIdentity,
		OriginRef:    origin,
		ResourceKind: resFile,
		ResourceRef:  redact.Clean(filePath),
		Mode:         maskToMode(mask),
		Source:       model.SignalEBPF,
		Confidence:   model.ConfidenceApproximate,
		ToolRef:      toolFilePermission,
		ObservedAt:   at,
	}, true
}

// netEdge builds the access edge for a kernel-observed outbound connection: a
// workload identity reached a network endpoint. A TCP socket is bidirectional, so
// the mode is readwrite (the connection permits both directions may refine
// from flow data later). It returns false when the endpoint is unknown.
func netEdge(origin, endpoint string, at time.Time) (model.EdgeObservation, bool) {
	if origin == "" || endpoint == "" {
		return model.EdgeObservation{}, false
	}
	return model.EdgeObservation{
		OriginKind:   originIdentity,
		OriginRef:    origin,
		ResourceKind: resNet,
		ResourceRef:  endpoint,
		Mode:         model.ModeReadWrite,
		Source:       model.SignalEBPF,
		Confidence:   model.ConfidenceApproximate,
		ToolRef:      toolTCPConnect,
		ObservedAt:   at,
	}, true
}
