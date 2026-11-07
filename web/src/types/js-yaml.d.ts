// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
declare module 'js-yaml' {
  export const JSON_SCHEMA: object
  export function load(text: string, options: { schema: object }): unknown
}
