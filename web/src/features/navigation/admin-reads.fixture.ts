// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The administrator's reads (adminReadPerms in core/auth/permission.go): the organization's
// governance and administration reads, granted from admin up and never by the read or write
// tier. An editor holds none of them; a test makes an editor's permission set by taking these
// out of the set it would otherwise hold.
export const ADMIN_READS: ReadonlySet<string> = new Set([
  // The audit record.
  'audit:read',
  'security:integrity:read',
  // Rules and policy.
  'governance:policy:read',
  'governance:claude-policy:read',
  'governance:guardian:read',
  'governance:routine:read',
  'governance:rbac:read',
  'governance:breakglass:read',
  'inferenceproxy:dlp:read',
  'knowledge:dlp:read',
  // Security findings.
  'security:finding:read',
  'security:anomaly:read',
  'security:case:read',
  'governance:agent-risk:read',
  // Other people's usage and money.
  'finops:spend:read',
  'finops:budget:read',
  'adoption:metrics:read',
  'adoption:developer:read',
  // Reads only administrator places use: connector health, the access map, the inference
  // proxy's settings, evaluation runs, retention, the attestation record, webhooks, alerts.
  'health:status:read',
  'accessmap:graph:read',
  'inferenceproxy:config:read',
  'evals:run:read',
  'compliance:retention:read',
  'observability:attestation:read',
  'eventing:subscription:read',
  'notify:route:read',
])
