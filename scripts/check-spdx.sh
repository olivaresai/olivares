#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# REUSE-IgnoreStart
# (This script legitimately contains the string SPDX-License-Identifier in its
#  comments, echoes and regexes; the marker above/below stops `reuse lint` from
#  mis-parsing those as license declarations. The file's own license is the
#  header two lines up, outside this block.)
#
# check-spdx.sh — verify every source file carries the correct SPDX header.
#
# COVERAGE — AND WHY IT IS NO LONGER AN ALLOWLIST OF EXTENSIONS (2026-08-14).
#
# This gate used to enumerate the extensions it wanted ('*.go' '*.ts' '*.js' …) and
# everything else was NOT SOURCE **by default, in silence**. That shape fails open, and it
# had already failed open twice:
#   * 2026-08-01: shell was missing, so two tracked scripts were invisible and the count did
#     not move when they landed.
#   * 2026-08-14: `.mjs` and `.cjs` were missing — 44 tracked files. Measured with the SAME
#     200 bytes (a LicenseRef-Olivares-Commercial header inside AGPL web/): as `web/x.js`
#     the gate answered `MISMATCH … rc=1`; renamed `web/x.mjs`, `web/x.cjs`, `web/x.mts` or
#     `web/x.cts`, all four answered `SPDX check OK: 4855 source files … rc=0`. One live
#     offender was sitting in that blind spot: scripts/gen-dodo-product-art.mjs declares
#     LicenseRef-Olivares-Internal in a zone the frontier maps to AGPL-3.0-only.
#     THE FIX FIRST WRITTEN HERE FOR IT — "the licence is what is wrong, rewrite the header
#     to AGPL-3.0-only" — WAS RETRACTED THE SAME DAY, by READING the file instead of the map.
#     It is the named debt D-2 below, and the reason is in it: relicensing that file would
#     have published the commercial price table it carries.
#
# Adding `.mjs` and `.cjs` to the old list would have fixed those 44 files and left the
# CLASS untouched: the next `.vue`, `.rs`, `.kt` or `.svelte` would be invisible on arrival,
# with nobody deciding anything — the same way a glob never expires (see D-1 below).
#
# So the list stopped being "what I look at" and became a THREE-WAY CLASSIFICATION of every
# tracked path, in classify() below:
#     source  -> the header is read and must match the module map
#     data    -> declared NOT source ON PURPOSE, with the reason written next to it
#     unknown -> THE GATE REFUSES TO RULE (exit 2) and names the extension
# Silence no longer means "not source"; silence now means STOP. The list can still go short,
# but it can no longer go short QUIETLY: the day an extension this file has never seen lands
# in the tree, the gate stops and makes a human write down which of the two buckets it is in.
# That is the only property that does not expire, because it does not depend on this file's
# author having predicted the language.
#
# The two directions the enumeration can break are covered by two different guards, because
# one cannot see the other: a NEW extension is caught by the `unknown` bucket, and a
# COLLAPSE of the enumeration itself (the walk breaking, the wrong root) is caught by the
# dated population floor at the bottom. Neither is a substitute for the other.
#
# License frontier (LICENSING.md — open core; the commercial enterprise edition ships separately):
#   core/  modules/  web/        => AGPL-3.0-only      (product, copyleft)
#   terraform-provider-olivares/ => AGPL-3.0-only      (first-party manage-as-code binary)
#   operator/                    => AGPL-3.0-only      (first-party K8s Operator, SCP-10)
#   sdk/   connectors/           => Apache-2.0         (permissive ecosystem)
#   clients/                     => Apache-2.0         (client SDKs + generator)
#   commercial/                  => LicenseRef-Olivares-Commercial (internal fulfilment backend; never exported)
#   cloud/                       => LicenseRef-Olivares-Commercial (cloud control plane; separate Go module)
#   cmd/                         => AGPL-3.0-only, WITHOUT EXCEPTION AS A RULE (see the debt below)
#
# THE RULE THAT USED TO BE HERE, AND WHY IT IS GONE (2026-08-08).
# This map carried `cmd/*/*_enterprise.go => LicenseRef-Olivares-Commercial`, described as
# "enterprise seam impls that live in cmd/ when enterprise/ is absent". It was not a
# description of the frontier; it was a HOLE IN IT with a comment on it. Two consequences,
# both measured:
#   * `task lint:spdx` was GREEN with 405 lines of commercial engine under cmd/ — the licence
#     gate had been TOLD that was correct, so it was not failing to notice.
#   * ADR-0020:39-41 (status: accepted) says the public repository "no longer contains the
#     enterprise/ tree, the //go:build enterprise cmd/olivares wiring", and the file sat at
#     position 191 of the 7,184-path export manifest, the ONLY one declaring a commercial
#     licence, shipping beside LICENSING.md:35 telling the reader commercial code is in a
#     "separate private repository — not in this repo".
# A GLOB NEVER EXPIRES: while that arm existed, the next `cmd/*/*_enterprise.go` would be
# blessed on arrival, with nobody deciding anything. What replaces it is ONE named path with
# a date and a reason, and a ratchet that makes it fail once its subject is gone.
#
# - Walks EVERY tracked path and classifies it (classify(), below).
# - Skips vendor/, node_modules/, dist/, build output, and generated files
#   (*.gen.go *_gen.go *.pb.go *_pb.go *.gen.ts, files marked
#    "Code generated ... DO NOT EDIT" or "@generated").
# - Verifies each file has a license identifier AND that it matches the module
#   the file lives in.
# - Exits non-zero, listing every offender (MISSING / MISMATCH / ORPHAN).
#
# Strict POSIX sh (runs under dash/busybox); no Node or Go runtime required.
# Filenames containing newlines are unsupported (pathological, policy-rejected).
#
# Usage:  scripts/check-spdx.sh [root]   (root defaults to repo root / CWD)

set -eu

ROOT="${1:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}"
cd "$ROOT"

# ⛔ LA VENTANA DEL SELFTEST: si `test-check-spdx.sh` esta plantando ahora mismo sus sondas en ESTE
# arbol, lo que este gate vea NO es el arbol — son los defectos que su hermano planta a proposito
# para la pierna R1 (`test-check-spdx.sh`), y el veredicto seria un ROJO FALSO contra codigo sano.
# Reproducido el 2026-08-24: con una sonda plantada, `MISSING …/leak.mjs` y rc=1; retirandola, rc=0.
#
# La respuesta correcta NO es ignorar esa ruta —abriria un agujero permanente y ademas rompe R1, que
# necesita que el gate SI la vea— sino la tercera respuesta que este guion ya da en otros tres
# sitios: **2 NO PUEDO MIRAR**. Un `2` no es un pase.
#
# ⛔ Y LAS CUATRO SALVAGUARDAS SON DEL CONTRASTE `sol max` DE que encontro tres agujeros altos
# en la primera version y tenia razon en los tres:
#   · UN FICHERO POR PID. El singleton no soportaba dos selftests: A escribia su PID, B lo pisaba, y
#     la limpieza de A abria la ventana con B aun plantando (C3).
#   · CADUCIDAD. Sin plazo, un selftest colgado —o cualquiera que escriba un PID vivo— dejaba el
#     gate mudo indefinidamente, que es aplazar el diagnostico para siempre (C1).
#   · ASCENDENCIA, NO VARIABLE DE ENTORNO. La salvedad para el dueño se comprobaba con
#     OLIVARES_SPDX_SELFTEST_PID, que cualquiera puede fijar: era falsificable (C2). La ascendencia
#     no se puede fingir — R1 invoca este gate COMO HIJO del selftest.
#   · Y la ventana la abre el selftest solo alrededor de R1, no durante toda la bateria (C1).
_spdx_gitdir="$(git rev-parse --git-dir 2>/dev/null || true)"
if [ -n "$_spdx_gitdir" ]; then
  # ¿es $1 un ancestro de este proceso? No se puede fingir.
  _spdx_es_ancestro() {
    _a=$$
    while [ -n "$_a" ] && [ "$_a" != "0" ] && [ "$_a" != "1" ]; do
      [ "$_a" = "$1" ] && return 0
      _a="$(awk '{print $4}' "/proc/$_a/stat" 2>/dev/null)" || return 1
      [ -z "$_a" ] && return 1
    done
    return 1
  }
  for _spdx_lock in "$_spdx_gitdir"/ng-spdx-selftest-inflight.*; do
    [ -f "$_spdx_lock" ] || continue
    _spdx_pid="${_spdx_lock##*.}"
    # caducidad: 600 s es 30x el coste medido de R1 (~20 s). Un candado mas viejo es residuo.
    _spdx_edad="$(( $(date +%s) - $(stat -c %Y "$_spdx_lock" 2>/dev/null || echo 0) ))"
    if [ "$_spdx_edad" -gt 600 ]; then continue; fi
    kill -0 "$_spdx_pid" 2>/dev/null || continue
    _spdx_es_ancestro "$_spdx_pid" && continue   # somos su R1: se nos deja ver las sondas
    echo "SPDX check CANNOT LOOK: the SPDX selftest (pid $_spdx_pid) is planting probes in this tree" >&2
    echo "  right now, so anything found here would describe ITS fixtures and not the tree." >&2
    echo "  This is not a licence violation and it is not a pass. Re-run when it finishes." >&2
    exit 2
  done
fi

if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  enterprise_files="$(git -c core.quotepath=off ls-files -- enterprise/)"
  if [ -n "$enterprise_files" ]; then
    {
      echo "ERROR: enterprise/ must not exist in the public repository."
      echo "Commercial code lives in the private enterprise overlay repository after the enterprise split; keeping it here breaks overlay assembly."
      echo "Tracked files under enterprise/:"
      printf '%s\n' "$enterprise_files"
    } >&2
    exit 1
  fi
fi

# --- file discovery ----------------------------------------------------------
# Prefer git (fast, respects .gitignore); fall back to find for non-git trees
# (e.g. a release tarball running the same gate). Newline-delimited output.
#
# NO PATHSPEC, NO -name FILTER, in either branch. The filter used to live HERE, which is what
# made "not listed" indistinguishable from "not source"; it now lives in classify(), where
# not-source is a written decision instead of an omission. Keeping the two branches
# unfiltered also means they can no longer disagree: the 2026-08-01 note asking the next
# author to "add it to BOTH listings below" was itself a description of a defect waiting to
# happen — there is now one listing, and it is the classifier.
list_files() {
  if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    git -c core.quotepath=off ls-files --cached --others --exclude-standard
  else
    find . \
      \( -path './vendor' -o -path '*/vendor' \
         -o -name node_modules -o -name dist -o -name build -o -name target \
         -o -name .astro -o -path './.git' \) -prune -o \
      -type f -print \
      | sed 's|^\./||'
  fi
}

# Paths that are never source-of-truth — ignored even if listed.
is_excluded_path() {
  case "$1" in
    # Terraform provider example SNIPPETS. tfplugindocs renders these files
    # verbatim into the published provider documentation, so an inline SPDX
    # header would appear inside the rendered `terraform import` example. They
    # are licensed by a REUSE.toml annotation instead — added the same day this
    # exclusion was, because they SHIP (measured: all 7 are in the export
    # manifest) and until then they carried no licence at all, inline or
    # annotated. Excluding them here without that annotation would have hidden
    # the compliance hole rather than closed it.
    terraform-provider-olivares/examples/*) return 0 ;;
    */vendor/*|vendor/*) return 0 ;;
    */node_modules/*|node_modules/*) return 0 ;;
    */dist/*|dist/*) return 0 ;;
    */build/*|build/*) return 0 ;;
    */target/*|target/*) return 0 ;; # Maven build output (clients/java) — not source
    */.astro/*|.astro/*) return 0 ;; # Astro's generated content-collection cache (not source)
    design/*) return 0 ;; # brand/design deliverables (boards, canvases) — assets, not product source
    *.gen.go|*_gen.go|*.pb.go|*_pb.go|*_grpc.pb.go) return 0 ;;
    *.gen.ts|*.gen.tsx|*.gen.js) return 0 ;;
    *.min.js|*.min.ts|*.min.css) return 0 ;;
    */mocks/*|*_mock.go|*.mock.ts) return 0 ;;
  esac
  return 1
}

# --- classify: source / data / unknown ---------------------------------------
# THE POINT OF THIS FUNCTION IS THE THIRD ANSWER. Two buckets would have been the old
# defect with more typing: whatever the author forgot would land in "data" and stay quiet.
# `unknown` is what makes forgetting LOUD.
#
# Echoes exactly one of: source | data | unknown.
#
#   source  — the SPDX header is read and must match the module map below.
#   data    — deliberately NOT header-checked. Each group carries its reason. Most of these
#             are licensed centrally by REUSE.toml (the repo-wide annotation covers .md,
#             .txt, .yaml/.yml, .toml, .json, .mod, .sum, Dockerfile, CODEOWNERS, the
#             dotfiles…), so "data" means "licensed elsewhere", NOT "unlicensed".
#   unknown — no rule. The gate refuses to rule on the tree until someone writes one.
#
# Order matters: source patterns first, then the by-NAME rules (Dockerfile.release would
# otherwise be read as an extension called "release"), then by extension.
classify() {
  base=${1##*/}
  case "$base" in
    # ---- SOURCE -------------------------------------------------------------
    # Go, the TypeScript/JavaScript family INCLUDING the ESM/CJS spellings, markup and
    # styles, Python, Java, shell. .mjs/.cjs/.mts/.cts were the 2026-08-14 blind spot;
    # they are written out rather than globbed so the family is visible to a reader.
    *.go|*.ts|*.tsx|*.mts|*.cts|*.js|*.jsx|*.mjs|*.cjs|*.html|*.css|*.py|*.java|*.sh|*.awk)
      echo source; return ;;
    # `.awk` entró el 2026-08-30 y es FUENTE por la misma razón que `.sh`: son programas, no datos
    # — `scripts/lib/sigpipe-m2.awk` analiza líneas de shell y decide si una tubería cuenta como
    # deuda. Lo destapó el primer fichero que estrenó la extensión: hasta entonces el gate no
    # respondía «violación» ni «OK», sino **2 — no sé si esto es fuente**, que es el fail-closed que
    # este `classify()` existe para dar. Un `.awk` sin cabecera es una violación como cualquier otra.
    # Promoted 2026-08-14, and the promotion is a RECORD, not a policy change: every one of
    # these already carried a module-correct inline header, measured file by file before the
    # line was written (sql 101/101, rego 6/6, tf 2/2 outside the annotated examples,
    # proto 2/2, lua 1/1, astro 1/1 — zero MISSING, zero MISMATCH, zero ORPHAN). The tree
    # was already treating them as source; only the gate was not.
    *.sql|*.rego|*.tf|*.proto|*.lua|*.astro)
      echo source; return ;;
    # ---- NOT SOURCE, by NAME ------------------------------------------------
    # Container/build recipes and project metadata. `Dockerfile.*` covers the five variants
    # (release, fips, stig, agentops, ebpf-source) whose suffixes are NOT extensions.
    Dockerfile|Dockerfile.*|Containerfile|Makefile|Taskfile.yml|CODEOWNERS) echo data; return ;;
    # License/notice texts and legal boilerplate: REUSE.toml marks these CC0-1.0 precisely
    # so nobody demands a licence header for a licence.
    LICENSE|NOTICE|DCO|CLA.md|LICENSING.md) echo data; return ;;
    # ---- NOT SOURCE, by extension -------------------------------------------
    # Prose and docs (licensed by the REUSE.toml repo-wide and docs-site/** annotations).
    *.md|*.mdx|*.txt) echo data; return ;;
    # Structured data, config and manifests — including the Go module metadata.
    *.json|*.json5|*.jsonc|*.jsonl|*.jsonlog|*.ndjson|*.yaml|*.yml|*.toml|*.xml) echo data; return ;;
    *.mod|*.sum|*.work|*.example|*.conf|*.service|*.webmanifest|*.node-version) echo data; return ;;
    # Tabular/log FIXTURES and captured evidence: connector testdata, audit patches.
    *.csv|*.csvlog|*.tsv|*.log|*.patch|*.baseline|*.unpublished-baseline) echo data; return ;;
    # Build stamps: a line of digest + count written by a build task, consumed by a gate.
    # DATA, and the reason is that a header would change the file: `bundle-source.stamp` is
    # compared byte for byte against a freshly computed digest, so prepending an SPDX line
    # would make the comparison fail forever. A marker is a marker, not a program — the same
    # call as `.olivares-public-export` below. Licensed by the REUSE.toml repo-wide annotation.
    *.stamp) echo data; return ;;
    # Keys and signed blobs. A public key with a comment header stops being that key.
    *.pub|*.pem|*.mcpb) echo data; return ;;
    # CAPTURED WIRE PAYLOADS. Same call as the keys above and for the same reason: these are the
    # exact bytes a NAMED binary emitted, replayed byte for byte by a test, so prepending an SPDX
    # line would stop them being that payload — a protobuf with a comment in front does not
    # unmarshal, and the test that replays it would be measuring the header. First case:
    # connectors/grok/testdata/otlp-traces-grok-1.0.5-*.bin, the OTLP export of
    # `grok 1.0.5 (5115b46bc9)`. Licensed by the REUSE.toml repo-wide annotation.
    # Binary assets: images, fonts, video, archives, subtitles.
    *.bin) echo data; return ;;
    *.png|*.svg|*.gif|*.webp|*.ico|*.woff|*.woff2|*.ttf|*.eot) echo data; return ;;
    *.mp4|*.webm|*.vtt|*.srt|*.zip|*.gz|*.tgz|*.pdf|*.thumbnail) echo data; return ;;
    # Tool dotfiles (.gitignore, .gitattributes, .dockerignore, .helmignore,
    # .prettierignore, .editorconfig): all named in the REUSE.toml repo-wide annotation.
    *.gitignore|*.gitattributes|*.dockerignore|*.helmignore|*.prettierignore|*.editorconfig)
      echo data; return ;;
    # The curation pipeline's own stamp. Not in this repository at all — it exists only in a
    # curated export tree, which is the second tree this gate runs in, and the `unknown`
    # bucket found it on its FIRST live run there (2026-08-14): a marker file is a marker,
    # not a program. Worth keeping as the record of what the third answer is for.
    .olivares-public-export) echo data; return ;;
    # SCAFFOLD TEMPLATES — and this one is a DECISION, not an oversight, so it is written
    # down here instead of being left to silence. sdk/scaffold/templates/*.tmpl and the Helm
    # _helpers.tpl render into SOMEBODY ELSE'S project: README.md.tmpl, gitignore.tmpl and
    # go.mod.tmpl deliberately carry no header, because an inline SPDX line would stamp
    # Olivares' identifier onto a user's generated files. The ones that DO carry a header
    # (main.go.tmpl, source.go.tmpl, …) carry a TEMPLATED one — `[[.Year]] <your name>` —
    # which is the same reasoning from the other side. They are licensed by the REUSE.toml
    # `sdk/**` override (Apache-2.0), exactly like terraform-provider-olivares/examples/**.
    # Measured 2026-08-14: 8 of the 11 .tmpl carry a header, 3 do not, and the 3 are the
    # three that render into the user's project. Promoting .tmpl to `source` would have made
    # this gate demand the header that decision forbids.
    *.tmpl|*.tpl) echo data; return ;;
    # ---- EXTENSION-LESS: a CONTENT test, not a name test --------------------
    # No dot in the basename. Guessing from the name is how .githooks/pre-push and
    # .githooks/commit-msg — two real shell programs, both AGPL-headed — sat outside every
    # enumeration this gate ever had. The interpreter line is the property that matters, so
    # it is what gets read: a shebang makes it a program and therefore source. Without one
    # it is a datum (LICENSE, NOTICE, DCO, RELEASE-VERSION, CODEOWNERS, _headers,
    # PLACEHOLDER, VERSION, a fuzz corpus seed — measured: 23 of the 25 today).
    *.*)
      # Has an extension and matched no rule above. THE THIRD ANSWER.
      echo unknown; return ;;
    *)
      if [ "$(head -n 1 "$1" 2>/dev/null | cut -c1-2)" = '#!' ]; then
        echo source
      else
        echo data
      fi
      return ;;
  esac
}

# Generated-content sniff (first 30 lines) for files not caught by name.
is_generated_content() {
  head -n 30 "$1" 2>/dev/null | grep -Eq \
    'Code generated .*DO NOT EDIT|@generated|Autogenerated by|DO NOT EDIT\.'
}

# ⛔ LA DEUDA D-1 SE PAGO EL 2026-08-30: `cmd/olivares/circuitbreaker_enterprise.go` se movio a
# `cmd-overlay/olivares/` del overlay privado, que es el final de estado que este bloque debia.
# Lo forzo el ensayo T3-B: con el submodulo `./public` del espejo apuntando ya al export
# publicado, el enterprise no compilaba (`undefined: cbEngine`). El detalle esta en
# the export curation script, donde vivia la curacion que lo tapaba.
#
# Este comentario NO es la deuda: la deuda y su trinquete se han retirado enteros, que es lo
# que el propio trinquete manda hacer el dia que el sujeto desaparece.

# --- NAMED, DATED LICENCE DEBT — D-2 (2026-08-14) ------------------------------------------
# ONE path, in the same shape as D-1 above, and deliberately NOT `scripts/*.mjs`: the 16
# siblings of this file are 16/16 AGPL-3.0-only and must stay checkable. A pattern here would
# hand the whole extension back to the blind spot this gate has just stopped having.
#
# scripts/gen-dodo-product-art.mjs:2 declares LicenseRef-Olivares-Internal inside `scripts/*`,
# which the map below sends to AGPL-3.0-only. When the widened enumeration made the file
# visible, the fix first written for it was to rewrite that header to AGPL-3.0-only. THAT WAS
# WRONG, and what refuted it was READING THE FILE rather than the map: lines 118-143 are its
# CATALOG — the commercial price table, in dollars. Measured, 12 `price:` rows at 119-142:
# business-m $129/month, business-y $1,290/year, regulated-m $99/month, regulated-y $990/year,
# and eight more. AGPL-3.0-only is a licence to redistribute, so relicensing that file would
# have PUBLISHED the price list. The identifier is correct; what is wrong is where the file
# sits, and the cure is CURATION — its SCRIPTS_BLOCK entry in the export curation script — not
# relicensing.
#
# WHAT THIS ENTRY DOES AND DOES NOT CLAIM. It settles the LICENCE question only: the gate
# stops demanding an identifier whose meaning is "anyone may republish this". It does not
# claim the file is already out of the export. When this entry was written it did not: the
# manifest still selected the path at position 6,045 of 7,554. THE CURATION HAS SINCE LANDED
# (8b3b27d9e, its SCRIPTS_BLOCK entry), so the manifest no longer selects it — which is exactly
# what makes the declaration below true, and owed.
#
# THE DECLARATION D-2 PROMISED, now that its condition holds. This entry deliberately went
# UNannotated while the export still SHIPPED the path: the declaration is itself checked, and one
# that names a published path is STALE — a defect, not a pass. The author wrote that it "becomes
# TRUE, and belongs here, in the same commit that adds the SCRIPTS_BLOCK entry". That curation
# landed in a DIFFERENT commit and this half did not follow, so every reference below went
# unresolved and the export gate has been red on main ever since. The claim is unchanged and
# still checked: this script only ever compares the path as a string — the ratchet tests $1
# against it, the staleness arm tests for its ABSENCE — and never hands it to an execution verb,
# which is the one condition this class is rejected for.
#
# export-closure: absent-by-design scripts/gen-dodo-product-art.mjs — a SUBJECT, not a
#   dependency: named to except it from the licence map and to tell a stamped export from a
#   development tree, never executed. The export REMOVES it on purpose (SCRIPTS_BLOCK).
#
# The ratchet below applies to it unchanged: move the file out of scripts/ — the end state
# this debt is owed — and the gate fails until someone deletes this entry.
INTERNAL_DEBT='scripts/gen-dodo-product-art.mjs'

# Expected identifier for a path. Echoes the id, or "" outside any licensed
# module (reported as an orphan).
expected_id() {
  if [ "$1" = "$INTERNAL_DEBT" ]; then
    echo "LicenseRef-Olivares-Internal"
    return
  fi
  case "$1" in
    core/*|modules/*|web/*)            echo "AGPL-3.0-only" ;;
    cmd/*)                             echo "AGPL-3.0-only" ;;
    docs-site/*|docs/*)                echo "AGPL-3.0-only" ;;
    terraform-provider-olivares/*)     echo "AGPL-3.0-only" ;;
    operator/*)                        echo "AGPL-3.0-only" ;;
    scripts/*)                         echo "AGPL-3.0-only" ;;
    # ⛔ `test/` ENTRA EN EL MAPA, y no es una excepcion: es el mismo AGPL que el resto del arbol
    # abierto. Anadida el 2026-08-30 al integrar el lote 2-f, cuando dos fixtures nuevos
    # (`test/fixtures/git-env/*.sh`, del carril git-env) salieron ORPHAN — «source file outside
    # core/modules/web/sdk/connectors»— pese a declarar ya `AGPL-3.0-only` en su cabecera.
    # Un ORPHAN no dice «licencia mala»: dice «no se para que arbol es este fichero», y la
    # respuesta aqui es la misma que para `scripts/`. Lo comercial sigue acotado por su propia
    # rama (`enterprise/`, `commercial/`), que esta arriba y gana.
    test/*)                            echo "AGPL-3.0-only" ;;
    sdk/*|connectors/*|clients/*)      echo "Apache-2.0" ;;
    examples/bring-your-own-protocol/*) echo "Apache-2.0" ;;
    # Added 2026-08-01 when the scan learned to read shell. Every one of these
    # directories ALREADY declared a licence unanimously in its files — the map
    # simply had no rule, so the gate called them orphans and could not check
    # them at all. Measured before writing: .github/actions 2/2 AGPL, deploy
    # 60/60, oscap 3/3, packaging 7/7, and the three examples other than
    # bring-your-own-protocol 1/1 each. These entries record what the tree says;
    # they do not relicense anything. Keep them BELOW the more specific
    # bring-your-own-protocol rule — first match wins in a case statement.
    examples/*)                        echo "AGPL-3.0-only" ;;
    deploy/*|oscap/*|packaging/*)      echo "AGPL-3.0-only" ;;
    .github/*)                         echo "AGPL-3.0-only" ;;
    # Added 2026-08-14 for the same reason and by the same method as the 2026-08-01 block
    # above: these two directories became VISIBLE for the first time when the enumeration
    # stopped being an extension allowlist, and without a rule the gate would have called
    # seven correctly-licensed files orphans. Measured before writing, file by file:
    # email/ 5/5 AGPL-3.0-only (it holds only .mjs and .json, which is exactly why no
    # earlier enumeration ever reached it) and .githooks/ 2/2 AGPL-3.0-only (commit-msg and
    # pre-push, extension-less shell programs found by their shebang). These entries record
    # what the tree already says; they do not relicense anything.
    email/*)                           echo "AGPL-3.0-only" ;;
    .githooks/*)                       echo "AGPL-3.0-only" ;;
    # Added 2026-08-17, by the same method as the two blocks above: the agent hooks under .claude/
    # became the first licensed files in that directory, and without a rule the gate called them
    # orphans — meaning it could not check them AT ALL, which is the failure this map exists to
    # prevent. They are INTERNAL: .claude/ sits in the exporter's top-level blocklist, so nothing
    # there ever ships, and the identifier RECORDS that rather than granting anything. Measured
    # before writing: 2/2 files carry LicenseRef-Olivares-Internal.
    .claude/*)                         echo "LicenseRef-Olivares-Internal" ;;
    commercial/*)                      echo "LicenseRef-Olivares-Commercial" ;;
    cloud/*)                           echo "LicenseRef-Olivares-Commercial" ;;
    *)                                 echo "" ;;
  esac
}

missing=0   # files with no identifier at all
mismatch=0  # files whose id != the module's id
orphan=0    # source files outside any licensed module
checked=0
unknown=0   # paths classify() has no rule for — the gate refuses to rule on these
unknown_exts=''  # space-separated, deduplicated, for the report

# Read newline-delimited paths via a temp file so the loop runs in the CURRENT
# shell (no subshell) and counters survive (a pipe would lose them under POSIX sh).
tmplist="$(mktemp)"
trap 'rm -f "$tmplist"' EXIT
list_files >"$tmplist"

while IFS= read -r f; do
  [ -n "$f" ] || continue
  [ -f "$f" ] || continue
  is_excluded_path "$f" && continue

  kind="$(classify "$f")"
  case "$kind" in
    data) continue ;;
    unknown)
      # Report the file AND remember the extension, so the message names the decision that
      # has to be made once instead of the hundred files that need it.
      printf 'UNCLASSIFIED %s  (classify() in this script has no rule for this extension)\n' "$f"
      unknown=$((unknown + 1))
      b=${f##*/}; e=".${b##*.}"
      case " ${unknown_exts} " in
        *" ${e} "*) : ;;
        *) unknown_exts="${unknown_exts}${e} " ;;
      esac
      continue ;;
  esac

  is_generated_content "$f" && continue

  want="$(expected_id "$f")"
  if [ -z "$want" ]; then
    printf 'ORPHAN   %s  (source file outside core/modules/web/sdk/connectors)\n' "$f"
    orphan=$((orphan + 1))
    continue
  fi

  checked=$((checked + 1))

  got="$(head -n 30 "$f" 2>/dev/null \
        | sed -n 's/.*License-Identifier:[[:space:]]*\([^[:space:]*]*\).*/\1/p' \
        | head -n 1)"

  if [ -z "$got" ]; then
    printf 'MISSING  %s  (no license identifier; expected %s)\n' "$f" "$want"
    missing=$((missing + 1))
  elif [ "$got" != "$want" ]; then
    printf 'MISMATCH %s  (found %s; expected %s)\n' "$f" "$got" "$want"
    mismatch=$((mismatch + 1))
  fi
done <"$tmplist"

# THE DEBT RATCHET. An exception that outlives its subject is a permanent hole: the file
# moves to the private overlay, this entry stays, and the next file created at exactly that
# path is blessed as commercial by a line nobody re-decided. So the exception must fail once
# there is nothing to except.
#
# It is skipped in a curated PUBLIC EXPORT, where the path is absent BY DESIGN — the export's
# COMMERCIAL_BLOCK removes it — and the marker the export curation script stamps
# (PUBLIC-EXPORT.md) is the discriminator, the same one scripts/hub-leg.sh uses. Two
# distinguishable states, never one: absent in an export is sanctioned, absent anywhere else
# means the debt was paid and its record was not.
#
# It counts, it no longer just latches: with two named debts, `stale=1` would have reported
# ONE for both, so the number in the verdict would stop matching the STALE lines above it.
stale=0
# D-2 rides the same ratchet for the same reason, and its export carve-out is the mirror of
# D-1's: once SCRIPTS_BLOCK curates the path out, a stamped export is a tree where it is
# absent BY DESIGN, and PUBLIC-EXPORT.md is again the discriminator. The end state this debt
# owes is the file leaving scripts/ — and on the day it does, this line goes red until the
# entry above is deleted, which is the whole point of a ratchet.
if [ ! -f "$INTERNAL_DEBT" ] && [ ! -f PUBLIC-EXPORT.md ]; then
  printf 'STALE    %s  (named licence debt D-2 whose subject no longer exists: delete INTERNAL_DEBT from this script)\n' "$INTERNAL_DEBT"
  stale=$((stale + 1))
fi

# ---------------------------------------------------------------------------------------
# THE TWO ANSWERS, IN THE ORDER THAT IS HONEST: "I could not look" OUTRANKS "what I saw".
#
# Until 2026-08-14 the licence verdict was decided FIRST and the floor below could only be
# reached on the way to a green. Run from an empty directory the gate answered
# `SPDX check FAILED: 0 missing, 0 mismatched, 0 orphan, 1 stale exception (of 0 licensed
# files checked)` and exited 1 — non-zero, so no tree was wrongly approved, but the sentence
# blames a licence debt for what is really an enumeration that read NOTHING, and the exit
# code says "licence violation" when the truth is "refusal to rule". Both reasons now print,
# and a tree the gate could not enumerate exits 2 whatever its headers say.
#
# Neither reordering can turn a red into a green: every path below this line is non-zero
# except the last one, and the last one requires a full population AND zero offenders.
# ---------------------------------------------------------------------------------------

# THE POPULATION FLOOR — dated and measured, in the shape scripts/check-migrations.sh uses.
#
# The old floor caught only checked == 0. A PARTIAL collapse of the enumeration still
# printed OK: lose `*.go` from the walk and the gate would answer "SPDX check OK: 1,242
# source files …" in green, having stopped reading three quarters of the product. Zero is
# not the only number that means "I did not look".
#
# MEASURED 2026-08-14 at bbdffe22e, both trees this script is expected to run in — the second
# one by actually building it (the export curation script into a scratch dir) and running this
# gate from inside it, which also exercises the non-git `find` branch of list_files:
#     this repository .................... 5,004 licensed files checked
#     the curated public export tree ..... 4,714 (smaller BY DESIGN; the export curation script
#                                          selects 7,554 of 10,930 tracked paths)
# The floor sits below BOTH so one constant serves both trees and no carve-out is needed —
# a carve-out keyed on PUBLIC-EXPORT.md would have been a second place for this to rot.
#
# It is a COLLAPSE detector, not a target, and it says so out loud: it will not notice a
# small loss (drop `.sh` alone and 179 files vanish inside the margin). The other direction
# — a language arriving that nobody classified — is the `unknown` bucket's job, not this
# one's. If a legitimate deletion ever takes the population under this number, the fix is to
# re-measure and move the constant IN THE SAME COMMIT, with the new date.
SPDX_MIN_CHECKED=4200

cannot_look=0

if [ "$unknown" -ne 0 ]; then
  echo
  # NO "tracked": la lista de arriba es `ls-files --cached --others --exclude-standard`,
  # que incluye los NO RASTREADOS. Decir «tracked» manda a quien lo lee a mirar el indice,
  # HEAD y main —donde el fichero no esta— y a concluir que el gate se equivoca. Me paso a
  # mi el 2026-08-24 con un `.keep` de 0 bytes que era residuo de mi propio worktree.
  echo "SPDX check CANNOT LOOK: ${unknown} file(s) in the working tree (tracked OR untracked-and-not-ignored) have an extension classify() has no rule for." >&2
  echo "  Extensions: ${unknown_exts}" >&2
  echo "  This is not a licence violation and it is not a pass: the gate does not know whether" >&2
  echo "  those files are source. Open classify() in this script and put each extension in ONE" >&2
  echo "  of the two buckets, with the reason written next to it:" >&2
  echo "    source -> it gets a module-correct inline SPDX header like every other source file" >&2
  echo "    data   -> it does not, and REUSE.toml is where it is licensed instead" >&2
  cannot_look=1
fi

if [ "${checked}" -lt "${SPDX_MIN_CHECKED}" ]; then
  echo
  if [ "${checked}" -eq 0 ]; then
    echo "SPDX check CANNOT LOOK: zero licensed source files were examined." >&2
    echo "  Nothing was read, so nothing is approved. Run this from the repository root," >&2
    echo "  or point it at a tree that has tracked sources." >&2
  else
    echo "SPDX check CANNOT LOOK: only ${checked} licensed source files were examined, below the" >&2
    echo "  floor of ${SPDX_MIN_CHECKED}. On 2026-08-14 this same walk found 5,004 here and 4,714 in the" >&2
    echo "  public export, so a population this small means the enumeration stopped working —" >&2
    echo "  a wrong root, a widened exclusion, a broken list_files — not that the sources went" >&2
    echo "  away. A partial read graded as a complete one is exactly what this refuses to do." >&2
    echo "  If the tree really did shrink, re-measure and move SPDX_MIN_CHECKED in the same commit." >&2
  fi
  cannot_look=1
fi

if [ "$cannot_look" -ne 0 ]; then
  exit 2
fi

bad=$((missing + mismatch + orphan + stale))
if [ "$bad" -ne 0 ]; then
  echo
  echo "SPDX check FAILED: ${missing} missing, ${mismatch} mismatched, ${orphan} orphan, ${stale} stale exception (of ${checked} licensed files checked)."
  echo "Fix: add the correct header near the top of the file. Examples:"
  echo "  Go/TS:  the AGPL-3.0-only or Apache-2.0 identifier per the module frontier (see CONTRIBUTING.md)"
  exit 1
fi

echo "SPDX check OK: ${checked} source files carry a valid, module-correct header."
# REUSE-IgnoreEnd
