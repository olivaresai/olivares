// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Wire shapes of the skills catalog (modules/skills/catalog.go, assignment.go).

export interface SkillPack {
  id: string
  name: string
  state: 'enabled' | 'retired' | (string & {})
  latest_revision_id: string
  latest_revision: number
  version: number
  created_at: string
  updated_at: string
}

export interface SkillMember {
  name: string
  directory: string
  description: string
}

export interface SkillRevision {
  id: string
  pack_id: string
  number: number
  source: { kind: string; origin?: string; resolved_commit?: string }
  members: SkillMember[]
  created_at: string
}

export interface SkillPackDetail {
  pack: SkillPack
  revisions: SkillRevision[]
  revisions_cursor?: string
  has_more_revisions: boolean
}

/** Engine target kinds; a workspace is the department until departments have their own node. */
export type SkillTargetKind =
  'workspace' | 'template' | 'agent_group' | 'agent' | 'session'

export interface SkillAssignment {
  id: string
  target_kind: SkillTargetKind
  target_id: string
  pack_id: string
  pack_revision_id: string
  members: string[] | null
  version: number
}
