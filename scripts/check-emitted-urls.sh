#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-emitted-urls.sh — a URL the product SENDS must be a URL that answers.
#
# ============ WHY THIS EXISTS ===============================================================
# `core/api/stability.go:52` puts this in the `Link` header of every deprecated API response:
#
#     https://docs.olivares.ai/reference/api-stability/
#
# Measured 2026-08-13, five attempts: 404, 404, 404, 404, 404. The page EXISTS in this repo
# (docs-site/src/content/docs/reference/api-stability.md) and is not deployed at that host —
# docs.olivares.ai is served by the marketing Worker, not by docs-site, which docs-site/README.md
# records as a one-time move that was never completed. Two more emitted docs URLs answer the same
# way: /reference/verified-connectors/ and /how-to/build-a-connector/.
#
# So a customer who follows our own API-stability policy — the contract we point them at when we
# deprecate something — lands on a 404. Nothing measured this, because nothing connected "the
# product emits a URL" to "that URL answers": the page's presence in the repo is not its presence
# on the web, and every gate we had was looking at the repo.
#
# ============ WHAT IT CHECKS ================================================================
# Membership is DERIVED: every olivares.ai URL literal in shipping, non-test source is an emitted
# URL. A hand-written list of "the URLs we care about" would caducate exactly like the surface
# list in check-release-version.sh did.
#
#   1 · every emitted URL is DECLARED below with its measured status and the date measured
#   2 · no declaration is older than MAX_RECORD_AGE_DAYS — "nobody has checked" is a red, not a
#       silence, which is the lesson check-security-txt.sh learned twelve days too late
#   3 · a declared non-200 must carry an owner, and it is NAMED in the verdict every run: a
#       known-broken URL must never become quiet furniture
#
# The push gate makes no network request. `--probe` does, five attempts per URL, and prints the
# record lines to paste back. Five, not one: a single sample off a loaded machine produced a
# confident wrong verdict on this very host on the day this was written.
#
# Three answers, never two: CLEAN / BROKEN / UNVERIFIED.
#
# ============ WHERE THE RECORD LIVES (2026-09-23) ===========================================
# In two places, chosen by who can see the emitter. The INLINE record below ships with the
# export, so it holds every line that a PUBLISHED file emits and no other. A line whose every
# emitter is curated out of the export lives in docs/emitted-urls-hub-record.txt instead: same
# format, with `#` and blank lines ignored, and the export curation removes it by one exact
# entry. Left inline, such a line reads in the public tree as "declared and nothing emits it",
# and the export cannot drop one line of an inline string.
#
# The record file's ABSENCE is not a password. The gate asks scripts/hub-leg.sh --classify
# which tree it is in, and that answer rests on the marker sentence the export stamps AND on
# the absence of every hub-only path:
#   public   -> SCOPED, said on every run, and only the inline record is read
#   hub      -> BROKEN (exit 1): a development tree has lost its record, and reading half of
#               it would certify URLs nobody declared
#   unknown  -> UNVERIFIED (exit 2), and the same for a classifier that cannot run: nothing
#               proves this is the public export, so nothing is certified
# A URL declared in BOTH places is BROKEN, because the two trees would judge it by different
# lines.
#
# The split is ENFORCED in both directions, not only described. A URL that only the excluded record
# declares, emitted by a file the export publishes, is BROKEN: the public tree would find it
# undeclared, and the excluded record would be a way to whitelist a page through the one file the
# export drops. And an INLINE row whose every emitter is curated out is BROKEN too (since
# 2026-09-23): it belongs in the excluded record. Which files publish is read from
# scripts/export-public.sh's own lists, by the rule its --manifest applies. Where that script is
# absent, every file present counts as published, and lists the gate cannot read are
# UNVERIFIED (exit 2).
set -eu
cd "$(dirname "$0")/.."

MAX_RECORD_AGE_DAYS=45

# <url> <status measured> <ISO date> [owner, only when the status is not 200]
#
# ⛔ THE 404s ARE REAL AND ARE NOT WAIVED, AND THEY ARE NOT ALL THE SAME KIND. The owner column
# says which is which, and every one of them is NAMED in the verdict on every run:
#
#   release-blocker-no-producer-no-server
#       https://olivares.ai/updates is the default update endpoint of EVERY community binary
#       (cmd/olivares/cmd_upgrade.go:60). The client asks for <base>/<channel>/manifest.json, so
#       the first thing a community user's `olivares upgrade` does is 404 — measured 5/5, and
#       .github/workflows/ contains no producer that publishes there. This is the public
#       product's update path, and it needs a signed channel manifest, which needs the release
#       ceremony's key. It is not code that is missing.
#
#   compliance-pages-unpublished — ⛔ RESUELTO EL 2026-08-29, y se deja escrito en vez de borrado.
#       Decía: el exportador OSCAL estampa estas URL en documentos que lee un auditor
#       (modules/compliance/oscal.go:109,195,242 · oscalprofile.go:159), el documento es correcto
#       y la página que cita no existe. Ya existe: web#97 (`4e7eff79d`) las publicó y las tres
#       responden 200 — medido con el método de este propio gate (`curl -sSL`, que sigue la
#       redirección de la barra final), 26 ids × 2 rutas = 52/52 a 200, y el control negativo
#       `/compliance/frameworks/no_such_framework` a 404, sin el cual 52 doscientos no distinguen
#       enrutado real de un comodín.
#
#       Se conserva el párrafo, y no es sentimentalismo: la cabecera de más abajo lo NOMBRA como
#       uno de los cuatro dueños que un re-sondeo masivo aplastó el 2026-08-18. Borrarlo dejaría
#       esa lección citando algo que ya no está en el fichero.
#
#   namespace-identifier-not-a-location
#       https://olivares.ai/ns/oscal is a NAMESPACE. It identifies; it is not required to answer,
#       and its 404 is recorded rather than waived so nobody later reads the silence as health.
#
#   docs-subdomain-withdrawn
#       ⛔ RENOMBRADO DESDE `fran-deployment-decision` EL 2026-08-19, PORQUE YA NO ES UNA DECISIÓN
#       PENDIENTE. Decía que las páginas existen en el repo y no están desplegadas en ese host, y
#       planteaba dos salidas: entregar docs.olivares.ai a docs-site, o repuntar las URLs a
#       olivares.ai/docs. **La primera ya no existe: el nombre ha sido retirado del DNS.**
#
#       Medido hoy con la calibración que a las lecturas anteriores les faltaba —y ésa es toda la
#       diferencia, porque un 000 desde esta caja ya engañó una vez y borró un Canonical que
#       servía—: `getent hosts` resuelve olivares.ai, alma.olivares.ai y licenses.olivares.ai en la
#       MISMA corrida y falla sólo en docs (si fuese el egreso, fallarían las cuatro), y un
#       resolutor independiente del local —Cloudflare DoH— devuelve **Status 3 (NXDOMAIN)** con 0
#       respuestas, frente a Status 0 con 2 para el apex. NXDOMAIN no es «no contesta ahora»: es
#       «el nombre no está publicado». Cinco intentos HTTP: 5/5 000.
#
#       Y no queda decisión que pedir: Ya la tomó el 2026-08-01, citada literal en
#       core/api/stability.go:54 — «ahora mismo están en olivares.ai/docs … [docs.olivares.ai] es
#       irrelevante». Mantener la etiqueta anterior hacía que este gate pareciera estar esperando a
#       por algo ya resuelto, que es la clase de espera que no debe existir.
#
#       ⛔ RE-MEDIDO EL 2026-08-23: **el nombre VOLVIÓ, y la fila pasa de `000` a `200`.**
#       Todo lo de arriba fue cierto mientras se escribió y la lección del `000` sigue en pie —por
#       eso no se borra—, pero el diagnóstico se completó ese día: el nombre no estaba «retirado»,
#       lo **desprendía cada deploy**. `routes` de `wrangler.jsonc` es DECLARATIVO y reconcilia los
#       custom domains contra el fichero, así que el aprovisionamiento a mano del 08-01 se perdía
#       en cada push a `main` (>=7 desde el 08-13). Declarado en el fichero y plegado al apex con
#       un 301 (repo web, PR #82, mergeado 12:42:42Z), el nombre resolvió a los TRES minutos.
#
#       Medido con el mismo método que este gate usa (`curl -sSL`, que sigue redirecciones y por
#       tanto anota el código FINAL) y con control, porque un número sin control ya nos costó una
#       vez: **5/5 = 200** para docs.olivares.ai, **3/3 = 200** para el apex. La cadena real es
#       `docs.olivares.ai/` → 301 → `olivares.ai/docs` → 200, en UN salto.
#
#       ⛔⛔ LOS DOS PÁRRAFOS QUE SEGUÍAN AQUÍ ESTABAN CADUCADOS Y DECÍAN LO CONTRARIO DE LO QUE
#       PASA. Re-medido el 2026-08-27 por el carril del escaparate, y se corrige aquí porque un
#       comentario falso en
#       el fichero que sostiene un registro se cita después como si fuera la medida.
#
#       Decían: (a) «la profundidad no está publicada… el docs-site Astro, que sigue sin
#       publicarse», y (b) «**ninguno de los 38** [destinos de ayuda de la consola] existe».
#
#       Hoy, medido con el mismo método y con control positivo:
#         · `docs.olivares.ai/` → **200 DIRECTO**, sirviendo el docs-site. La cadena
#           `docs.olivares.ai → 301 → olivares.ai/docs` que describe la nota de arriba YA NO
#           EXISTE: el hostname es una route del Worker `olivares-docs` (y su DNS lo sostiene
#           todavía el custom domain de `olivaresai-web`; ver docs-site/wrangler.jsonc).
#         · `docs.olivares.ai/reference/api-stability/` → **200**. La profundidad SÍ está
#           publicada; lo que sigue en 404 es esa misma ruta bajo el apex, que es otro sitio.
#         · los 37 `helpHref` no-raíz de `web/src/features/registry.tsx`, pedidos uno a uno bajo
#           `https://docs.olivares.ai<ruta>/`: **37/37 = 200, cero no-200**. De ahí que
#           `DEEP_LINKS_PUBLISHED` esté hoy en `true` y `DOCS_BASE` vuelva a ser el subdominio.
#
#       ⇒ Lo que este registro mide sigue siendo la RAÍZ, que es lo correcto para una fila de
#       URL emitida. Lo que cambia es que la salvedad ya no aplica y no debe volver a copiarse.
#
#   public-repo-empty-release-blocker
#       ⛔ THE LAUNCH BLOCKER ITSELF, measured instead of remembered (the count moves as copy
#       lands and is not restated here — the printed summary is the count).
#       github.com/olivaresai/olivares is PUBLIC and EMPTY — the API says size=0KB with a pushed_at
#       from June — so every deep link the launch copy hard-codes answers 404. Among them the
#       INSTALL ONE-LINER that docs/launch/blog-launch-post.md:130 and reddit-pack.md:57 tell people
#       to pipe into a shell:
#
#           curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install.sh | sh
#
#       Populating that repository is call (nothing goes to olivaresai/* without it), so this
#       gate does not fix it — it makes it impossible to forget, dated and named on every run. It was
#       a manual checkbox in RUNBOOK-DAY-D (P4) until now, and a checkbox is not a measurement.
#
#   docs-site-deploy-lag
#       docs-site/public/_redirects promises `/cli -> /reference/cli/ 301`, it is committed on
#       main (31e13c238), and production answers 404 — measured five of five with the docs host
#       answering 200 in the same run. The 404 is the LAG ITSELF, not rot: the docs site is
#       deployed by hand, so main moves and the site does not. It is named in
#       .github/workflows/docs-site-deploy.yml precisely so the cost of that missing deploy leg is
#       written where the leg is being built, and `task docs-site:live-check` is what re-measures
#       it. The row closes the day the site is published from that workflow, not before.
#
#   apex-serves-no-docs-depth
#       The apex serves the MARKETING site's /docs hub, not the docs-site tree, so no path BELOW
#       /docs resolves there — measured 404, five of five, with the apex answering 200 in the same
#       run. It is recorded rather than waived because `core/api/stability.go` names it on purpose:
#       the file states both halves of a live comparison (docs.olivares.ai serves the policy page,
#       the apex does not) so the next reader does not have to re-measure to know why the constant
#       still points at the root. A 404 that a comment DELIBERATELY cites is a fact, not rot.
#   discussions-not-enabled-maintainer-act
#       GitHub Discussions on the public repository. `has_discussions=false` and the /discussions
#       URL answers 404 (measured 2026-08-27). It is a SEPARATE owner from the empty-repository
#       family on purpose, and the distinction is the whole value of the row: pushing the export
#       makes every `blob/main/...` link above resolve BY ITSELF, and does NOT make this one
#       resolve — enabling Discussions is a maintainer act in the repository settings, listed in
#       docs/RELEASE-GO-LIVE-RUNBOOK.md §4. Filing it under `public-repo-empty-release-blocker`
#       would attach a TRUE status to a FALSE reason, and the day the repository is pushed
#       somebody would read the remaining 404 as a stale record instead of as the pending act it
#       is. The two emission sites this repository carried were retired on 2026-08-27
#       (.github/ISSUE_TEMPLATE/{config,docs_issue}.yml, which now route to SUPPORT.md); the row
#       exists for the launch copy that names Discussions as the community home.
#
#   public-release-v26.9.0-pending
#       The README's high-assurance path names the release page of the version this tree cuts
#       (README.md, "release page"). That page exists only after the PUBLIC ACT publishes the
#       v26.9.0 release: pushing the export does not create it, and neither does the tag on the
#       repository that declares that profile. Measured 404 on 2026-09-16 before the cut was published. It is its own
#       owner, not `public-repo-empty-release-blocker`, for the same reason Discussions is: a true
#       status must carry its true reason, and this one resolves at release time, by the act.
#       ⛔ RESOLVED on 2026-09-23. v26.9.0 was published on 2026-09-17 and its tag page answers
#       200, so no row carries this owner any more. The paragraph stays so that the history above
#       still names what it describes.
#
#   drill-fixture-not-a-location
#       A security-DRILL advisory id in cmd/olivares/fixtures/. It identifies a rehearsal, is not
#       meant to resolve, and is recorded rather than waived so nobody later reads its 404 as rot.
#
#   sigstore-certificate-identity-not-a-location
#       The GitHub workflow URI is the exact certificate subject that cosign verifies. It is an
#       identity, not an HTTP release page; its literal tag template answered 404 in five of five
#       probes on 2026-09-02 and must remain exact for the signature policy.
#
#   package-repository-awaits-authorized-publish
#       The canonical package origin exists in DNS but answered 404 in five of five probes on
#       2026-09-02. F2 prepares the guarded publisher; only the separately authorized publication
#       act can populate the bucket and make this endpoint answer.
#
# ⚠⚠ AND RE-PROBING EN MASSE ALMOST DESTROYED THAT DECISION (2026-08-18). Regenerating the whole
# record from one `--probe` run overwrote the docs.* lines with `000` — «I could not reach it» —
# and a 000 written into a record READS AS A MEASUREMENT to everyone after you. It also flattened
# four owners (`compliance-pages-unpublished`, `namespace-identifier-not-a-location`,
# `release-blocker-no-producer-no-server`) into the probe's default, erasing distinctions this very
# header spends paragraphs explaining. Both were caught by diffing against the previous record
# instead of trusting the new one. RULE: a probe that returns 000 does NOT replace an existing
# measurement, and a re-probe never rewrites the owner column.
#
# ⚠ AND THE docs.* MEASUREMENTS ARE OLDER THAN THE REST ON PURPOSE. Re-probed 2026-08-14 they
# answered 000 five times out of five from this container, exactly as they did once before under
# local load — and that reading, taken as fact, once deleted a working Canonical from our
# published security.txt. The 2026-08-13 values are the ones taken when the box was quiet and
# every host answered 5/5. That caution was RIGHT, and el 2026-08-19 queda
# DESCARGADA en vez de repetida: «una medida honesta y vieja gana a una fresca que el punto de
# observación no puede sostener» sólo vale mientras ese punto no se pueda calibrar. Hoy se calibró
# —control positivo sobre los hermanos con el MISMO instrumento, más un resolutor independiente que
# devuelve NXDOMAIN— y el veredicto ya no depende de esta caja: el nombre no está publicado. El
# detalle, en `docs-subdomain-withdrawn` arriba. What this gate guarantees meanwhile is that the number
# cannot quietly grow and the record cannot quietly rot.
# 2026-09-17 · README rewrite (docs/readme-clarity-20260916): the thirteen olivares.ai section URLs it
# links, the releases index and the v26.9.0 tag page were measured with --probe from this box (five
# attempts each). The tag page is 404 on the public repository until the v26.9.0 release is published
# there (dev cut only, PR #2504); the README emitted it since that cut without a record line.
# 2026-09-23 · release links. There is no v26.9.1: the next release is v26.10, and no v26.9.1
# tag or release exists. The release canon is the published
# v26.9.0, so README.md's release link names v26.9.0 and the v26.9.1 row left the record with
# that correction. The v26.9.0 tag page answered 200 five of five times on 2026-09-23 (GET,
# following redirects); v26.9.0 is the latest published release (2026-09-17). The v26.8.0 tag
# page answers 200 too (published 2026-09-01), so its
# 404, which was true when it was measured on 2026-08-31, now reads 200. The enterprise
# repository page moved to the excluded record unchanged, because only curated-out files emit it.
# 2026-09-23 · six more rows moved to the excluded record unchanged, for the same reason: the
# CODE_OF_CONDUCT and honesty-and-limits blob links, Discussions, the v26.8.0 tag page, the raw
# honesty-and-limits link and the raw install.sh one-liner. Only docs/launch/ files emit them. The
# gate now refuses such a row inline, so the public tree never reads one as emitted by nothing.
EMITTED_RECORD="https://alma.olivares.ai 200 2026-08-27
https://docs.olivares.ai 200 2026-08-23
https://docs.olivares.ai/cli 404 2026-08-28 docs-site-deploy-lag
https://docs.olivares.ai/reference/api-stability/ 200 2026-08-28
https://docs.olivares.ai/reference/configuration/ 200 2026-09-11
https://github.com/olivaresai/olivares 200 2026-08-18
https://github.com/olivaresai/olivares.git 200 2026-08-18
https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/\${tag} 404 2026-09-02 sigstore-certificate-identity-not-a-location
https://github.com/olivaresai/olivares/blob/main/CONTRIBUTING.md 404 2026-08-18 public-repo-empty-release-blocker
https://github.com/olivaresai/olivares/blob/main/GOVERNANCE.md 404 2026-08-27 public-repo-empty-release-blocker
https://github.com/olivaresai/olivares/blob/main/INSTALL.md#operate-claude-code-co-deployment 404 2026-08-18 public-repo-empty-release-blocker
https://github.com/olivaresai/olivares/blob/main/SECURITY.md 404 2026-08-18 public-repo-empty-release-blocker
https://github.com/olivaresai/olivares/blob/main/SUPPORT.md 404 2026-08-27 public-repo-empty-release-blocker
https://github.com/olivaresai/olivares/blob/main/docs/RELEASE-VERIFICATION.md 404 2026-08-18 public-repo-empty-release-blocker
https://github.com/olivaresai/olivares/blob/main/scripts/smoke-agentops.sh 404 2026-08-18 public-repo-empty-release-blocker
https://github.com/olivaresai/olivares/releases 200 2026-09-17
https://github.com/olivaresai/olivares/releases/tag/v26.9.0 200 2026-09-23
https://github.com/olivaresai/olivares/security/advisories/OLIVARES-DRILL-0001 404 2026-08-18 drill-fixture-not-a-location
https://github.com/olivaresai/olivares/tree/main/examples/govern-claude-code 200 2026-08-18
https://licenses.olivares.ai 200 2026-08-18
https://olivares.ai 200 2026-08-18
https://olivares.ai/compliance/assessment-plan/ 200 2026-08-29
https://olivares.ai/compliance/capabilities 200 2026-08-29
https://olivares.ai/compliance/frameworks/ 200 2026-08-29
https://olivares.ai/docs 200 2026-08-18
https://olivares.ai/docs/reference/api-stability 404 2026-08-28 apex-serves-no-docs-depth
https://olivares.ai/favicon.svg 200 2026-08-18
https://olivares.ai/ns/oscal 404 2026-08-18 namespace-identifier-not-a-location
https://olivares.ai/pricing 200 2026-08-26
https://olivares.ai/product 200 2026-09-17
https://olivares.ai/solutions 200 2026-09-17
https://olivares.ai/how-it-works 200 2026-09-17
https://olivares.ai/architecture 200 2026-09-17
https://olivares.ai/security 200 2026-09-17
https://olivares.ai/trust 200 2026-09-17
https://olivares.ai/compare 200 2026-09-17
https://olivares.ai/demo 200 2026-09-17
https://olivares.ai/changelog 200 2026-09-17
https://olivares.ai/olivares/install.sh 200 2026-09-17
https://olivares.ai/brand 200 2026-09-17
https://olivares.ai/press 200 2026-09-17
https://olivares.ai/status 200 2026-09-17
https://olivares.ai/roadmap 200 2026-09-17
https://olivares.ai/updates 404 2026-08-18 release-blocker-no-producer-no-server
https://packages.olivares.ai 404 2026-09-02 package-repository-awaits-authorized-publish
https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install-agentops.sh 404 2026-08-18 public-repo-empty-release-blocker"

EMU_SELFTEST=0
[ "${1:-}" = "--selftest" ] && EMU_SELFTEST=1
EMU_PROBE=0
[ "${1:-}" = "--probe" ] && EMU_PROBE=1
export EMU_SELFTEST EMU_PROBE EMITTED_RECORD MAX_RECORD_AGE_DAYS

python3 - <<'PY'
import fnmatch, os, re, shutil, socket, stat, subprocess, sys, tempfile, datetime
from urllib.parse import urlsplit

SELFTEST = os.environ.get("EMU_SELFTEST") == "1"
PROBE = os.environ.get("EMU_PROBE") == "1"
MAX_AGE = int(os.environ["MAX_RECORD_AGE_DAYS"])

# A URL literal, stopping at whatever cannot be part of one in prose or code.
# ⛔ THE APEX WAS INVISIBLE TO THE FIRST VERSION OF THIS PATTERN, and it is where the community
# product points. `[a-z0-9][a-z0-9.-]*\.olivares\.ai` REQUIRES something before the domain, so
# https://olivares.ai/updates — the default update endpoint every community binary uses
# (cmd/olivares/cmd_upgrade.go:60) — matched nothing at all. The gate reported six URLs and a
# clean census while the one that serves the PUBLIC product was outside its alphabet. A gate
# that cannot see the apex of its own domain is measuring a subset and calling it the set.
URL = re.compile(r"https://(?:[a-z0-9][a-z0-9.-]*\.)?olivares\.ai(?:/[^\s\"'`)\]<>,;]*)?")

# ⛔ Y LA SEGUNDA FAMILIA, QUE ES LA QUE SE NOS ESCAPÓ ENTERA. La copy de lanzamiento manda a la
# gente a `github.com/olivaresai/olivares`: el blog y el pack de Reddit dicen literalmente
# `curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install.sh | sh`.
# Ese repositorio está PÚBLICO y VACÍO —`size=0KB`, `pushed_at` de junio— así que la orden de
# instalación que publicamos descarga un 404 y lo pipea a `sh`. Medido 5/5 el 2026-08-18: de los
# diez enlaces que la copy fija, OCHO son 404.
#
# La familia estaba fuera de este censo por dos razones a la vez, y las dos había que romperlas:
# el patrón sólo veía `olivares.ai`, y las raíces sólo miraban código que se envía — no `docs/launch`
# ni `docs-site`, que es donde vive la copy que el cliente lee ANTES de instalar nada.
ORG_URL = re.compile(r"https://(?:raw\.githubusercontent\.com|github\.com)/olivaresai/[^\s\"'`)\]<>,;]*")
ROOTS = ("core", "modules", "cmd", "clients", "sdk", "connectors", "operator",
         "web/src", "packaging", "deploy", "examples", "oscap")
# Raíces de COPY PUBLICADA: sólo se barren con ORG_URL. Meter `olivares.ai` aquí ampliaría el censo
# de golpe con URLs de prosa que nadie ha declarado, y un gate que enrojece por su propia ampliación
# se desactiva el mismo día.
COPY_ROOTS = ("docs/launch", "docs/trust", "docs-site/src/content")
# .github/FUNDING.yml alimenta el botón «Sponsor» de GitHub, que es de las superficies más
# visibles del repositorio: lo ve todo el que entra. Su `custom:` es una URL NUESTRA y hasta
# hoy no la gateaba nada — medido el 2026-08-26. (El `github:` no es una URL y no entra por
# aquí; y una cuenta personal de Sponsors quedaría fuera de los dos patrones a propósito:
# este censo cubre nuestros dominios y nuestra organización, no terceros.)
COPY_FILES = ("README.md", ".github/FUNDING.yml")
# ⛔ `.github/` ENTERO, Y NO SÓLO SU FUNDING.yml. Medido el 2026-08-27: las plantillas de issues
# que VIAJAN en el export emitían `https://github.com/olivaresai/olivares/discussions` —404, porque
# Discussions está DESHABILITADO— y este censo no las miraba, porque de `.github/` sólo entraba
# FUNDING.yml por la línea de arriba. Es exactamente la clase que cerró en `docs/launch` y que
# aquí seguía abierta con NINGÚN control encima: la superficie más visible del repositorio
# (plantillas de issue, chooser, perfil de la organización, botón Sponsor) quedaba fuera del único
# gate que comprueba que lo que emitimos existe.
#
# Se barre con LOS DOS PATRONES, como `.github/FUNDING.yml` y por la misma razón: `.github/` no es
# código que se envía, es COPY PUBLICADA — la lee todo el que entra al repositorio. La ampliación
# se midió antes de hacerla, que es lo que la hace segura: sobre el árbol de hoy destapa NUEVE URLs
# distintas, de las que SIETE ya estaban declaradas; las dos nuevas son `alma.olivares.ai` (perfil
# de la organización) y el `blob` de GOVERNANCE.md (plantilla de feature). No es una ampliación a
# ciegas que enrojece el día que se añade, que es como se desactivan los gates.
PUBLISHED_SURFACE_ROOTS = (".github",)
SKIP_DIRS = {"node_modules", ".git", "vendor", "dist", ".astro", "testdata"}
EXTS = (".go", ".ts", ".tsx", ".md", ".json", ".yaml", ".yml")


def unverified(msg):
    print(f"UNVERIFIED check-emitted-urls: {msg}")
    sys.exit(2)


def is_test(name):
    return name.endswith("_test.go") or ".test." in name or name.endswith(".spec.ts")


def unread_emitter(path, exc):
    """A file the census listed and cannot read is COULD NOT LOOK (exit 2), never a skip: a
    forbidden emitter behind mode 000 would otherwise pass as "every one declared"."""
    unverified(f"{path} was enumerated but cannot be read ({exc.strerror or exc}); an unread "
               "emitter is not a clean one")


def walk_refused(exc):
    """os.walk's `onerror`: a directory the census cannot list hides every emitter in it, so it
    is COULD NOT LOOK (exit 2), never a smaller census."""
    unverified(f"{exc.filename} could not be listed ({exc.strerror or exc}); a directory the "
               "census cannot list is not an empty one")


def census_walk(top):
    """-> os.walk(top) that refuses instead of skipping. A root that does not exist yields
    nothing (a partial tree may lack it); a root that exists and cannot be examined, or is not a
    directory, is COULD NOT LOOK, and so is any directory below it that cannot be listed."""
    try:
        os.lstat(top)
    except FileNotFoundError:
        return iter(())
    except OSError as exc:
        walk_refused(exc)
    try:
        is_dir = stat.S_ISDIR(os.stat(top).st_mode)
    except OSError as exc:
        walk_refused(exc)
    if not is_dir:
        unverified(f"{top} is a census root but not a directory; nothing below it can be listed")
    return os.walk(top, onerror=walk_refused)


def listed_source(path):
    """-> True for a surface file the census must read, False for an optional one that is simply
    absent (README.md, .github/FUNDING.yml). Present but unexaminable (a dangling link included)
    or not a regular file is COULD NOT LOOK; os.path.isfile() answered False to both, a skip."""
    try:
        os.lstat(path)
    except FileNotFoundError:
        return False
    except OSError as exc:
        unread_emitter(path, exc)
    try:
        mode = os.stat(path).st_mode
    except OSError as exc:
        unread_emitter(path, exc)
    if not stat.S_ISREG(mode):
        unverified(f"{path} is listed as a surface file but is not a regular file")
    return True


def emitted():
    """-> {url: [(path, line)]}. Derived, never declared: a URL literal in shipping, non-test
    source is a URL this product can put in front of a customer."""
    found = {}
    for top in ROOTS:
        for root, dirs, files in census_walk(top):
            dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
            for f in sorted(files):
                if not f.endswith(EXTS) or is_test(f):
                    continue
                path = os.path.join(root, f)
                try:
                    text = open(path, encoding="utf-8", errors="replace").read()
                except OSError as exc:
                    unread_emitter(path, exc)
                for i, line in enumerate(text.splitlines(), 1):
                    for m in URL.finditer(line):
                        found.setdefault(m.group(0).rstrip(".,"), []).append((path, i))
                    for m in ORG_URL.finditer(line):
                        found.setdefault(m.group(0).rstrip(".,"), []).append((path, i))
    for top in COPY_ROOTS:
        for root, dirs, files in census_walk(top):
            dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
            for f in sorted(files):
                if not f.endswith(EXTS + (".mdx",)) or is_test(f):
                    continue
                path = os.path.join(root, f)
                try:
                    text = open(path, encoding="utf-8", errors="replace").read()
                except OSError as exc:
                    unread_emitter(path, exc)
                for i, line in enumerate(text.splitlines(), 1):
                    for m in ORG_URL.finditer(line):
                        found.setdefault(m.group(0).rstrip(".,"), []).append((path, i))
    copy_files = list(COPY_FILES)
    for top in PUBLISHED_SURFACE_ROOTS:
        for root, dirs, files in census_walk(top):
            dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
            for f in sorted(files):
                if f.endswith(EXTS + (".mdx",)) and not is_test(f):
                    copy_files.append(os.path.join(root, f))
    for path in dict.fromkeys(copy_files):
        if not listed_source(path):
            continue
        try:
            text = open(path, encoding="utf-8", errors="replace").read()
        except OSError as exc:
            unread_emitter(path, exc)
        for i, line in enumerate(text.splitlines(), 1):
            # Los dos patrones, no sólo el de la organización. Medido el 2026-08-26: con sólo
            # ORG_URL, `.github/FUNDING.yml` entraba en el censo y su `custom:` seguía invisible —
            # el gate llegaba a decir «declarada y nadie la emite», que es la queja correcta ante
            # un censo que no mira donde debe. Destapa exactamente DOS: `https://olivares.ai` en
            # el README (ya declarada) y `https://olivares.ai/pricing` en FUNDING.yml.
            for m in URL.finditer(line):
                found.setdefault(m.group(0).rstrip(".,"), []).append((path, i))
            for m in ORG_URL.finditer(line):
                found.setdefault(m.group(0).rstrip(".,"), []).append((path, i))
    return found


# export-closure: absent-by-design docs/emitted-urls-hub-record.txt — DATA, read and never run:
#   the excluded half of the record. The export curation removes it by one exact entry, and in
#   the published tree its absence is SCOPED by the tree's profile, never assumed.
HUB_RECORD = "docs/emitted-urls-hub-record.txt"


def parse_record(text, source):
    """-> {url: (status, date, owner)} from `<url> <status> <date> [owner]` lines."""
    out = {}
    for raw in text.splitlines():
        parts = raw.split()
        if not parts or parts[0].startswith("#"):
            continue
        if len(parts) < 3:
            unverified(f"{source}: record line {raw!r} is not '<url> <status> <date> [owner]'")
        url, status, when = parts[0], parts[1], parts[2]
        out[url] = (status, when, parts[3] if len(parts) > 3 else "")
    return out


def tree_profile():
    """-> (profile, why): what scripts/hub-leg.sh --classify says this tree is. A classifier that
    is missing, fails, or answers outside its three words gives `unknown`. That is an inability,
    and it is never a reason to guess `public`."""
    cls = os.path.join("scripts", "hub-leg.sh")
    try:
        r = subprocess.run(["bash", cls, "--classify", "--root", os.getcwd()],
                           capture_output=True, text=True, timeout=60)
    except (OSError, subprocess.SubprocessError) as exc:
        return "unknown", f"{cls} --classify could not run ({exc})"
    answer, why = r.stdout.strip(), r.stderr.strip()
    if why.startswith("hub-leg: "):
        why = why[len("hub-leg: "):]
    if r.returncode != 0 or answer not in ("hub", "public", "unknown"):
        return "unknown", f"{cls} --classify exited {r.returncode} answering {answer!r}"
    return answer, why


def hub_record_text():
    """-> the excluded record's text, or None where the tree has none by design (the public export).

    If the file is present, it is read whatever the profile. If it is absent, the profile decides,
    and only a tree PROVEN public continues. A present file that cannot be read is COULD NOT LOOK."""
    try:
        os.lstat(HUB_RECORD)
    except FileNotFoundError:
        profile, why = tree_profile()
        if profile == "public":
            print(f"SCOPED check-emitted-urls: {HUB_RECORD} is hub-only and curated out of the "
                  f"published tree ({why}); only the inline record is read here")
            return None
        if profile == "hub":
            print(f"BROKEN check-emitted-urls: {HUB_RECORD} is missing from a development tree "
                  f"({why}). It declares the URLs that only curated-out files emit, and the inline "
                  "record alone would leave them undeclared or certify half a record.")
            sys.exit(1)
        unverified(f"{HUB_RECORD} is absent and this tree is not proven to be the public export "
                   f"({why}); refusing to read the inline record alone")
    except OSError as exc:
        unverified(f"{HUB_RECORD} cannot be examined ({exc.strerror or exc})")
    try:
        with open(HUB_RECORD, encoding="utf-8") as fh:
            return fh.read()
    except (OSError, UnicodeDecodeError) as exc:
        unverified(f"{HUB_RECORD} is present but cannot be read ({exc}); an unread half of the "
                   "record is not an empty one")


def record():
    """-> ({url: (status, date, owner)}, hub_only, problems). The inline record, plus the excluded
    record wherever the tree has one; `hub_only` is the set of URLs only the excluded record declares.
    A URL declared in both is a problem: the public tree would judge it by the inline line and
    the excluded record by whichever came last."""
    out = parse_record(os.environ["EMITTED_RECORD"], "the inline record")
    if not out:
        unverified("the record parsed empty; refusing to certify against nothing")
    problems, hub_only = [], set()
    text = hub_record_text()
    if text is not None:
        for url, row in parse_record(text, HUB_RECORD).items():
            if url in out:
                problems.append(
                    f"{url} is declared in both the inline record and {HUB_RECORD}: keep the line "
                    "where its emitters are (inline if any published file emits it)")
            else:
                out[url] = row
                hub_only.add(url)
    return out, hub_only, problems


# export-closure: absent-by-design scripts/export-public.sh — DATA, read and never run: its lists
#   say which files the export publishes. The export removes it, and in a tree without it every
#   file present counts as published, which over-reports and never under-reports.
EXPORT_SCRIPT = "scripts/export-public.sh"
CURATION_LISTS = ("TOP_ALLOW", "TOP_BLOCK", "DOCS_BLOCK", "DOCS_KEEP", "SCRIPTS_BLOCK",
                  "GITHUB_BLOCK", "COMMERCIAL_BLOCK", "MISC_BLOCK")
# What a list entry may look like for fnmatch to match it exactly as the shell's [[ == ]] does:
# path characters and `*`, nothing the two could read differently (`?`, `[`, quotes, `$`).
CURATION_TOKEN = re.compile(r"^[A-Za-z0-9._/@+*-]+$")


def publication_rule():
    """-> (published(path) -> bool, source): the rule `export-public.sh --manifest` applies to a
    path, read from that script's own lists and never restated here. A DOCS_KEEP entry is an exact
    path that ships out of a blocked directory; a path that matches a *_BLOCK entry as a glob, or
    sits below one, does not ship; every other path ships when its first segment is in TOP_ALLOW.

    With no export script, the tree IS the export, so every file present counts as published. A
    script that is present but unreadable, or whose lists this reader cannot follow (missing,
    defined twice, appended to elsewhere, an empty TOP_ALLOW, an entry the shell would expand
    differently), is UNVERIFIED: a curation nobody could read certifies nothing."""
    try:
        with open(EXPORT_SCRIPT, encoding="utf-8") as fh:
            src = fh.read()
    except FileNotFoundError:
        return (lambda path: True), f"the tree itself ({EXPORT_SCRIPT} is absent, as in a public export)"
    except (OSError, UnicodeDecodeError) as exc:
        unverified(f"{EXPORT_SCRIPT} is present but cannot be read ({exc}); which files the export "
                   "publishes is unknown")
    lists = {}
    for name in CURATION_LISTS:
        bodies = re.findall(r"^" + name + r"=\((?:\)|\n(.*?)^\))", src, re.S | re.M)
        if len(bodies) != 1 or len(re.findall(r"^\s*" + name + r"=", src, re.M)) != 1 \
                or re.search(r"\b" + name + r"\+=", src):
            unverified(f"{EXPORT_SCRIPT}: {name} is not one plain list defined once; the curation "
                       "changed shape and which files it publishes cannot be read")
        tokens = [t for ln in bodies[0].splitlines() for t in ln.split("#", 1)[0].split()]
        odd = [t for t in tokens if not CURATION_TOKEN.match(t)]
        if odd:
            unverified(f"{EXPORT_SCRIPT}: {name} holds {odd[0]!r}, which this reader cannot match "
                       "the way the shell does")
        lists[name] = tokens
    if not lists["TOP_ALLOW"]:
        unverified(f"{EXPORT_SCRIPT}: TOP_ALLOW parsed empty; refusing to say nothing publishes")
    keep, allow = set(lists["DOCS_KEEP"]), set(lists["TOP_ALLOW"])
    blocks = [b for name in CURATION_LISTS if name.endswith("_BLOCK") for b in lists[name]]

    def published(path):
        blocked = path not in keep and any(
            fnmatch.fnmatchcase(path, b) or fnmatch.fnmatchcase(path, b + "/*") for b in blocks)
        return not blocked and path.split("/", 1)[0] in allow

    return published, f"{EXPORT_SCRIPT} ({', '.join(CURATION_LISTS)})"


def curated_out_inline(urls, inline):
    """-> problems: an INLINE row whose every emitter is curated out. The inline record ships,
    so in the public tree that row is "declared and nothing emits it"; it belongs in the excluded
    record. Emitted by nothing at all is reported by the record check, not here. With no export
    script every file present counts as published, so nothing is reported."""
    emitted = sorted(u for u in inline if u in urls)
    if not emitted:
        return []
    published, source = publication_rule()
    problems = []
    for url in emitted:
        if not any(published(os.path.normpath(p)) for p, _ in urls[url]):
            sites = ", ".join(f"{p}:{i}" for p, i in urls[url][:3])
            problems.append(
                f"{url} is declared inline, but every file that emits it is curated out ({sites}; "
                f"publication read from {source}). The public tree would read it as declared and "
                f"emitted by nothing: move the line to {HUB_RECORD}")
    return problems


def published_in_hub_record(urls, hub_only):
    """-> problems: a URL only the excluded record declares that a PUBLISHED file emits. The public
    tree reads the inline record alone, so there that URL is undeclared; certifying it in the excluded
    record would be a green that the export turns red, and a way to whitelist a page through the one
    file the export drops."""
    emitted = sorted(u for u in hub_only if u in urls)
    if not emitted:
        return []
    published, source = publication_rule()
    problems = []
    for url in emitted:
        sites = [f"{p}:{i}" for p, i in urls[url] if published(os.path.normpath(p))]
        if sites:
            problems.append(
                f"{url} is declared only in {HUB_RECORD}, but a published file emits it "
                f"({', '.join(sites[:3])}; publication read from {source}). The public tree would "
                "find it undeclared: declare it in the inline record")
    return problems


BLOB = re.compile(r"^https://github\.com/([^/]+)/([^/]+)/blob/")


def probe_target(url):
    """⛔ UN `blob` DE GITHUB NO SE SONDEA POR `github.com`, Y ESTE DETALLE ES EL QUE ESCONDIÓ EL
    BLOQUEANTE. `https://github.com/<org>/<repo>/blob/main/<lo-que-sea>` contesta 200 aunque el
    fichero NO EXISTA — lo sirve la aplicación web, no el contenido. Se descubrió el 2026-08-18
    porque un control con una ruta INVENTADA (`ESTO-NO-EXISTE-abc123.md`) también daba 200: sin ese
    control, ocho enlaces rotos de la copy de lanzamiento se habrían certificado como sanos.
    `raw.githubusercontent.com` sí distingue: la misma ruta inventada da 404."""
    m = BLOB.match(url)
    if not m:
        return url
    resto = url[m.end():]
    return f"https://raw.githubusercontent.com/{m.group(1)}/{m.group(2)}/{resto}"


def resolves(u):
    """(host, addrs) del URL. NO es una prueba de salud: separa «no hay registro» de «no contesta».

    ⛔ ESCRITA EL 2026-08-20, y digo por qué: llegó a `main` con la restauración de
    #824 **llamada cuatro veces y sin definir**, así que `lint:emitted-urls` moría con
    `NameError` y, por estar en el carril rápido, tumbaba el push de CUALQUIER rama. Su
    contrato no lo he inventado: lo fijan las tres casillas del propio auto-test —un nombre que
    siempre resuelve, uno que la RFC 2606 reserva para que nunca pueda, y una cadena sin host—
    más el uso de producción `host, addrs = resolves(url)`. Si el autor la quería de otra forma,
    esas tres casillas son el sitio donde decirlo.
    """
    host = urlsplit(u).hostname or ""
    if not host:
        return "", []          # sin host no hay resolución que fallar: [] no es un fallo aquí
    try:
        infos = socket.getaddrinfo(host, None)
    except (socket.gaierror, UnicodeError, OSError):
        # ⛔ `UnicodeError` NO es subclase de `OSError`, así que un `except OSError` la deja subir
        #    y MATA el gate. La levanta el codificador IDNA ANTES de llegar al resolutor cuando una
        #    etiqueta pasa del límite. Medido el 2026-08-20: 'a'*300 + '.example' → UnicodeError,
        #    'xn--' y 'no-such.invalid' → gaierror. La captura de tres la tenía el autor original
        #    (`7a21c5bef`) y yo la perdí al reescribir; me la devolvió otro carril comparando
        #    ambas versiones. El caso de abajo existe para que no se pierda una tercera vez.
        return host, []        # NXDOMAIN, resolutor mudo o host imposible: lo dice quien llama
    return host, sorted({i[4][0] for i in infos})


def probe(url, attempts=5):
    """Five attempts, following redirects. ONE IS NOT A MEASUREMENT: on the day this was
    written a single curl to this very host returned 000 under local load and was read as
    'the host does not exist', which deleted a working declaration elsewhere."""
    url = probe_target(url)
    codes = []
    for _ in range(attempts):
        try:
            r = subprocess.run(
                ["curl", "-sSL", "-o", "/dev/null", "-w", "%{http_code}", "--max-time", "25", url],
                capture_output=True, text=True, timeout=40)
            codes.append(r.stdout.strip() or "000")
        except (OSError, subprocess.SubprocessError):
            codes.append("000")
    answered = [c for c in codes if c != "000"]
    return (answered[-1] if answered else "000"), codes


def fixture_run(profile, emits, inline=None, hub_record=None, classifier=True, exporter=None,
                files=None, unreadable=None, links=None):
    """Runs THIS gate, whole and unmodified except for its inline record when `inline` is given,
    inside a throwaway tree of the named profile -> (exit code, everything it printed).

    The profile is built the way scripts/hub-leg.sh --classify reads a tree, never declared to
    the gate: `public` carries the marker sentence the export stamps and no hub-only path, `hub`
    carries a hub-only path, `unknown` carries neither. `classifier=False` leaves the classifier
    out of the tree, so the gate cannot ask. The tree's README.md emits `emits`, one per line.
    `exporter` is the text of the tree's export script (itself a hub-only path), and `files` maps
    further relative paths to their text, so that an emitter can sit where the curation drops it.

    The inline record is swapped only where a case needs rows of its own: the real record ages
    and grows, and a case about the record FILE must not start failing the day a real row goes
    stale. A case about the real declaration keeps the real record (inline=None)."""
    src = open(os.path.join("scripts", "check-emitted-urls.sh"), encoding="utf-8").read()
    if inline is not None:
        src, n = re.subn(r'(?ms)^EMITTED_RECORD="[^"]*"$', lambda m: 'EMITTED_RECORD="' + inline + '"', src)
        if n != 1:
            return None, "fixture: the inline record block was not found exactly once"
    with tempfile.TemporaryDirectory(prefix="emitted-urls-selftest.") as t:
        os.makedirs(os.path.join(t, "scripts"))
        with open(os.path.join(t, "scripts", "check-emitted-urls.sh"), "w", encoding="utf-8") as fh:
            fh.write(src)
        if classifier:
            shutil.copy(os.path.join("scripts", "hub-leg.sh"), os.path.join(t, "scripts", "hub-leg.sh"))
        if profile == "public":
            sig = subprocess.run(["bash", os.path.join("scripts", "hub-leg.sh"), "--marker-signature"],
                                 capture_output=True, text=True, timeout=60).stdout
            with open(os.path.join(t, "PUBLIC-EXPORT.md"), "w", encoding="utf-8") as fh:
                fh.write("# Public export\n\n" + sig)
        elif profile == "hub":
            os.makedirs(os.path.join(t, "design"))
        with open(os.path.join(t, "README.md"), "w", encoding="utf-8") as fh:
            fh.write("\n".join(emits) + "\n")
        if hub_record is not None:
            os.makedirs(os.path.join(t, "docs"), exist_ok=True)
            with open(os.path.join(t, "docs", "emitted-urls-hub-record.txt"), "w", encoding="utf-8") as fh:
                fh.write(hub_record)
        if exporter is not None:
            with open(os.path.join(t, "scripts", "export-public.sh"), "w", encoding="utf-8") as fh:
                fh.write(exporter)
        for rel, text in (files or {}).items():
            path = os.path.join(t, *rel.split("/"))
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(text)
        for rel, target in (links or {}).items():     # a listed source whose target is gone
            path = os.path.join(t, *rel.split("/"))
            os.makedirs(os.path.dirname(path), exist_ok=True)
            os.symlink(target, path)
        for rel in unreadable or ():
            os.chmod(os.path.join(t, *rel.split("/")), 0)   # listed, not readable (a non-root uid)
        r = subprocess.run(["bash", os.path.join(t, "scripts", "check-emitted-urls.sh")],
                           capture_output=True, text=True, timeout=120)
        for rel in unreadable or ():                        # so the throwaway tree can be removed
            path = os.path.join(t, *rel.split("/"))
            os.chmod(path, 0o755 if os.path.isdir(path) else 0o644)
        return r.returncode, r.stdout + r.stderr


def selftest():
    ok = True

    def expect(name, cond):
        nonlocal ok
        print(("selftest ok: " if cond else "selftest FAIL: ") + name)
        ok = ok and cond

    expect("a bare host is a URL", URL.findall("see https://docs.olivares.ai for more") ==
           ["https://docs.olivares.ai"])
    expect("a path is kept whole", URL.findall("https://docs.olivares.ai/a/b-c/") ==
           ["https://docs.olivares.ai/a/b-c/"])
    expect("a trailing sentence full stop is not part of the URL",
           URL.findall("go to https://docs.olivares.ai/x.")[0].rstrip(".") ==
           "https://docs.olivares.ai/x")
    expect("a quote ends the literal",
           URL.findall('url = "https://docs.olivares.ai/y" // note') == ["https://docs.olivares.ai/y"])
    expect("a foreign host is not ours", URL.findall("https://github.com/olivaresai/olivares") == [])
    expect("test files are excluded", is_test("stability_test.go") and is_test("client.test.ts")
           and not is_test("stability.go"))
    # The 000 discriminator, calibrated in BOTH directions with no network: a name that always
    # resolves and one that RFC 2606 reserves so it never can. If either side stopped holding,
    # the reading printed beside every probed URL would be decoration.
    expect("a resolvable host reports addresses",
           resolves("https://localhost/x")[1] != [])
    expect("a reserved-nonexistent host reports none",
           resolves("https://no-such-host.invalid/x")[1] == [])
    expect("a URL with no host is not a resolver failure",
           resolves("not a url")[1] == [])
    expect("an impossible IDNA label is not a crash",
           resolves("https://" + "a" * 300 + ".example/x")[1] == [])
    # The one that matters: the record must be able to go stale.
    old = datetime.date(2026, 1, 1)
    # --- la familia del org, y la conversión que impide el falso 200 --------------------
    expect("the raw install one-liner is an emitted URL",
           ORG_URL.findall(
               "curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install.sh | sh")
           == ["https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install.sh"])
    expect("a blob deep link is an emitted URL",
           ORG_URL.findall("see https://github.com/olivaresai/olivares/blob/main/CONTRIBUTING.md now")
           == ["https://github.com/olivaresai/olivares/blob/main/CONTRIBUTING.md"])
    expect("another org is not ours",
           ORG_URL.findall("https://github.com/someoneelse/olivares/blob/main/x.md") == [])
    # ⛔ LA CELDA QUE MÁS IMPORTA DE ESTE FICHERO. Sin la conversión, un `blob` inexistente contesta
    #    200 —lo sirve la aplicación web de GitHub, no el contenido— y ocho enlaces rotos de la copy
    #    de lanzamiento se certificarían como sanos. Se descubrió con una ruta INVENTADA que también
    #    daba 200.
    expect("a github blob is probed through raw.githubusercontent",
           probe_target("https://github.com/olivaresai/olivares/blob/main/a/b.md")
           == "https://raw.githubusercontent.com/olivaresai/olivares/main/a/b.md")
    expect("a raw URL is probed as it is",
           probe_target("https://raw.githubusercontent.com/olivaresai/olivares/main/x.sh")
           == "https://raw.githubusercontent.com/olivaresai/olivares/main/x.sh")
    expect("a non-blob github URL is probed as it is",
           probe_target("https://github.com/olivaresai/olivares/releases/tag/v0.2.0")
           == "https://github.com/olivaresai/olivares/releases/tag/v0.2.0")
    expect("an olivares.ai URL is untouched by the conversion",
           probe_target("https://olivares.ai/updates") == "https://olivares.ai/updates")

    # --- the record split: an excluded record, read or refused by the profile of the tree ------
    # The inline record SHIPS, so a line whose only emitters are curated out cannot live there: in
    # the public tree it reads as "declared and nothing emits it". Such lines live in the excluded
    # record, which the export curates out. Its absence is SCOPED only where the tree PROVES it is
    # the public export; a development tree that lost it, or a tree nobody can classify, is never green.
    day = datetime.date.today().isoformat()
    shipped = "https://olivares.ai/selftest-inline"
    hub_only = "https://github.com/olivaresai/selftest-hub-only"
    inline = f"{shipped} 200 {day}"
    rc, out = fixture_run("public", [shipped], inline=inline)
    expect("a public tree without the excluded record says SCOPED and exits 0",
           rc == 0 and "SCOPED" in out and "docs/emitted-urls-hub-record.txt" in out)
    rc, out = fixture_run("hub", [shipped], inline=inline)
    expect("a development tree without the excluded record is BROKEN, exit 1, never green",
           rc == 1 and "BROKEN" in out and "docs/emitted-urls-hub-record.txt" in out
           and "OK check-emitted-urls" not in out)
    rc, out = fixture_run("unknown", [shipped], inline=inline)
    expect("an unclassifiable tree without the excluded record is UNVERIFIED, exit 2, never green",
           rc == 2 and "UNVERIFIED" in out and "OK check-emitted-urls" not in out)
    rc, out = fixture_run("public", [shipped], inline=inline, classifier=False)
    expect("a marked tree whose classifier cannot run is not proven public: exit 2",
           rc == 2 and "UNVERIFIED" in out and "OK check-emitted-urls" not in out)
    # A tree's export script says what it publishes (see the inline invariant below), so the `hub` profile
    # cases carry a small one: README.md publishes, docs/launch does not.
    curation = ("TOP_ALLOW=(\n  README.md docs scripts\n)\nTOP_BLOCK=(\n  design\n)\n"
                "DOCS_BLOCK=(\n  docs/launch\n  docs/emitted-urls-hub-record.txt\n)\n"
                "DOCS_KEEP=(\n)\nSCRIPTS_BLOCK=(\n)\nGITHUB_BLOCK=(\n)\nCOMMERCIAL_BLOCK=()\n"
                "MISC_BLOCK=(\n)\n")
    dropped = {"docs/launch/copy.md": hub_only + "\n"}
    hub_line = f"{hub_only} 404 {day} selftest-fixture-owner\n"
    rc, out = fixture_run("hub", [shipped], inline=inline, exporter=curation, files=dropped,
                          hub_record="# a comment line\n\n" + hub_line)
    expect("an excluded-record line for a curated-out emitter is honoured, its non-200 named with its owner",
           rc == 0 and "OK check-emitted-urls" in out and hub_only in out
           and "selftest-fixture-owner" in out)
    rc, out = fixture_run("hub", [shipped], inline=inline, hub_record=f"{shipped} 200 {day}\n")
    expect("a URL declared in both records is refused",
           rc == 1 and "BROKEN" in out and "declared in both" in out)
    # A fixture record here: the page is refused by any record that does not declare it.
    v2691 = "https://github.com/olivaresai/olivares/releases/tag/v26.9.1"
    rc, out = fixture_run("public", [shipped, v2691], inline=inline)
    expect("an emitted v26.9.1 release link with no declaration is refused",
           rc == 1 and f"{v2691} is emitted" in out and "not in the record" in out)
    # And the REAL records, inline and excluded: no v26.9.1 release exists, so neither record
    # declares its page, and the real inline record refuses a README that emits it.
    real_hub = open(HUB_RECORD, encoding="utf-8").read() if os.path.exists(HUB_RECORD) else ""
    declared = set(parse_record(os.environ["EMITTED_RECORD"], "the inline record"))
    declared |= set(parse_record(real_hub, HUB_RECORD))
    expect("the real records declare no v26.9.1 release page", v2691 not in declared)
    rc, out = fixture_run("public", [v2691])
    expect("the real inline record refuses an emitted v26.9.1 release link",
           rc == 1 and f"{v2691} is emitted" in out and "not in the record" in out)

    # --- the inline invariant: a URL that a PUBLISHED file emits is declared inline, never only in
    # the excluded record. Otherwise that record certifies it, the public tree finds it undeclared, and a
    # release page nobody published could be whitelisted through the file the export drops.
    # Publication is read from the export script's own lists, by the rule its --manifest applies;
    # a tree with no export script counts every file present as published.
    rc, out = fixture_run("hub", [shipped, v2691], inline=inline, exporter=curation,
                          hub_record=f"{v2691} 404 {day} selftest-fixture-owner\n")
    expect("a published emitter's URL declared only in the excluded record is BROKEN, exit 1",
           rc == 1 and "BROKEN" in out and f"{v2691} is declared only in" in out
           and "README.md:2" in out)
    rc, out = fixture_run("hub", [shipped, hub_only], inline=inline, hub_record=hub_line)
    expect("with no export script every file present counts as published: that line is BROKEN",
           rc == 1 and "BROKEN" in out and f"{hub_only} is declared only in" in out)
    rc, out = fixture_run("hub", [shipped], inline=inline, files=dropped, hub_record=hub_line,
                          exporter="# an export script whose lists are gone\n")
    expect("an export script whose lists cannot be read is UNVERIFIED, exit 2, never green",
           rc == 2 and "UNVERIFIED" in out and "OK check-emitted-urls" not in out)
    # --- and the reverse direction (added 2026-09-23): an INLINE row whose every emitter is
    # curated out reads in the public tree as "declared and nothing emits it", so in a tree that
    # carries the export's lists it is BROKEN and belongs in the excluded record. One published emitter
    # keeps it inline; with no export script every file counts as published and nothing moves.
    only_dropped = inline + f"\n{hub_only} 404 {day} selftest-fixture-owner"
    rc, out = fixture_run("hub", [shipped], inline=only_dropped, exporter=curation, files=dropped,
                          hub_record="# empty\n")
    expect("an inline row whose every emitter is curated out is BROKEN, exit 1: it belongs in the excluded record",
           rc == 1 and "BROKEN" in out and f"{hub_only} is declared inline" in out
           and "docs/launch/copy.md:1" in out)
    rc, out = fixture_run("hub", [shipped, hub_only], inline=only_dropped, exporter=curation,
                          files=dropped, hub_record="# empty\n")
    expect("the same inline row with a published emitter as well stays inline -> OK",
           rc == 0 and "OK check-emitted-urls" in out)
    rc, out = fixture_run("hub", [shipped], inline=only_dropped, files=dropped, hub_record="# empty\n")
    expect("with no export script every emitter counts as published: the inline row stays -> OK",
           rc == 0 and "OK check-emitted-urls" in out)
    own = os.path.join("scripts", "export-public.sh")
    if os.path.exists(own):
        with open(own, encoding="utf-8") as fh:
            own_text = fh.read()
        rc, out = fixture_run("hub", [shipped, v2691], inline=inline, exporter=own_text,
                              files=dropped,
                              hub_record=f"{v2691} 404 {day} selftest-fixture-owner\n" + hub_line)
        expect("this tree's own export lists decide: README.md publishes, docs/launch does not",
               rc == 1 and f"{v2691} is declared only in" in out
               and f"{hub_only} is declared only in" not in out)
    else:
        profile = subprocess.run(["bash", os.path.join("scripts", "hub-leg.sh"), "--classify",
                                  "--root", os.getcwd()], capture_output=True, text=True,
                                 timeout=60).stdout.strip()
        expect("no export script here, because this tree is the public export that removed it",
               profile == "public")

    # --- an emitter the census cannot read is COULD NOT LOOK (added 2026-09-24). A file listed and
    # then skipped is a hole in the census, not a clean file: a forbidden emitter behind mode 000
    # would pass. One case per enumerator (code roots, copy roots, published surface files), each
    # beside its readable control, which must still refuse.
    undeclared = "https://olivares.ai/selftest-undeclared"
    org_undeclared = "https://github.com/olivaresai/selftest-undeclared"
    sites = [("core/x.go", f'const u = "{undeclared}"\n', undeclared),
             ("docs/launch/copy.md", org_undeclared + "\n", org_undeclared),
             (".github/ISSUE_TEMPLATE/x.yml", f"url: {undeclared}\n", undeclared)]
    if os.geteuid() == 0:
        expect("unreadable emitter: the mode-000 staging needs a non-root uid; not applicable at uid 0", True)
    else:
        for rel, text, url in sites:
            rc, out = fixture_run("hub", [shipped], inline=inline, hub_record="# empty\n", files={rel: text})
            expect(f"a readable forbidden emitter still refuses: {rel} -> BROKEN, exit 1",
                   rc == 1 and f"{url} is emitted" in out)
            rc, out = fixture_run("hub", [shipped], inline=inline, hub_record="# empty\n", files={rel: text},
                                  unreadable=[rel])
            expect(f"an unreadable emitter is COULD NOT LOOK: {rel} at mode 000 -> UNVERIFIED, exit 2, named",
                   rc == 2 and "UNVERIFIED" in out and rel in out and "OK check-emitted-urls" not in out)
        # One level up (added 2026-09-24): a directory the walk cannot list hides every emitter
        # in it, so it is COULD NOT LOOK too, never a smaller census. One per enumerator.
        for d, rel, text in (("core/zz", "core/zz/x.go", f'const u = "{undeclared}"\n'),
                             ("docs/launch/zz", "docs/launch/zz/copy.md", org_undeclared + "\n"),
                             (".github/zz", ".github/zz/x.yml", f"url: {undeclared}\n")):
            rc, out = fixture_run("hub", [shipped], inline=inline, hub_record="# empty\n", files={rel: text},
                                  unreadable=[d])
            expect(f"an unlistable directory is COULD NOT LOOK: {d} at mode 000 -> UNVERIFIED, exit 2, named",
                   rc == 2 and "UNVERIFIED" in out and d in out and "OK check-emitted-urls" not in out)
    # A listed source that cannot be examined (a dangling link) is COULD NOT LOOK at any uid.
    gone = ".github/ISSUE_TEMPLATE/gone.yml"
    rc, out = fixture_run("hub", [shipped], inline=inline, hub_record="# empty\n", links={gone: "nowhere.yml"})
    expect(f"a dangling listed source is COULD NOT LOOK: {gone} -> UNVERIFIED, exit 2, named",
           rc == 2 and "UNVERIFIED" in out and gone in out and "OK check-emitted-urls" not in out)

    expect("an old record is over the age limit",
           (datetime.date(2026, 8, 13) - old).days > MAX_AGE)
    print("selftest " + ("OK — every red case is red, every green case is green" if ok else "FAILED"))
    sys.exit(0 if ok else 1)


if SELFTEST:
    selftest()

urls = emitted()
if not urls:
    unverified("discovery found no emitted URLs at all; the roots or the pattern are wrong")

if PROBE:
    today = datetime.date.today().isoformat()
    print(f"check-emitted-urls: probing {len(urls)} emitted URL(s) (network) — {today}")
    bad = 0
    for url in sorted(urls):
        status, codes = probe(url)
        host, addrs = resolves(url)
        print(f"  {url}")
        print(f"      {status}  ({' '.join(codes)})")
        # Printed for EVERY url, not only the 000s: the reading is only worth anything next to
        # the hosts that DO resolve in the same pass.
        print(f"      dns: {host} -> " + (", ".join(addrs) if addrs
                                          else "NO ADDRESS RECORD (a 000 here is 'no server')"))
        owner = " fran-deployment-decision" if status != "200" else ""
        print(f"      record line: {url} {status} {today}{owner}")
        if status != "200":
            bad += 1
    print(f"check-emitted-urls: {bad} of {len(urls)} do not answer 200")
    sys.exit(1 if bad else 0)

rec, hub_only, problems = record()
problems += published_in_hub_record(urls, hub_only)
problems += curated_out_inline(urls, set(rec) - hub_only)
today = datetime.date.today()

for url, sites in sorted(urls.items()):
    if url not in rec:
        where = ", ".join(f"{p}:{i}" for p, i in sites[:3])
        problems.append(
            f"{url} is emitted ({where}) and is not in the record. Measure it with "
            f"`bash scripts/check-emitted-urls.sh --probe` and declare what it answers.")

for url, (status, when, owner) in sorted(rec.items()):
    if url not in urls:
        problems.append(f"{url} is declared and nothing emits it any more — drop the record line")
        continue
    try:
        measured = datetime.date.fromisoformat(when)
    except ValueError:
        problems.append(f"{url}: {when!r} is not an ISO date")
        continue
    age = (today - measured).days
    if age > MAX_AGE:
        problems.append(
            f"{url} was last measured {age} days ago (limit {MAX_AGE}). Re-measure with --probe.")
    if status != "200" and not owner:
        problems.append(f"{url} is declared {status} with no owner — a known-broken URL needs one")

if problems:
    print(f"BROKEN check-emitted-urls: {len(problems)} problem(s):")
    for p in problems:
        print(f"  · {p}")
    sys.exit(1)

broken = {u: v for u, v in rec.items() if v[0] != "200"}
print(f"OK check-emitted-urls: {len(urls)} emitted URL(s), every one declared and measured "
      f"within {MAX_AGE} days")
if broken:
    # NEVER SILENT. A known-broken URL that stops being mentioned becomes furniture.
    print(f"  ⚠ {len(broken)} of them do NOT answer 200 and are awaiting a deployment decision:")
    for u, (status, when, owner) in sorted(broken.items()):
        n = len(urls[u])
        print(f"      {status}  {u}  ({n} emission site(s), measured {when}, owner: {owner})")
PY
