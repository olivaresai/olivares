// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import React from 'react';
import {
  StatusAborted,
  StatusError,
  StatusOK,
  StatusPending,
  StatusWarning,
} from '@backstage/core-components';

import type { Tone } from '../../api/transform';

/**
 * ToneStatus renders a semantic tone as the matching Backstage Status dot+label,
 * so every state across the plugin reads consistently and accessibly (the Status
 * components carry an aria role). The tone→component mapping is the single place
 * the honesty rules surface visually: `pending` is the amber dot used for
 * reconciliation-pending drift (never the red error dot).
 */
export const ToneStatus = ({
  tone,
  children,
}: {
  tone: Tone;
  children: React.ReactNode;
}) => {
  switch (tone) {
    case 'ok':
      return <StatusOK>{children}</StatusOK>;
    case 'warning':
      return <StatusWarning>{children}</StatusWarning>;
    case 'error':
      return <StatusError>{children}</StatusError>;
    case 'pending':
      return <StatusPending>{children}</StatusPending>;
    default:
      return <StatusAborted>{children}</StatusAborted>;
  }
};
