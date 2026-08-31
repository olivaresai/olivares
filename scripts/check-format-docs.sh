#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-format-docs.sh — occurrence-aware guard for the OTLP export-token remap
# in LIVE documentation.
#
# Since the remap, on every surface (source of truth: sdk/siemwire/catalog.go):
#
#   otlp            = the complete, postable OTLP/HTTP JSON ExportLogsServiceRequest
#   otlp_envelope   = an exact byte-for-byte alias of otlp
#   otlp_log_record = the bare one-LogRecord-per-line projection (ledger pull export only)
#
# This script FAILS when live documentation still carries the pre-remap
# vocabulary. It is occurrence-aware, not blanket: every pattern below names a
# specific stale claim, and the exemptions state why a matching line is not one.
# The pattern set was hardened by the 2026-07-27 sweep itself, which
# adjudicated all 683 live occurrences and reported the spellings the first
# revision missed (backtick-separated lists, natural-language enumerations,
# truncated pre-LEEF lists, localized glosses). Plain grep on purpose — it must
# run in any build environment, ripgrep or not.
#
# Deliberate non-patterns, so the guard does not cry wolf:
#   - Uppercase family naming ("CEF/LEEF/syslog/OTLP/OCSF") is prose, not a
#     token enumeration; token patterns are lowercase and case-sensitive.
#   - Loose "open formats" enumerations (docs/trust/vendor-viability.md) assert
#     nothing about the remap.
# Known limit: a stale gloss wrapped across two physical lines (seen once in a
# CJK file) evades single-line grep; the sweep fixed the one instance, and the
# per-language phrase fragments below are chosen to sit inside one line.
#
# Frozen zones are exempt on purpose — they describe the state at their date:
#   docs-site/src/content/docs/2026-06/**   (published snapshot)
#   **, docs/superpowers/**   (dated research/design artifacts)
#   sessions/**, design/**                  (dated engineering logs and audit reports)
#   ESTADO-PROYECTO.md                      (dated status journal, not product docs)
#   (dated gap register: entries quote
#                                            historical code states as evidence;
#                                            its one live caveat was fixed by
#                                            the sweep before this exemption)
#
# scripts/format-docs-allow.txt carries the adjudicated exceptions: past-tense
# historical narrative (CHANGELOG bug entries) that truthfully quotes pre-remap
# spellings and that no pattern can tell from a stale present-tense claim.
set -euo pipefail
cd "$(dirname "$0")/.."

# ⛔ AISLAMIENTO DE ENTORNO GIT — lo exige `lint:git-env` a cualquier guion que empareje `mktemp -d`
# con git, y aquí no es burocracia: el self-test de abajo EJECUTA este mismo guion dentro de un
# árbol temporal, y un `GIT_DIR` heredado haría que cualquier git de ese árbol operase sobre OTRO
# repositorio. Es el mecanismo del repo, ya auditado sobre 30 miembros.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

# ⛔ SELF-TEST: la guarda anti-vacuidad se prueba en las DOS direcciones, o no prueba nada.
# La cabecera de este fichero ya avisaba —«the only figure that would have betrayed an empty sweep
# is the occurrence total near the end of this file, and that is computed by this same function
# and MERELY PRINTED»—. Estaba impreso y no aplicado: medido el 2026-08-15 con el guion copiado a
# un árbol vacío, imprimía «occurrences: 0» y contestaba **OK, rc=0**.
#
# El señuelo va bajo $HOME porque /tmp está montado noexec en el contenedor (execve → 126).
if [ "${1:-}" = "--selftest" ]; then
	_sut="$(cd "$(dirname "$0")" && pwd)/$(basename "$0")"
	_ok=0; _ko=0
	_caso() { # <rc esperado> <descripción> <contenido del doc, vacío = ninguno>
		local want="$1" label="$2" body="${3:-}" d rc
		d="$(mktemp -d "${TMPDIR_SELFTEST:-${HOME:-${TMPDIR:-/tmp}}}/.fdst.XXXXXX")"
		mkdir -p "$d/scripts" "$d/docs"
		cp "$_sut" "$d/scripts/" && chmod +x "$d/scripts/$(basename "$0")"
		cp "$(dirname "$_sut")/format-docs-allow.txt" "$d/scripts/" 2>/dev/null || true
		# ⛔ La librería de aislamiento VIAJA con el señuelo: este guion la sourcea al arrancar, así
		#    que sin ella la copia interna sale 2 con «cannot source» y la casilla mediría eso en
		#    vez de lo que dice medir. Lo cazó el propio self-test al ponerse 1/1.
		mkdir -p "$d/scripts/lib"
		cp "$(dirname "$_sut")/lib/git-env.sh" "$d/scripts/lib/" 2>/dev/null || true
		if [ -n "$body" ]; then printf '%s\n' "$body" > "$d/docs/x.md"; fi
		# ⛔ `|| rc=$?` y no `; rc=$?`: bajo `set -e` un subshell que sale ≠0 mata el guion
		#    ANTES de que la asignación corra. Me pasó dos veces en esta misma función —la
		#    otra fue un `[ … ] && printf` con el cuerpo vacío—, y las dos veces el síntoma
		#    fue el mismo: el self-test imprimía su cabecera y NINGUNA casilla.
		rc=0
		( cd "$d" && "./scripts/$(basename "$0")" >/dev/null 2>&1 ) || rc=$?
		rm -rf "$d"
		if [ "$rc" -eq "$want" ]; then _ok=$((_ok+1)); printf '  ok    %-52s rc=%s\n' "$label" "$rc"
		else _ko=$((_ko+1)); printf '  FALLO %-52s esperaba=%s obtuvo=%s\n' "$label" "$want" "$rc"; fi
	}
	echo "check-format-docs self-test"
	_caso 2 'un barrido SIN sujeto es NO HE PODIDO MIRAR' ''
	# ⛔ CONTROL NEGATIVO: sin él, la casilla de arriba se cumpliría rechazando SIEMPRE.
	_caso 0 'con sujeto y sin vocabulario obsoleto, pasa' 'El campo `otlp` lleva el sobre completo.'
	printf 'check-format-docs self-test: %d pasan, %d fallan\n' "$_ok" "$_ko"
	[ "$_ko" -eq 0 ]
	exit $?
fi

# grep exits 0 with matches, 1 with none and >=2 on ERROR. The trailing `|| true` used to
# make all three the same answer — an empty hit set — so every P1-P4 family reported OK
# and the gate passed. It does not take an exotic grep: measured 2026-08-01 on this very
# tree, GNU grep returns 2 for ANY unreadable file or directory encountered during the
# recursive walk, EVEN WHEN IT ALSO FOUND MATCHES. The only figure that would have
# betrayed an empty sweep is the occurrence total near the end of this file, and that is
# computed by this same function and merely printed.
live_docs() { # any number of grep -e patterns
  local out rc=0
  out="$(grep -rInE "$@" \
    --include='*.md' --include='*.mdx' \
    --exclude-dir=2026-06 --exclude-dir=research --exclude-dir=superpowers \
    --exclude-dir=sessions --exclude-dir=design --exclude-dir=node_modules \
    --exclude-dir=.git --exclude-dir=.export-tmp \
    --exclude=ESTADO-PROYECTO.md \
    --exclude=12-GAPS-Y-ROADMAP-ENTERPRISE.md \
    .)" || rc=$?
  if [ "$rc" -ge 2 ]; then
    echo "check-format-docs: the documentation sweep failed (grep exit ${rc});" >&2
    echo "  the live-doc vocabulary is UNVERIFIED. Refusing to report a clean tree." >&2
    exit 2
  fi
  # ⛔ CON SALTO FINAL, Y ESO CAMBIA EL NÚMERO QUE ESTE GATE PUBLICA. `printf '%s'` no lo deja, y
  # el total se calcula con `wc -l`, que cuenta SALTOS: el censo contaba siempre UNA DE MENOS.
  # Medido el 2026-08-15 con árboles fabricados de 1, 2 y 3 ocurrencias → informaba 0, 1 y 2. Con
  # UNA sola ocurrencia el árbol se leía como VACÍO, que es la peor de las tres: sin guarda
  # anti-vacuidad daba «limpio», y con ella daría «no he podido mirar» sobre un árbol que sí tiene
  # sujeto. La cifra real de este repositorio es 840, no 839.
  #
  # El caso vacío se distingue a mano: `printf '%s\n' ""` emitiría UNA línea en blanco y `wc -l`
  # contaría 1 sobre un barrido sin resultados, que es el error simétrico.
  if [ -n "$out" ]; then printf '%s\n' "$out"; fi
}

# allowed drops the adjudicated historical-narrative lines listed in
# scripts/format-docs-allow.txt (comments and blanks stripped).
allowed() { grep -vFf <(grep -vE '^#|^$' scripts/format-docs-allow.txt) || true; }

# other_surface drops the OTHER surfaces' legitimate adjacencies — narrowly, so
# a stale ledger list cannot hide behind an unrelated mention of another token
# on the same line (a review mutation proved the first, token-presence version
# of this exemption had exactly that hole). Exempt are: the eventing list's
# otlp_envelope→json tail (within a short window, covering separators and the
# "or"/CJK-comma spellings), the notification list's ocsf→asim tail, and the
# ledger list's own otlp_envelope→otlp_log_record adjacency.
#
# That third leg is WINDOWED for the same reason as the other two. It used to be
# a bare token-presence test, which a contrast round proved blind: a stale list
# that merely NAMES the projection token elsewhere on the line ("accepts
# `cef|…|otlp_envelope|ocsf`; `otlp_log_record` is not offered") escaped the whole
# family. A correct post-remap ledger list always carries the token INSIDE the
# enumeration, right after otlp_envelope — that adjacency is the exemption.
other_surface() {
  grep -vE 'otlp_envelope.{0,12}json|ocsf.{0,8}asim|otlp_envelope.{0,8}otlp_log_record' || true
}

# projection_owned drops a GLOSS line only when the bare-projection claim on it
# actually BELONGS to otlp_log_record — the one sentence the post-remap contract
# requires docs to write. Ownership is same-CLAUSE adjacency, in either order:
#
#   token then claim — "`otlp_log_record` is still the bare LogRecord projection"
#   claim then token — "…the bare LogRecord projection its own `otlp_log_record` token"
#
# with no sentence boundary between them. That boundary is the whole discriminator:
# the stale shape this replaces wears the token as camouflage in a SEPARATE clause
# ("The `otlp` format is a bare LogRecord projection; `otlp_log_record` is
# unrelated"), which the previous bare token-presence exemption let through and a
# contrast round proved blind. Commas do not separate — a trailing appositive
# ("…under its own token, `otlp_log_record`") still owns the claim.
#
# Implemented with grep, not awk, deliberately: mawk's regex engine does not match
# the multibyte CJK gloss patterns that grep -E matches (measured — match() returns
# 0 on the very line grep selected), so a position test in awk would silently exempt
# nothing in exactly the languages the sweep worked hardest on. One engine, one
# answer. $1 = the ERE its family matched on.
projection_owned() { # $1 = family ERE
  grep -vE "otlp_log_record[^.;]{0,160}(${1})|(${1})[^.;]{0,160}otlp_log_record" || true
}

# join_re renders a family's `-e pat -e pat` argument list as one ERE, so the
# position test above matches on exactly what the family matched on — no second,
# drifting copy of the patterns.
join_re() {
  local out="" p
  for p in "$@"; do
    [ "$p" = "-e" ] && continue
    if [ -z "$out" ]; then out="$p"; else out="$out|$p"; fi
  done
  printf '%s' "$out"
}

fail=0
report() { # $1 = pattern id, $2 = hits
  if [ -n "$2" ]; then
    echo "STALE[$1]:"
    echo "$2"
    fail=1
  fi
}

# P1 — pre-remap ledger token lists. Separators cover pipes (plain and escaped),
# slashes, commas, spaces, backticks and the CJK comma; natural-language
# enumerations bridge the last two tokens with a conjunction. The post-remap
# ledger list interposes otlp_log_record (exempt via other_surface), and the
# other surfaces' legitimate adjacencies are exempted narrowly there too.
# The list-ENDS-at-otlp patterns are anchored to end-of-line without a trailing
# comma: a correct seven-token list wrapped mid-enumeration ends its first
# physical line "`otlp`," (continuation), while a stale list truncated at otlp
# ends the claim there.
P1=(
  -e 'otlp_envelope[`\\ |/,、]{1,6}ocsf'
  -e 'otlp_envelope`?[^a-z_]{0,3}(and|und|y|et|и|和)[^a-z_]{0,3}`?ocsf'
  -e 'otlp[`\\ |/,、]{1,6}ocsf'
  -e 'cef[`\\|/, ]{1,4}syslog[`\\|/, ]{1,4}otlp'
  -e 'syslog[`\\|/,、 ]{1,4}otlp`?[.)]*$'
  -e ' (or|oder|ou|или|或) `?otlp`?[.)]*$'
)
p1=$(live_docs "${P1[@]}" | other_surface | allowed)
report P1-stale-ledger-token-list "$p1"

# P2 — the pre-remap "bare projection or postable request envelope" gloss, both
# halves, all seven languages, backticked or not. Post-remap prose may still
# describe the bare projection — but only while naming its token on the line.
P2=(
  -e 'bare .{0,2}LogRecord.{0,2} projection'
  -e 'bare (LogRecord )?projection'
  -e 'reine (LogRecord-)?Projektion'
  -e 'projection simple (de LogRecord|ou )'
  -e 'proyecci.{1,2}n simple (de LogRecord|o )'
  -e 'прост(ая|ой) проекци[^ ]*( LogRecord| или)'
  -e '素の ?(LogRecord ?射影|プロジェクション)'
  -e '裸.{0,6}投影'
  -e 'o(r|der) post(able|bare)[^.]{0,30}[Ee]nvelope'
  -e 'envoltorio de request posteable'
  -e 'enveloppe de requête postable'
  -e 'POST 可能なリクエスト・エンベロープ'
  -e 'отправляемый конверт запроса'
  -e '可 POST 的请求信封'
)
p2=$(live_docs "${P2[@]}" | projection_owned "$(join_re "${P2[@]}")" | allowed)
report P2-bare-projection-gloss "$p2"

# P3 — claims that the otlp token is not postable, that only otlp_envelope is,
# or that the alias is a different thing than its canonical form; plus the
# present-tense persistence variants and the proto-type spelling of the
# bare-shape claim.
P3=(
  -e 'otlp.{0,80}is a LogRecord projection'
  -e 'not an .?ExportLogsServiceRequest.? body'
  -e 'otlp_envelope.{0,40}is the postable one'
  -e '(stays|remains|is still) a bare'
  -e 'enviable es .otlp_envelope'
  -e 'publiable est .otlp_envelope'
  -e 'Sendefähig ist .otlp_envelope'
  -e 'Отправляемый вариант.{0,4}.otlp_envelope'
  -e 'POST 可能なのは .otlp_envelope'
  -e '投递的是 .otlp_envelope'
  -e 'otlp_envelope.\).{0,4}\|.{0,4}(OTLP/HTTP|Requête|Petición|Запрос экспорта|エクスポートリクエスト|导出请求)'
  -e 'logspb\.LogsData. real'
)
ALIAS_DENIAL='two different things|zwei verschiedene Dinge|dos cosas distintas|deux choses différentes|разные вещи|別物|两样东西'
p3a=$(live_docs "${P3[@]}" | projection_owned "$(join_re "${P3[@]}")" | allowed)
p3b=$(live_docs -e 'otlp_envelope' | { grep -E "$ALIAS_DENIAL" || true; } \
  | projection_owned "$ALIAS_DENIAL" | allowed)
# Join with a newline and drop the blank one, so two non-empty halves cannot glue
# the last hit of the first onto the first hit of the second: $(...) strips the
# trailing newline, and the operator reads the evidence this guard exists to print.
p3=$(printf '%s\n%s\n' "$p3a" "$p3b" | { grep -v '^$' || true; })
report P3-otlp-not-postable-claim "$p3"

# P4 — stale alias-scope and enum-drift claims: post-remap the alias holds on
# EVERY surface, and the OpenAPI enum derives from the engine registry.
P4=(
  -e 'DIFFER only on the ledger export'
  -e 'alias.{0,20}de .otlp. aqu'
  -e 'pero el motor también sirve'
)
p4=$(live_docs "${P4[@]}" | projection_owned "$(join_re "${P4[@]}")" | allowed)
report P4-alias-scope-claim "$p4"

# ---- SELF-TEST: the guard must still CATCH the known stale families and must
# still PASS the canonical correct spellings. A pattern or exemption edit that
# breaks either direction fails the run on its own — a review mutation proved
# the first exemption was broad enough to hide a real stale list, so blindness
# is treated as a failure, not a silent pass.
scan1() { printf '%s\n' "$1" | { grep -E "${P1[@]}" || true; } | other_surface; }
scan2() { printf '%s\n' "$1" | { grep -E "${P2[@]}" || true; } | projection_owned "$(join_re "${P2[@]}")"; }
scan3() { printf '%s\n' "$1" | { grep -E "${P3[@]}" || true; } | projection_owned "$(join_re "${P3[@]}")"; }
scan3b() { printf '%s\n' "$1" | { grep -E 'otlp_envelope' || true; } | { grep -E "$ALIAS_DENIAL" || true; } | projection_owned "$ALIAS_DENIAL"; }
scan4() { printf '%s\n' "$1" | { grep -E "${P4[@]}" || true; } | projection_owned "$(join_re "${P4[@]}")"; }
must_flag() { # $1 = family, $2 = probe line, $3 = scan result
  if [ -z "$3" ]; then
    echo "SELF-TEST BLIND[$1]: $2"
    fail=1
  fi
}
must_pass() { # $1 = family, $2 = probe line, $3 = scan result
  if [ -n "$3" ]; then
    echo "SELF-TEST FALSE-POSITIVE[$1]: $2"
    fail=1
  fi
}

probe='SIEM/ITSM push (CEF/LEEF/syslog/OTLP (bare LogRecord projection or postable request envelope)/OCSF)'
must_flag P2-en "$probe" "$(scan2 "$probe")"
probe='SIEM/ITSM-Push (CEF/LEEF/syslog/OTLP (reine LogRecord-Projektion oder postbare Request-Envelope)/OCSF)'
must_flag P2-de "$probe" "$(scan2 "$probe")"
probe='or an {"export_complete":true,...} JSON line for `otlp`/`ocsf`.'
must_flag P1-terminator "$probe" "$(scan1 "$probe")"
probe='Ledger export accepts cef|leef|syslog|otlp|otlp_envelope|ocsf; eventing also supports json.'
must_flag P1-review-mutation "$probe" "$(scan1 "$probe")"
probe='**`otlp` and `otlp_envelope` are two different things, on purpose.**'
must_flag P3-alias-denial "$probe" "$(scan3b "$probe")"
probe='The `otlp` format is a LogRecord projection — one JSON object per line.'
must_flag P3-not-postable "$probe" "$(scan3 "$probe")"
probe='the two DIFFER only on the ledger export'
must_flag P4-alias-scope "$probe" "$(scan4 "$probe")"
# The camouflage shapes: a stale claim that NAMES the projection token elsewhere on
# the line. The bare token-presence exemption swallowed both; the windowed P1 leg and
# projection_owned exist to catch them, and these two probes are what prove it.
probe='Ledger export accepts `cef|leef|syslog|otlp|otlp_envelope|ocsf`; `otlp_log_record` is not offered.'
must_flag P1-projection-camouflage "$probe" "$(scan1 "$probe")"
probe='The `otlp` ledger format is a bare LogRecord projection; `otlp_log_record` is unrelated.'
must_flag P2-projection-camouflage "$probe" "$(scan2 "$probe")"

probe='Ledger export accepts `cef|leef|syslog|otlp|otlp_envelope|otlp_log_record|ocsf` (default `cef`).'
must_pass ledger-seven "$probe" "$(scan1 "$probe")$(scan2 "$probe")"
probe='Eventing sink accepts `ocsf|cef|leef|syslog|otlp|otlp_envelope|json` (default `ocsf`).'
must_pass eventing-list "$probe" "$(scan1 "$probe")"
probe='Notification connectors accept `json|cef|leef|syslog|otlp|otlp_envelope|ocsf|asim`.'
must_pass notification-list "$probe" "$(scan1 "$probe")"
probe='Supported `format` values are `cef`, `leef`, `syslog`, `otlp`,'
must_pass wrapped-continuation "$probe" "$(scan1 "$probe")"
probe='still exists as the bare `LogRecord` projection under its own token, `otlp_log_record`.'
must_pass gloss-names-token "$probe" "$(scan2 "$probe")"
# Shape (a) of projection_owned: the token OWNS the claim by preceding it. P3 carried
# no token exemption at all before, so this correct sentence used to fail the gate.
probe='`otlp_log_record` is still the bare `LogRecord` projection, and it is not postable.'
must_pass projection-owns-claim "$probe" "$(scan2 "$probe")$(scan3 "$probe")"
probe='`otlp` and `otlp_envelope` carry the envelope; `otlp_log_record` is the bare projection.'
must_pass token-first-gloss "$probe" "$(scan2 "$probe")"

total=$(live_docs -ie '\botlp' | wc -l)
echo "info: live-doc OTLP-token occurrences (all verdicts): ${total}"

# ⛔ UN CENSO VACÍO NO ES DOCUMENTACIÓN LIMPIA: ES UN CENSO QUE DEJÓ DE ENCONTRAR SUJETO.
# Medido el 2026-08-15 con el guion copiado a un árbol vacío: imprimía «occurrences: 0» y
# contestaba **OK con rc=0**. Este fichero ya se protege de que `grep` FALLE (arriba), pero no de
# que no encuentre NADA, que es la forma silenciosa del mismo fallo — y la peor, porque el verde
# viaja igual a la máquina.
#
# El umbral es 1 a propósito, no un número redondo: lo que se afirma es que el barrido tiene
# sujeto, no cuánto sujeto. Este repositorio tiene 839 ocurrencias hoy; cualquier cifra concreta
# envejecería y volvería a fallar por una razón que no es la que mide.
if [ "${total:-0}" -eq 0 ]; then
  echo "check-format-docs: NO HE PODIDO MIRAR: el barrido no encontró NI UNA ocurrencia de" >&2
  echo "  \`otlp\` en documentación viva. Eso no es un árbol limpio: es un censo sin sujeto —" >&2
  echo "  el árbol equivocado, un --include que dejó de casar, o la documentación movida." >&2
  exit 2
fi

if [ "$fail" -ne 0 ]; then
  echo "NOT CLEAN: live documentation still carries pre-remap OTLP vocabulary."
  exit 1
fi
echo "OK: no pre-remap OTLP vocabulary in live documentation."
