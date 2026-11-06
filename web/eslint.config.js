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
  // The console colors through the DTCG semantic tokens (ADM-CORE-04): a raw palette
  // class, an arbitrary color value or a bare color string paints the same in Light,
  // Dark and Black and escapes the contrast tests. Every source file; not the
  // tests, the test setup or the dev-only components page. The rule reads .ts and .tsx
  // only: the one literal color in the CSS outside the token files is the print sheet's
  // white paper (src/index.css, @media print), which stays white in every theme by design.
  {
    files: ['src/**/*.{ts,tsx}'],
    ignores: ['src/**/*.test.{ts,tsx}', 'src/test/**', 'src/dev/**'],
    plugins: { olivares: { rules: { 'no-raw-palette': noRawPalette } } },
    rules: { 'olivares/no-raw-palette': 'error' },
  },
  // A tab strip or an open sheet is a place in the address, not component memory: a reload,
  // Back and a pasted link must restore it (useTabParam / useUrlState). Uncontrolled
  // `<Tabs defaultValue>` and a one-time read of `window.location.search` are how ~14 views
  // lost theirs. A genuinely ephemeral strip with nothing to restore into carries an
  // `eslint-disable` with the reason.
  {
    files: ['src/features/**/*.{ts,tsx}'],
    ignores: ['src/features/**/*.test.{ts,tsx}'],
    rules: {
      'no-restricted-syntax': [
        'error',
        {
          selector:
            "JSXOpeningElement[name.name='Tabs'] > JSXAttribute[name.name='defaultValue']",
          message:
            'Tabs must not keep the tab in memory: use useTabParam (lib/hooks/use-tab-param) so reload, Back and a pasted URL restore it.',
        },
        {
          selector:
            "NewExpression[callee.name='URLSearchParams'] > MemberExpression.arguments[property.name='search']",
          message:
            'Do not read window.location.search once: use useUrlState / useTabParam so the view follows the address (Back, a second link) and writes its state back.',
        },
      ],
    },
  },
  // The API playground's request and response panes are the editor of one unsaved request:
  // their sub-tabs (params, headers, body) have no address to restore into.
  {
    files: [
      'src/features/api-playground/request-panel.tsx',
      'src/features/api-playground/response-panel.tsx',
    ],
    rules: { 'no-restricted-syntax': 'off' },
  },
  // The colors that must stay literal, each for its reason.
  {
    files: [
      // The theme previews paint each theme's own colors, whichever theme is active.
      'src/app/pages/settings.tsx',
      // The chart fallback before CSS resolves, pinned to the dark tokens by tokens.test.ts.
      'src/components/charts/chart-theme.ts',
      // WebGL cannot read CSS variables; this fallback mirrors chart-theme's.
      'src/features/shared/graph/sigma-graph-build.ts',
      // The five account hues, the same in every theme by design.
      'src/components/ui/mono-mark.tsx',
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
