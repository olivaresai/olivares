// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Agent-artifact supply-chain DTOs. Registry responses and write commands stay
// separate so server-set identity/attestation fields can never leak into POST.
import type { AibomSeal, AibomSealReceipt } from '@/features/models/types'

export type AgentArtifactClass =
  'skill' | 'mcpb_extension' | 'mcp_app_template' | 'agents_md'

export type PostureGrade = 'A' | 'B' | 'C' | 'D' | 'F'

export interface AgentArtifact {
  id: string
  artifact_class: AgentArtifactClass
  name: string
  version?: string
  provenance?: string
  source_ref?: string
  content_hash?: string
  content_alg?: string
  posture_grade?: PostureGrade
  posture_issues: number
  posture_scanned: boolean
  verified: boolean
  attested_by?: string
  attested_at?: string
  note?: string
}

/** Writable fields accepted by POST /agent-artifacts. A posture grade and scan
 * state are derived from one selector in the UI, making invalid combinations
 * unrepresentable. */
export interface AgentArtifactInput {
  artifact_class: AgentArtifactClass
  name: string
  version?: string
  provenance?: string
  source_ref?: string
  content_hash?: string
  content_alg?: string
  posture_grade?: PostureGrade
  posture_issues: number
  posture_scanned: boolean
  verified: boolean
  note?: string
}

// The agent and per-model seal ledgers deliberately share this frozen DTO shape.
export type { AibomSeal, AibomSealReceipt }
