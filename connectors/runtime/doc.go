// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package runtime is the read-only runtime-inventory SourceConnector: it
// discovers WHERE AI workloads run and emits the containment/topology of that
// runtime so the consumer module can materialize a runtime inventory.
//
// It runs three independent, config-gated sub-discoverers in one batch pass:
//
//   - linux: walks a procfs (proc_root) and, for processes whose program or
//     script basename matches an AI-tool pattern, emits a host->process
//     containment edge (and a container->process edge when the process is in a
//     container, detected from its cgroup). It reads only public /proc fields —
//     comm, the cmdline program/script basename (for matching only), the status
//     "Uid:" line, and cgroup — and NEVER reads /proc/<pid>/environ, never emits
//     a full command line.
//   - docker: talks to the Docker daemon over its unix socket with read-only GET
//     calls (/version, /info, /containers/json, /images/json, /networks) and
//     emits docker.host->container and container->container.image edges. It never
//     reads or emits container Env or labels.
//   - k8s: talks to the Kubernetes API over HTTPS with read-only GETs (nodes,
//     namespaces, pods, deployments) using a bearer ServiceAccount token, and
//     emits cluster->node, node->pod, namespace->deployment and pod->image edges.
//     It never reads secrets, configmaps or pod env.
//
// Every emitted edge is a CONTAINMENT/TOPOLOGY edge, not an access:
// Mode=ModeUnknown, Source=signalRuntime, Confidence=ConfidenceAttributed (we
// observed the relationship directly via the API/procfs). The SDK observation
// sum type has no "entity" kind, so entities are named only by the edges' natural
// refs; the consumer materializes them. This connector issues ONLY read/list/
// describe calls, persists only identifiers + classification, and routes every
// reference through the redact helper so no secret, env var, token or payload can
// leak into an emitted ref or finding (docs/SECURITY-HARDENING.md).
//
// When an ENABLED, PRESENT target cannot be reached or listed, the connector
// emits a "health" FindingReport (with the error detail hashed, never raw) and
// continues — a gap is a signal, not silence. A target that is simply absent or
// unconfigured (no proc_root, no docker socket on disk, no k8s connection) is
// skipped silently.
//
// Minimum privilege:
//   - linux: no root; reads only public /proc fields; never /proc/<pid>/environ.
//   - docker: read access to the docker socket (docker-group membership);
//     read-only daemon API calls only.
//   - k8s: a read-only ServiceAccount bound to a ClusterRole granting get/list on
//     nodes, namespaces, pods and apps/v1 deployments — nothing else.
package runtime
