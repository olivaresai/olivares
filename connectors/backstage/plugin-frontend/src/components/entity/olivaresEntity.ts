// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Framework-free helpers that tie a Backstage catalog entity back to its Olivares
// control-plane record, so the entity tab/card can show an agent's live sessions
// and R/RW access without the components owning any matching logic. The matching
// mirrors the catalog entity provider (../../README.md): it publishes an Olivares
// agent as a Component named `slug(agent.name)` and annotates it with
// `olivares.ai/external-id` and `olivares.ai/managed`.

import type { AgentDTO, LiveDTO } from '../../api/types';

/** The Olivares annotations the catalog entity provider stamps onto entities. */
export const ANNOTATION_MANAGED = 'olivares.ai/managed';
export const ANNOTATION_EXTERNAL_ID = 'olivares.ai/external-id';
export const ANNOTATION_STATUS = 'olivares.ai/status';

/**
 * EntityLike is the minimal structural slice of a Backstage `Entity` these
 * helpers read. A real `@backstage/catalog-model` Entity satisfies it, so the
 * components pass the real entity straight through — but this file stays free of
 * any Backstage import and is unit-testable with a plain object.
 */
export interface EntityLike {
  metadata?: {
    name?: string;
    namespace?: string;
    annotations?: Record<string, string>;
  };
  spec?: Record<string, unknown>;
}

/** annotation reads one annotation value, tolerant of a missing metadata block. */
function annotation(entity: EntityLike, key: string): string | undefined {
  return entity.metadata?.annotations?.[key];
}

/**
 * isOlivaresEntity is true when an entity was published/annotated by Olivares —
 * either explicitly managed or carrying an Olivares external id. The entity tab
 * and card mount only for these, so a plain catalog entity is never cluttered.
 */
export function isOlivaresEntity(entity: EntityLike): boolean {
  return (
    annotation(entity, ANNOTATION_MANAGED) === 'true' ||
    !!annotation(entity, ANNOTATION_EXTERNAL_ID)
  );
}

/** olivaresExternalId returns the agent's control-plane external id, if annotated. */
export function olivaresExternalId(entity: EntityLike): string | undefined {
  return annotation(entity, ANNOTATION_EXTERNAL_ID);
}

/**
 * slug mirrors the entity provider's name slug (OlivaresEntityProvider.slug) so a
 * Component name can be matched back to an agent name when no external id is set.
 */
export function slug(value: string): string {
  const s = value
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 63);
  return s || 'unknown';
}

/**
 * matchAgentToEntity finds the control-plane agent an entity represents. It
 * prefers the firm key (the `olivares.ai/external-id` annotation === agent
 * external_id) and falls back to the slugged name (entity name === slug(agent
 * name)) for entities scaffolded without an external id. Returns undefined when
 * nothing matches — the caller then shows an honest "not linked" state rather
 * than guessing.
 */
export function matchAgentToEntity(
  entity: EntityLike,
  agents: AgentDTO[],
): AgentDTO | undefined {
  const ext = olivaresExternalId(entity);
  if (ext) {
    const byExt = agents.find(a => a.external_id && a.external_id === ext);
    if (byExt) {
      return byExt;
    }
  }
  const name = entity.metadata?.name;
  if (name) {
    return agents.find(a => slug(a.name) === name);
  }
  return undefined;
}

/**
 * filterSessionsForAgent keeps the live sessions whose `agent_ref` points at the
 * given agent. `agent_ref` is an opaque reference, so we accept a match against
 * the agent's external id, name or id — whichever the connector emitted. An agent
 * with no resolvable reference yields an empty list (never every session).
 */
export function filterSessionsForAgent(
  sessions: LiveDTO[],
  agent: AgentDTO,
): LiveDTO[] {
  const refs = new Set(
    [agent.external_id, agent.name, agent.id].filter(
      (r): r is string => !!r && r.length > 0,
    ),
  );
  if (refs.size === 0) {
    return [];
  }
  return sessions.filter(s => s.agent_ref !== undefined && refs.has(s.agent_ref));
}
