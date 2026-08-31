// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
// @ts-check
import { defineConfig, fontProviders } from 'astro/config'
import starlight from '@astrojs/starlight'
import starlightOpenAPI, { createOpenAPISidebarGroup } from 'starlight-openapi'
import starlightVersions from 'starlight-versions'
import { localizeSidebar } from './src/sidebar-i18n.mjs'
import { LOCALES, VERSIONS } from './src/site-locales.mjs'

// The REST reference is generated at build time DIRECTLY from the product's own
// OpenAPI 3.1 contract (web/openapi/openapi.json, authored by core/api).
// We do not keep a copy: starlight-openapi reads the real file, so the rendered
// reference cannot drift from the served contract. scripts/check-spec-drift.mjs
// asserts this wiring stays true.
const OPENAPI_SCHEMA = '../web/openapi/openapi.json'

// The BETA module-route contract (web/openapi/openapi.beta.json, authored by
// core/api — reflected from the routes the modules register). Rendered alongside
// the stable reference under a "beta, may change" banner (the document's own
// info.description / x-beta-notice). Distinct file, distinct base — the stable
// 24-path contract stays identifiable and intact.
const OPENAPI_BETA_SCHEMA = '../web/openapi/openapi.beta.json'

// A placeholder so the generated REST reference nests under "Reference", keeping
// the Diátaxis information architecture intact instead of a floating top group.
const apiSidebarGroup = createOpenAPISidebarGroup()
const apiBetaSidebarGroup = createOpenAPISidebarGroup()

// https://astro.build/config
export default defineConfig({
  site: 'https://docs.olivares.ai',
  // Deterministic, reproducible static output; no SSR, no backend, no phone-home.
  output: 'static',
  trailingSlash: 'ignore',

  // THE BRAND'S TYPE, SELF-HOSTED AND METRIC-MATCHED.
  //
  // Until 2026-08-29 this site loaded NO font at all: brand.css declared
  // `--sl-font-mono: 'JetBrains Mono'` while nothing shipped the family, and `--sl-font` was
  // never declared, so Starlight fell through to `--sl-font-system`
  // (@astrojs/starlight/style/props.css:92-98) and docs.olivares.ai rendered in the visitor's
  // operating-system font — text AND code — while the product it documents renders in Inter.
  // Measured on the built artifact: `find dist -name '*.woff2'` returned 0.
  //
  // Why the fonts pipeline rather than importing the @fontsource CSS directly: importing it
  // works, and it cost CLS 0.2943 on a cold first visit — measured, one 0.2943 shift at 383 ms
  // on the quickstart's <ol>, immediately after the faces landed at ~275 ms. That is the swap
  // reflowing the page because the fallback's metrics differ. `optimizedFallbacks` derives a
  // metric-matched fallback face (capsize), so the swap costs no layout.
  //
  // ⛔ This sentence used to end "...and Astro preloads what the page uses". That is FALSE:
  // astro/components/Font.astro:13 destructures `preload = false`, so <Font> emits no preload link
  // unless asked, and src/components/Head.astro deliberately does not ask (the reason is measured
  // there). The fix is `optimizedFallbacks` alone; nothing here preloads anything. Doing that by hand would mean hand-authoring size-adjust numbers, which is
  // exactly the free-hand value the brand pipeline refuses.
  //
  // `provider: npm({ remote: false })` resolves the faces from the @fontsource-variable
  // packages already installed in node_modules — the SAME packages and the same ^5.3.0 the
  // console pins (web/package.json:36-38, imported at web/src/main.tsx:12-14). `remote: false`
  // means the build never reaches a CDN: no Google Fonts, no jsDelivr, nothing fetched at build
  // or at render. Parsing the package's own index.css also keeps every subset it ships with its
  // unicode-range, so the Cyrillic of the /ru locale still resolves.
  fonts: [
    {
      provider: fontProviders.npm({ remote: false }),
      name: 'Inter Variable',
      cssVariable: '--olv-font-sans',
      options: { package: '@fontsource-variable/inter' },
      optimizedFallbacks: true,
      fallbacks: ['system-ui', '-apple-system', 'Segoe UI', 'Roboto', 'Helvetica', 'Arial', 'sans-serif'],
    },
    {
      provider: fontProviders.npm({ remote: false }),
      name: 'JetBrains Mono Variable',
      cssVariable: '--olv-font-mono',
      options: { package: '@fontsource-variable/jetbrains-mono' },
      optimizedFallbacks: true,
      fallbacks: ['ui-monospace', 'SFMono-Regular', 'Menlo', 'Consolas', 'Liberation Mono', 'monospace'],
    },
    {
      provider: fontProviders.npm({ remote: false }),
      name: 'Space Grotesk Variable',
      cssVariable: '--olv-font-display',
      options: { package: '@fontsource-variable/space-grotesk' },
      optimizedFallbacks: true,
      fallbacks: ['Inter Variable', 'system-ui', 'sans-serif'],
    },
  ],
  integrations: [
    starlight({
      title: 'Olivares AI docs',
      description:
        'Product documentation for Olivares AI — integrate, manage and secure the AI in your enterprise, multi-provider by design: Claude Code at the deepest level, Codex and Grok Build alongside. See what each agent and model can reach, the resources it reads and writes, and prove it. Self-hosted, vendor-neutral, audit-ready.',
      // The Olivares "Ledger O" mark (brandv4) beside the title. Theme-aware so the
      // ink follows Starlight's light/dark toggle; the flagged-WRITE row stays brand
      // orange in both. Favicon is the same mark, adaptive to the browser chrome.
      favicon: '/favicon.svg',
      logo: {
        light: './src/assets/olivares-mark-light.svg',
        dark: './src/assets/olivares-mark-dark.svg',
        replacesTitle: false,
      },
      // English at the root, with six machine-translated locales. Every
      // non-root page carries a "machine-translated — English is authoritative,
      // native review pending" notice (src/components/Banner.astro); any page not
      // yet translated falls back to English automatically (no silent drift). The
      // `lang` is the BCP-47 tag Starlight uses for its built-in UI strings.
      // The map itself lives in src/site-locales.mjs so every consumer reads the
      // SAME declaration instead of each re-deriving it (a text scan of this file
      // and a hardcoded list respectively — both provably wrong on legal configs;
      // see that file's header). The ADR publisher WAS one of those consumers and
      // was withdrawn on 2026-08-25: architecture decision records are internal
      // development documentation and no longer ship on a public surface.
      defaultLocale: 'root',
      locales: LOCALES,
      // Override the Banner to render the machine-translation honesty notice on
      // translated/fallback pages (see src/components/Banner.astro).
      components: {
        Banner: './src/components/Banner.astro',
        // Starlight builds its own <head> and never renders Astro's <Font>, so the fonts
        // pipeline emits no @font-face without this override. See src/components/Head.astro.
        Head: './src/components/Head.astro',
      },
      // Pagefind: local, client-side search built into Starlight. No external
      // search service — consistent with the self-hosted / data-stays-home ADN.
      pagefind: true,
      head: [
        {
          tag: 'meta',
          attrs: { property: 'og:image', content: 'https://docs.olivares.ai/og-image.png' },
        },
        {
          tag: 'meta',
          attrs: { property: 'og:image:width', content: '1200' },
        },
        {
          tag: 'meta',
          attrs: { property: 'og:image:height', content: '630' },
        },
        {
          tag: 'meta',
          attrs: { name: 'twitter:card', content: 'summary_large_image' },
        },
        {
          tag: 'meta',
          attrs: { name: 'twitter:image', content: 'https://docs.olivares.ai/og-image.png' },
        },
        {
          tag: 'link',
          attrs: { rel: 'icon', href: '/favicon-32.png', sizes: '32x32', type: 'image/png' },
        },
        {
          tag: 'link',
          attrs: { rel: 'apple-touch-icon', href: '/favicon-48.png' },
        },
      ],
      // ⛔ AQUI HUBO UN `expressiveCode: { shiki: { langAlias: … } }` Y SE RETIRA: NO HACIA NADA.
      //
      // Cuatro lenguajes de valla no tienen gramatica en el paquete de Shiki que este sitio usa y
      // salen en gris plano — censado contra `@shikijs/langs/dist`, y el numero lo confirma el
      // propio build, que emite EXACTAMENTE 31 avisos:
      //
      //   cedar   8 bloques   how-to/cookbook/deny-closed-policies.md
      //   rego    8 bloques   how-to/cookbook/deny-closed-policies.md
      //   promql  8 bloques   how-to/troubleshooting.md
      //   caddy   7 bloques   how-to/docker-deployment.md
      //
      // (`text`, 71 bloques, esta bien: es alias nativo y no genera aviso.)
      //
      // El remedio evidente —`langAlias`— NO FUNCIONA EN ESTA PILA, medido tres veces: build
      // cacheado, build limpio (31 avisos, y el bloque `caddy` con UN color de token frente a los
      // CUATRO del bloque `nginx` real de la misma pagina) y servidor de desarrollo. El runtime SI
      // soporta la opcion (@expressive-code/plugin-shiki@0.42.0/dist/index.js:179,
      // `language = langAlias[language] ?? language`), asi que es cableado y no capacidad; y la via
      // alternativa esta cerrada por construccion: astro-expressive-code/dist/index.js:95-96
      // serializa la config de Astro copiando SOLO `langs` y descarta `langAlias` — re-verificado
      // el 2026-08-29 tras un contraste que lo puso en duda: la funcion sigue copiando unicamente
      // `langs`, asi que esa via concreta si esta cerrada.
      //
      // ⚠ PERO ESO NO EXPLICA EL FALLO, y conviene no leerlo como si lo explicara: la via de
      // `expressiveCode.shiki.langAlias` SI llega al renderer (`dist/index.js:160-167` la arrastra
      // desde `ecConfig`), y aun asi el alias no se aplico. **La causa real sigue SIN IDENTIFICAR.**
      // Lo unico medido es el efecto, tres veces; el mecanismo, no.
      //
      // Se retira en vez de dejarla puesta porque una opcion que no hace nada es la misma clase de
      // defecto que este trabajo vino a quitar: una declaracion que nadie cumple. Lo que lo
      // arreglaria de verdad para `cedar` y `rego` es vendorizar sus gramaticas TextMate (ambas
      // Apache-2.0) y pasarlas por `shiki.langs`, que es la via que el serializador SI transporta —
      // y meter gramaticas de terceros en este repositorio es decision de quien gobierna la cadena
      // de suministro, no de esta sesion. Tenirlas con una gramatica ajena esta descartado: una
      // politica de seguridad mal coloreada es peor que una sin colorear.
      // brand.css is GENERATED from the design tokens (scripts/gen-brand-css.mjs); motion.css is
      // hand-authored component selectors and loads after it, so the generated file stays
      // purely token-derived and can be regenerated without losing anything.
      customCss: ['./src/styles/brand.css', './src/styles/motion.css'],
      // The site ships from the public repository: every page links to its own
      // source for edits and contributions.
      editLink: {
        baseUrl: 'https://github.com/olivaresai/olivares/edit/main/docs-site/',
      },
      lastUpdated: true,
      // Diátaxis information architecture: the four modes are the top of the
      // sidebar, NOT the product features (DOC-01). localizeSidebar attaches the
      // per-locale label translations from src/sidebar-i18n.mjs.
      sidebar: localizeSidebar([
        {
          label: 'Start here',
          items: [
            { label: 'What is Olivares AI?', slug: 'start/what-is-olivares-ai' },
            { label: 'Quickstart', slug: 'start/quickstart' },
            { label: 'Paths by role', slug: 'start/paths-by-role' },
            { label: 'How this documentation is organized', slug: 'start/how-the-docs-are-organized' },
            { label: 'Honesty & limits', slug: 'start/honesty-and-limits' },
          ],
        },
        {
          label: 'Tutorials',
          collapsed: false,
          items: [
            { label: 'From zero to a read/write access graph', slug: 'tutorials/zero-to-graph' },
            {
              label: 'Getting started by scenario',
              items: [{ autogenerate: { directory: 'tutorials/getting-started' } }],
            },
          ],
        },
        {
          label: 'How-to guides',
          collapsed: true,
          items: [
            {
              label: 'Install & operate',
              items: [
                { label: 'Self-host the control plane', slug: 'how-to/self-hosting' },
                { label: 'Deploy with Docker', slug: 'how-to/docker-deployment' },
                //(C-13, community plan): the .deb/.rpm/.apk path. No page on this
                // site mentioned a package format before it — `self-hosting` Option 1 is the
                // tarball. It sits right after Docker because that is the fork a newcomer
                // actually faces: container or host package.
                // ⛔ COMMENTED OUT ON PURPOSE. The page carries `draft: true` until the
                // packages are tested on Leap/Tumbleweed and RHEL/Fedora, and Starlight
                // drops drafts from the production build — a sidebar entry pointing at a
                // slug the build does not emit fails the build. PUBLISHING IS TWO EDITS,
                // and neither works alone: remove `draft: true` from the page AND uncomment
                // the line below.
                // { label: 'Install from a package', slug: 'how-to/install-from-packages' },
                { label: 'Install air-gapped', slug: 'how-to/air-gap-install' },
                { label: 'Verify a release', slug: 'how-to/verify-a-release' },
                { label: 'Harden a deployment', slug: 'how-to/security-hardening' },
                { label: 'Back up and restore', slug: 'how-to/backup-and-restore' },
                //there was no upgrade guide of any kind, with `olivares upgrade`
                // already shipping. It sits after the backup entry on purpose.
                { label: 'Upgrade and roll back', slug: 'how-to/upgrade-and-rollback' },
                // A buyer had nowhere on this site to learn where a purchased
                // license goes. The steps existed only in INSTALL.md and
                // docs/UPGRADE-AND-ROLLBACK.md — repo files the site does not serve.
                { label: 'Install a license', slug: 'how-to/install-a-license' },
                { label: 'Monitor with Prometheus', slug: 'how-to/monitor-with-prometheus' },
                { label: 'Troubleshooting', slug: 'how-to/troubleshooting' },
              ],
            },
            {
              label: 'Connect & observe',
              items: [
                { label: 'Connect a source', slug: 'how-to/connect-a-source' },
                { label: 'Connect Claude Code', slug: 'how-to/connect-claude-code' },
                { label: 'Run Claude Code with Olivares', slug: 'how-to/run-claude-code-with-olivares' },
                { label: 'Governed data for Claude', slug: 'how-to/governed-data-for-claude' },
                { label: 'Govern Postgres content', slug: 'how-to/govern-postgres-content' },
                { label: 'Govern your file server', slug: 'how-to/govern-your-file-server' },
                { label: 'Enterprise OTel for Claude Code', slug: 'how-to/claude-code-enterprise-otel' },
                { label: 'Forward to Splunk', slug: 'how-to/forward-audit-to-splunk' },
              ],
            },
            // ⛔ THE INTEGRATION GUIDES ARE WIRED HERE AND DELIBERATELY COMMENTED OUT.
            //
            // `how-to/integrations/{claude-code,codex,grok}.mdx` exist as SKELETONS carrying
            // `draft: true`, so Starlight keeps them out of production builds until their
            // content lands. Autogenerate would therefore render this group with NO children,
            // and that is not hypothetical — measured on 2026-08-30: the build succeeded, the
            // three pages were correctly excluded, and a shipped page rendered
            // "Integration guides" as an empty expandable group with a caret. A visitor would
            // click a category that opens onto nothing. A skeleton must not degrade the live
            // site while it waits.
            //
            // PUBLISHED 2026-08-31, once the group had something to open onto: the
            // three guides plus their six locales, 21 pages, and the citation gate green.
            // Adding a fourth guide needs no edit here at all — it autogenerates.
            {
              label: 'Integration guides',
              items: [{ autogenerate: { directory: 'how-to/integrations' } }],
            },
            {
              label: 'Connector guides',
              items: [{ autogenerate: { directory: 'how-to/connectors' } }],
            },
            {
              label: 'Cookbook',
              items: [{ autogenerate: { directory: 'how-to/cookbook' } }],
            },
            {
              label: 'Govern & build',
              items: [
                { label: 'Govern and approve', slug: 'how-to/govern-and-approve' },
                { label: 'Build a governed workflow', slug: 'how-to/build-a-workflow' },
                { label: 'Manage as code (Terraform)', slug: 'how-to/manage-as-code' },
                { label: 'Use the client SDKs', slug: 'how-to/use-the-client-sdks' },
                { label: 'Build and ship a connector', slug: 'how-to/build-a-connector' },
              ],
            },
          ],
        },
        {
          label: 'Reference',
          collapsed: true,
          items: [
            { label: 'Overview', slug: 'reference' },
            {
              label: 'REST API',
              items: [apiSidebarGroup],
            },
            { label: 'API stability & deprecation policy', slug: 'reference/api-stability' },
            //the console had no section here at all — 57 published routes and no
            // way into any of them from the sidebar. Both pages are generated.
            { label: 'Console screens & permissions', slug: 'reference/console' },
            { label: 'gRPC services & methods', slug: 'reference/grpc' },
            { label: 'Event bus (AsyncAPI 3.0)', slug: 'reference/events' },
            { label: 'Connectors & coverage tiers', slug: 'reference/connectors' },
            { label: 'Verified connectors (third-party)', slug: 'reference/verified-connectors' },
            { label: 'SIEM & telemetry egress', slug: 'reference/siem-telemetry-egress' },
            {
              label: 'Modules catalog',
              items: [
                { label: 'Overview — the 30 modules', slug: 'reference/modules/overview' },
                // Observe
                { label: 'Inventory & discovery', slug: 'reference/modules/i-inventory' },
                { label: 'Live operation & sessions', slug: 'reference/modules/ii-sessions' },
                { label: 'Access & resource map (R/RW)', slug: 'reference/modules/iii-access-map' },
                { label: 'Orchestration & A2A', slug: 'reference/modules/iv-orchestration' },
                { label: 'MCP, skills & capabilities', slug: 'reference/modules/v-capabilities' },
                { label: 'Health, SLA & uptime', slug: 'reference/modules/xxii-health' },
                { label: 'Live-ingest', slug: 'reference/modules/live-ingest' },
                { label: 'Observability', slug: 'reference/modules/observability' },
                { label: 'Claude Code adoption', slug: 'reference/modules/claudeadoption' },
                // Govern & enforce
                { label: 'Identity, permissions & governance', slug: 'reference/modules/vi-governance' },
                { label: 'Source & credential scoping', slug: 'reference/modules/sourcescope' },
                { label: 'Deployment & integration', slug: 'reference/modules/vii-deploy' },
                // Claude & agent ecosystem
                { label: 'Model & provider management', slug: 'reference/modules/x-models' },
                { label: 'Inline inference proxy (PEP)', slug: 'reference/modules/inferenceproxy' },
                { label: 'Internal catalog & marketplace', slug: 'reference/modules/xiv-catalog' },
                { label: 'Voice & realtime agents', slug: 'reference/modules/xvi-voice' },
                // Security & data protection
                { label: 'Security, guardrails & audit', slug: 'reference/modules/ix-security' },
                { label: 'Privileged-session recording', slug: 'reference/modules/recording' },
                { label: 'Data, knowledge & context', slug: 'reference/modules/viii-knowledge' },
                // Compliance & evidence
                { label: 'Compliance & regulatory', slug: 'reference/modules/xiii-compliance' },
                { label: 'SIEM/ITSM forwarder', slug: 'reference/modules/siemforward' },
                { label: 'Posture export to control towers', slug: 'reference/modules/posture-export' },
                { label: 'Reporting (HTML/PDF)', slug: 'reference/modules/reporting' },
                // FinOps
                { label: 'Cost & AI FinOps', slug: 'reference/modules/xi-finops' },
                // Evals & safety
                { label: 'Quality, evals & testing', slug: 'reference/modules/xii-evals' },
                { label: 'Agent simulation/testing sandbox', slug: 'reference/modules/xvii-sandbox' },
                { label: 'Red-teaming & adversarial testing', slug: 'reference/modules/xviii-redteam' },
                // Platform & integrations
                { label: 'Output integrations & notifications', slug: 'reference/modules/xv-notify' },
                { label: 'Eventing & webhooks', slug: 'reference/modules/eventing' },
                { label: 'Saved console views', slug: 'reference/modules/consoleviews' },
                // Platform & core capabilities (not counted among the 30 modules)
                { label: 'API & manage-as-code (platform)', slug: 'reference/modules/xix-api-manage-as-code' },
                { label: 'Multi-tenancy & org management (platform)', slug: 'reference/modules/xx-multi-tenancy' },
                { label: 'Executive dashboards & reporting (console)', slug: 'reference/modules/xxi-executive-dashboards' },
                { label: 'Model operations (own models)', slug: 'reference/modules/xxiii-model-operations' },
                { label: 'Fine-tuning & inference execution (planned)', slug: 'reference/modules/xxiii-fine-tuning' },
              ],
            },
            { label: 'CLI', slug: 'reference/cli' },
            { label: 'Configuration', slug: 'reference/configuration' },
            { label: 'Glossary', slug: 'reference/glossary' },
          ],
        },
        {
          label: 'Explanation',
          collapsed: true,
          items: [
            { label: 'Overview', slug: 'explanation' },
            // main finding: the work plane existed on this site only as generated
            // CLI/event reference — the kernel had no page saying what it is.
            { label: 'The work plane', slug: 'explanation/work-plane' },
            { label: 'EU AI Act evidence from runtime data', slug: 'explanation/eu-ai-act-evidence' },
            { label: 'Architecture', items: [{ autogenerate: { directory: 'explanation/architecture' } }] },
            { label: 'Security & threat model', items: [{ autogenerate: { directory: 'explanation/security' } }] },
            { label: 'Positioning & fit', items: [{ autogenerate: { directory: 'explanation/positioning' } }] },
            { label: 'Open core & licensing', slug: 'explanation/open-core-and-licensing' },
            { label: 'Supporting the project', slug: 'explanation/supporting-the-project' },
          ],
        },
      ]),
      plugins: [
        // Versioning: ACTIVE. The product is pre-1.0 with no release cut
        // (the binary reports `dev`, the repo has no tags), so the archived
        // version is honestly a DATED DOCS SNAPSHOT — the public-launch baseline
        // (2026-06) — not a product release we refuse to fabricate. When the
        // release gate cuts the first release, add `{ slug: '1.0', label: 'v1.0' }` here (see
        // README.md §Versioning); the plugin snapshots current docs on build.
        starlightVersions({
          current: { label: 'Latest' },
          // Declared in src/site-locales.mjs, same reason as `locales` above: the
          // parity gate must skip archived snapshots, and it must learn which they
          // are from the site's own declaration rather than from a second parser.
          versions: VERSIONS,
        }),
        starlightOpenAPI([
          {
            base: 'reference/api',
            schema: OPENAPI_SCHEMA,
            sidebar: {
              group: apiSidebarGroup,
              label: 'Olivares AI control plane API',
              operations: { badges: true, labels: 'path', sort: 'document' },
            },
          },
          {
            // The beta module-route reference. Its overview page carries the
            // "beta, may change" banner from the document's own info.description.
            base: 'reference/api-beta',
            schema: OPENAPI_BETA_SCHEMA,
            sidebar: {
              group: apiBetaSidebarGroup,
              label: 'Module routes (beta)',
              operations: { badges: true, labels: 'path', sort: 'document' },
            },
          },
        ]),
      ],
    }),
  ],
})
