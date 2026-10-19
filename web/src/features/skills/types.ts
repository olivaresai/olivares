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
  /** Absent for portable skills; other names are qualified by kind. */
  kind?: 'agent' | 'command' | 'rule'
  name: string
  directory: string
  description: string
  /** What its tool acts on when it loads (hooks, shell interpolation, a tool
   * pre-approval, a plugin or settings file); pinning it needs approval. */
  active_content?: string[]
  scripts?: string[]
}

export interface SkillRevision {
  id: string
  pack_id: string
  number: number
  source: {
    kind: string
    origin?: string
    resolved_commit?: string
    subdir?: string
    source_digest?: string
  }
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
  | 'workspace'
  | 'profile'
  | 'folder'
  | 'template'
  | 'agent_group'
  | 'agent'
  | 'session'

export interface SkillAssignment {
  id: string
  target_kind: SkillTargetKind
  target_id: string
  pack_id: string
  pack_revision_id: string
  members: string[] | null
  version: number
}

export interface BuiltinPack {
  id: string
  origin: string
  resolved_commit: string
  source_digest: string
}
export interface SkillInstallRequest {
  name: string
  source:
    | { kind: 'builtin'; ref: string }
    | { kind: 'git'; url: string; ref: string; subdir: string }
}
export interface SkillInstallResult {
  state: string
  pack: SkillPack
  revision: SkillRevision
}

/** One skill a new conversation receives (modules/skills SelectedMember). */
export interface SkillSelectedMember {
  /** Absent for portable skills; other names are qualified by kind. */
  kind?: 'agent' | 'command' | 'rule'
  pack_id: string
  pack_revision_id: string
  name: string
  directory: string
  /** The broader targets' skills of this name that it replaced. */
  overrides?: Array<{
    pack_id: string
    pack_revision_id: string
    target_kind: SkillTargetKind
    target_id: string
  }>
}

/** The skills a launch pins for a target (modules/skills Selection). */
export interface SkillSelection {
  digest: string
  members: SkillSelectedMember[]
}

/** One skill an update adds, removes or changes (modules/skills MemberChange). */
export interface SkillMemberChange {
  /** Absent for portable skills; other names are qualified by kind. */
  kind?: 'agent' | 'command' | 'rule'
  name: string
  change: 'added' | 'changed' | 'removed'
  description?: string
  active_content?: string[]
  new_active_content?: string[]
}

/** One file that differs between two revisions (modules/skills FileChange). */
export interface SkillFileChange {
  path: string
  change: 'added' | 'changed' | 'removed'
  diff?: string
  diff_omitted?: string
}

export interface SkillPackDiff {
  members: SkillMemberChange[]
  files: SkillFileChange[]
}

/** POST /packs/{id}/update without apply: the recorded source read again. */
export interface SkillUpdateCheck {
  state: 'current' | 'available'
  pack: SkillPack
  current: { revision_id: string; number: number; resolved_commit?: string }
  /** Where the candidate was read from: the origin, the ref and the directory. */
  source?: {
    kind: string
    origin?: string
    requested_ref?: string
    subdir?: string
  }
  candidate?: { manifest_digest: string; resolved_commit?: string }
  diff?: SkillPackDiff
}
