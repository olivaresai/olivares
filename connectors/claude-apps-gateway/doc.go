// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package claudeappsgateway inventories an existing Claude apps gateway deployment,
// reports posture from its gateway.yaml, and ingests its JSONL audit events.
//
// The connector observes metadata only: gateway topology, IdP/upstream/OTLP references,
// declared model grants, audit event categories, pseudonymous OIDC sub values, and
// counts. It never emits prompt bodies, emails, telemetry headers, JWTs, passwords, or
// raw protocol documents. Sensitive details are represented by stable SHA-256 hashes.
//
// This connector is deliberately "and, not or": it does not replace the gateway,
// the Claude OTLP receiver, managed-settings governance, or the inference proxy. It
// makes an already deployed gateway visible to the Olivares control plane alongside
// those surfaces.
package claudeappsgateway
