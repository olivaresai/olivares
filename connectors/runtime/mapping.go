// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// signalRuntime is this connector's signal source. The consumer reads
// edges by signal_source to build the runtime inventory; a runtime topology edge
// carries very different meaning from a pgAudit READ, so it gets its own source.
const signalRuntime = model.SignalSource("runtime")

// Origin and resource kinds emitted by the three discoverers. These are
// containment/topology relationships (a host contains a process, a node runs a
// pod, an account owns an image) — NOT R/RW accesses; the consumer materializes
// the named entities from the edges' refs.
const (
	// linux discoverer.
	originHost = "host"
	resProcess = "process"

	// docker discoverer.
	originDockerHost = "docker.host"
	resContainer     = "container"
	resContainerImg  = "container.image"

	// container is both a resource (host->container) and an origin
	// (container->process, container->container.image).
	originContainer = "container"

	// k8s discoverer.
	originK8sCluster   = "k8s.cluster"
	originK8sNode      = "k8s.node"
	originK8sNamespace = "k8s.namespace"
	originK8sPod       = "k8s.pod"
	resK8sNode         = "k8s.node"
	resK8sPod          = "k8s.pod"
	resK8sDeployment   = "k8s.deployment"
)

// containmentEdge builds one topology/containment edge with this connector's
// shared provenance: ModeUnknown (a containment is not an access),
// signalRuntime, and ConfidenceAttributed (directly observed via API/procfs).
// Keeping every discoverer on this one helper guarantees the three of them stay
// consistent in Mode/Source/Confidence — the property the consumer relies on.
func containmentEdge(originKind, originRef, resourceKind, resourceRef, toolRef string, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   originKind,
		OriginRef:    originRef,
		ResourceKind: resourceKind,
		ResourceRef:  resourceRef,
		Mode:         model.ModeUnknown,
		Source:       signalRuntime,
		Confidence:   model.ConfidenceAttributed,
		ToolRef:      toolRef,
		ObservedAt:   at,
	}
}

// hostProcessEdge: a host contains a matched AI process.
func hostProcessEdge(host, procRef, pattern string, at time.Time) model.EdgeObservation {
	return containmentEdge(originHost, host, resProcess, procRef, pattern, at)
}

// containerProcessEdge: a container contains a matched AI process.
func containerProcessEdge(containerID, procRef string, at time.Time) model.EdgeObservation {
	return containmentEdge(originContainer, containerID, resProcess, procRef, "", at)
}

// dockerContainerEdge: a docker host runs a container (toolRef = its image).
func dockerContainerEdge(dockerHost, containerRef, image string, at time.Time) model.EdgeObservation {
	return containmentEdge(originDockerHost, dockerHost, resContainer, containerRef, image, at)
}

// containerImageEdge: a container is built from a container image.
func containerImageEdge(containerRef, image string, at time.Time) model.EdgeObservation {
	return containmentEdge(originContainer, containerRef, resContainerImg, image, "", at)
}

// k8sNodeEdge: a cluster owns a node.
func k8sNodeEdge(cluster, node string, at time.Time) model.EdgeObservation {
	return containmentEdge(originK8sCluster, cluster, resK8sNode, node, "", at)
}

// k8sPodEdge: a node runs a pod (toolRef = its first container image).
func k8sPodEdge(node, podRef, image string, at time.Time) model.EdgeObservation {
	return containmentEdge(originK8sNode, node, resK8sPod, podRef, image, at)
}

// k8sDeploymentEdge: a namespace owns a deployment.
func k8sDeploymentEdge(namespace, deployRef string, at time.Time) model.EdgeObservation {
	return containmentEdge(originK8sNamespace, namespace, resK8sDeployment, deployRef, "", at)
}

// k8sPodImageEdge: a pod is built from a container image.
func k8sPodImageEdge(podRef, image string, at time.Time) model.EdgeObservation {
	return containmentEdge(originK8sPod, podRef, resContainerImg, image, "", at)
}

// healthFinding reports an ENABLED, PRESENT target that could not be reached or
// listed. The error detail is hashed, never embedded, so a URL or token in an
// error message is never persisted (docs/SECURITY-HARDENING.md).
func healthFinding(subjectKind, subjectRef, title string, err error, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        "health",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectKind,
		SubjectRef:  subjectRef,
		Title:       title,
		DetailHash:  redact.Hash(err.Error()),
		OccurredAt:  at,
	}
}
