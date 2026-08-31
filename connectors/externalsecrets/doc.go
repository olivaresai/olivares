// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package externalsecrets is the Olivares AI connector for the External Secrets
// Operator (ESO). It OBSERVES the ESO CRDs an operator already
// keeps under GitOps — it never talks to ESO, to Kubernetes, or to any backend
// secret store, never resolves a remote key, and never reads a secret VALUE.
//
// It is read-first (docs/SECURITY-HARDENING.md): the operator EXPORTS its ESO manifests to a file
// or directory of Kubernetes YAML/JSON (the ExternalSecret/SecretStore/
// ClusterSecretStore objects) and this connector parses them. ESO declares which
// Kubernetes Secret is hydrated from which backend key in which store, so the
// manifests carry exactly the provisioning topology — and nothing else. A CRD
// manifest contains key NAMES (spec.data[].remoteRef.key), never the key's value;
// there is no secret material in the manifest at all, and a negative test asserts
// the connector never emits one.
//
// Provisioning edges (Gather). For every ExternalSecret, this connector resolves
// the SecretStore/ClusterSecretStore it references and emits ONE
// model.EdgeObservation per data/dataFrom entry:
//
//	OriginKind "identity"  OriginRef eso.store:<StoreKind>:<ns|cluster>:<name>
//	ResourceKind "k8s.secret"  ResourceRef <namespace>/<targetSecretName>
//	Mode write  Source "eso"  Confidence attributed  ToolRef <remote backend key>
//
// — "this store PROVISIONS (writes) this Kubernetes Secret, hydrated from that
// backend key". The store is the origin (an NHI custodian), the materialized
// Kubernetes Secret is the resource, the Mode is write (ESO creates/updates the
// Secret), and ToolRef names WHICH backend key feeds it. The remote key is a
// reference, never a value. An ExternalSecret with neither spec.data nor
// spec.dataFrom, or one whose store reference does not resolve, emits nothing — it
// is skipped, never guessed (ARCHITECTURE.md).
//
// Inventory. Snapshot exposes every SecretStore and ClusterSecretStore
// the manifests declare as a secret_store NHI — the "where secrets live" half of the
// unified secret-manager inventory. Each store's Ref uses the SAME format as the
// edge OriginRef, so a store the inventory lists and a store an edge attributes
// converge on one node. The Attributes carry the single backend provider key the
// store wraps (vault | aws | gcpsm | azurekv | kubernetes) so governance can see
// which estate-external custodian ESO is fronting. With no configured path Snapshot
// returns an empty graph (offline), no error — exactly like awskms.
//
// It imports only the SDK, the Apache identitysource/internal helpers, gopkg.in/
// yaml.v3 and the standard library — never the engine (/core).
package externalsecrets
