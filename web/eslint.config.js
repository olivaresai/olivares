// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import eslintConfigPrettier from 'eslint-config-prettier'
import { defineConfig, globalIgnores } from 'eslint/config'
import noRawPalette from './eslint-rules/no-raw-palette.js'

export default defineConfig([
  // dist (build output) + coverage + generated API types are never linted.
  globalIgnores(['dist', 'coverage', 'src/lib/api/openapi.gen.ts']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      tseslint.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
      // Disable ESLint rules that conflict with Prettier; must come last.
      eslintConfigPrettier,
    ],
    languageOptions: {
      globals: globals.browser,
    },
    rules: {
      // Fast-refresh hygiene is a DX hint, not correctness: a provider file that
      // co-locates its hook (the idiomatic React Context pattern) is fine. Keep
      // it as a warning so it never fails CI but still surfaces accidental mixes.
      'react-refresh/only-export-components': 'warn',
      // Allow intentional `_`-prefixed unused (e.g. destructure-to-omit).
      '@typescript-eslint/no-unused-vars': [
        'error',
        {
          argsIgnorePattern: '^_',
          varsIgnorePattern: '^_',
          ignoreRestSiblings: true,
        },
      ],
    },
  },
  // Feature views must color through the DTCG semantic tokens (ADM-CORE-04): raw
  // Tailwind palette classes bypass theming AND the AT contrast gate.
  {
    files: ['src/features/**/*.{ts,tsx}'],
    plugins: { olivares: { rules: { 'no-raw-palette': noRawPalette } } },
    rules: { 'olivares/no-raw-palette': 'error' },
  },
  // TODO: temporary allowlist — these six files already used the raw
  // palette when the rule landed, and all of them live in dirs a parallel Codex
  // session is rewriting for i18n (api-playground/, backups/). Migrate them to
  // semantic token classes after that session merges, then DELETE this block.
  {
    files: [
      'src/features/api-playground/endpoint-tree.tsx',
      'src/features/api-playground/request-history.tsx',
      'src/features/api-playground/request-panel.tsx',
      'src/features/api-playground/response-panel.tsx',
      'src/features/backups/pending-restores.tsx',
      'src/features/backups/restore-dialog.tsx',
    ],
    rules: { 'olivares/no-raw-palette': 'off' },
  },
  // Node-context files (build config, Playwright e2e, scripts) get node globals.
  {
    files: ['*.config.{ts,js}', 'e2e/**/*.{ts,tsx}', 'scripts/**/*.{ts,js}'],
    languageOptions: {
      globals: { ...globals.node },
    },
    rules: {
      'react-refresh/only-export-components': 'off',
    },
  },
])
