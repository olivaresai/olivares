// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import React, { useMemo } from 'react';
import {
  InfoCard,
  Progress,
  ResponseErrorPanel,
  StructuredMetadataTable,
} from '@backstage/core-components';
import { useApi } from '@backstage/core-plugin-api';
import { useEntity } from '@backstage/plugin-catalog-react';

import { olivaresApiRef } from '../../api/OlivaresApi';
import { useAsyncData } from '../../hooks/useAsyncData';
import { ToneStatus } from '../common/ToneStatus';
import { matchAgentToEntity } from './olivaresEntity';
import { summarizeGraph } from '../../api/transform';
import type { GraphResponse } from '../../api/types';

/**
 * EntityOlivaresCard is a compact overview card for an entity's page: the agent's
 * governance status and a one-line R/RW summary, for the platform engineer who
 * just wants the headline without opening the full tab. It degrades honestly — an
 * unmatched entity renders nothing (the card simply omits itself) rather than a
 * misleading empty governance panel.
 */
export const EntityOlivaresCard = () => {
  const { entity } = useEntity();
  const api = useApi(olivaresApiRef);

  const agentsState = useAsyncData(() => api.agents({ limit: 500 }), []);
  const agent = useMemo(
    () => (agentsState.value ? matchAgentToEntity(entity, agentsState.value.items) : undefined),
    [agentsState.value, entity],
  );

  const graphState = useAsyncData<GraphResponse>(async () => {
    if (!agent) return { nodes: [], edges: [], has_more: false };
    return api.accessGraph({ origin_id: agent.id, limit: 200 });
  }, [agent?.id]);

  if (agentsState.error) {
    return <ResponseErrorPanel error={agentsState.error} />;
  }
  if (agentsState.loading && !agentsState.value) {
    return (
      <InfoCard title="Olivares">
        <Progress />
      </InfoCard>
    );
  }
  // No matching agent: render nothing so the card never clutters an unrelated page.
  if (!agent) {
    return null;
  }

  const g = graphState.value ? summarizeGraph(graphState.value) : undefined;

  return (
    <InfoCard title="Olivares governance">
      <StructuredMetadataTable
        metadata={{
          Status: <ToneStatus tone="ok">{agent.status}</ToneStatus>,
          Kind: agent.kind || '—',
          'R/RW access': g ? (
            <ToneStatus tone={g.writeEdgeCount > 0 ? 'warning' : 'ok'}>
              {g.edgeCount} edges · {g.writeEdgeCount} write
            </ToneStatus>
          ) : (
            '…'
          ),
        }}
      />
    </InfoCard>
  );
};
