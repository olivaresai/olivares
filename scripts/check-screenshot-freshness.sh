#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Compare committed screenshot age with the sources it depicts, allowing configured lag.
# Git commit timestamps do not establish when an image was captured.
# Exit 0 within tolerance, 1 for enforced stale captures, or 2 when inputs cannot be
# measured.
set -uo pipefail
LC_ALL=C; export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || { echo "check-screenshot-freshness: ⛔ COULD NOT CHECK: missing $RAIZ" >&2; exit 2; }
git rev-parse --git-dir >/dev/null 2>&1 || { echo "check-screenshot-freshness: ⛔ COULD NOT CHECK: outside a repository" >&2; exit 2; }

# Record HEAD before reading timestamps, capture paths, pins or source indices.
# Reject changed HEAD after reading capture inputs rather than mixing revisions.
SUJETO="$(git rev-parse HEAD 2>/dev/null || printf 'sin-head')"

# Compare the ending HEAD with the revision recorded before the first input read.
verifica_sujeto() {
    local ahora
    ahora="$(git rev-parse HEAD 2>/dev/null || printf 'sin-head')"
    if [ "$ahora" != "$SUJETO" ]; then
        echo "check-screenshot-freshness: ⛔ COULD NOT CHECK: the tree changed during measurement" >&2
        echo "        (started at ${SUJETO:0:9}, ended at ${ahora:0:9}). The inputs describe two revisions," >&2
        echo "        but the verdict would name only one. Rerun against a stable tree." >&2
        exit 2
    fi
}

# Fallback clock: latest layout/features commit.
UI_TS="$(git log -1 --format=%ct -- 'web/src/components/layout' 'web/src/features' 2>/dev/null)"
if [ -z "${UI_TS:-}" ]; then
    echo "check-screenshot-freshness: ⛔ COULD NOT CHECK: no UI history in this clone." >&2
    exit 2
fi

# Select tracked captures, including current documentation and hero images.
# Known limit: brand compositions may embed screenshots but are outside this selector.
CAPS="$(git ls-files -- '*.png' '*.webp' 2>/dev/null | grep -iE 'screenshot|captura|docs-site/src/assets|docs-site/public/console/|\.github/assets/console-|design/launch-video/assets/console/|public/img' || true)"
N="$(printf '%s\n' "$CAPS" | grep -c . || true)"

# An empty capture set is unmeasured, never a successful freshness result.
if [ "${N:-0}" -eq 0 ]; then
    echo "check-screenshot-freshness: ⛔ COULD NOT CHECK: no screenshots found with the current pattern." >&2
    echo "                            An empty scan cannot pass; the pattern may be stale or files may have moved." >&2
    exit 2
fi

# Optional pin rows contain prefix, class, anchor and reason, separated by tabs.
# Unknown classes or missing fields exit 2; free-form reasons cannot waive freshness.
# snapshot-de-version requires a version-marked path and a same-version consumer
# that references a matching image.
# evidencia-fechada requires an ISO date anchor represented in the path.
# These classes preserve intentionally frozen material; pending recaptures are not pins.
PINS="$RAIZ/design/screenshot-pins.txt"
declare -a PIN_RUTA=() PIN_RAZON=() PIN_CLASE=() PIN_ANCLA=() PIN_USADO=()

# Return the first version/date mark in the path, or nothing when absent.
marca_de_version() {
    printf '%s\n' "$1" | grep -oE '20[0-9][0-9]-[0-9][0-9](-[0-9][0-9])?' | head -1
}

if [ -f "$PINS" ]; then
    linea=0
    while IFS=$'\t' read -r ruta clase ancla razon; do
        linea=$((linea+1))
        case "${ruta:-}" in ''|'#'*) continue ;; esac

        if [ -z "${clase:-}" ] || [ -z "${ancla:-}" ] || [ -z "${razon// /}" ]; then
            echo "check-screenshot-freshness: ⛔ COULD NOT CHECK: '$ruta' (line $linea in design/screenshot-pins.txt)" >&2
            echo "                            is missing fields <prefix><TAB><class><TAB><anchor><TAB><reason>." >&2
            echo "                            Classes: snapshot-de-version | evidencia-fechada. See the register header." >&2
            exit 2
        fi

        case "$clase" in
        snapshot-de-version)
            va="$(marca_de_version "$ruta")"
            vc="$(marca_de_version "$ancla")"
            if [ -z "$va" ]; then
                echo "check-screenshot-freshness: ⛔ COULD NOT CHECK: '$ruta' has no version marker (20YY-MM)," >&2
                echo "                            so it cannot be a version snapshot. A pending recapture" >&2
                echo "                            is unfinished work and must fail, not be pinned in the register." >&2
                exit 2
            fi
            if [ "$va" != "$vc" ]; then
                echo "check-screenshot-freshness: ⛔ COULD NOT CHECK: '$ruta' claims to be a snapshot of '$va', but its" >&2
                echo "                            declared consumer '$ancla' belongs to '${vc:-no version}'." >&2
                exit 2
            fi
            if ! git ls-files --error-unmatch -- "$ancla" >/dev/null 2>&1 && [ ! -e "$ancla" ]; then
                echo "check-screenshot-freshness: ⛔ COULD NOT CHECK: declared consumer '$ancla' is missing." >&2
                exit 2
            fi
            # Match version/basename so relative consumer links remain valid.
            # Check at least one referenced image among the first 40 matching paths;
            # this is
            # not proof that every image under the prefix is consumed.
            ficheros="$(git ls-files -- "$ruta" "$ruta*" 2>/dev/null | head -40)"
            if [ -n "$ficheros" ]; then
                usado=0
                while IFS= read -r img; do
                    [ -z "$img" ] && continue
                    if grep -rqF -- "$va/${img##*/}" "$ancla" 2>/dev/null; then usado=1; break; fi
                done <<EOF
$ficheros
EOF
                if [ "$usado" -eq 0 ]; then
                    echo "check-screenshot-freshness: ⛔ COULD NOT CHECK: '$ancla' uses no images from '$ruta'." >&2
                    echo "                            A snapshot requires a frozen consumer that uses it." >&2
                    exit 2
                fi
            fi
            ;;
        evidencia-fechada)
            case "$ancla" in
            20[0-9][0-9]-[0-9][0-9]-[0-9][0-9]) : ;;
            *)
                echo "check-screenshot-freshness: ⛔ COULD NOT CHECK: the anchor for '$ruta' must be an ISO date" >&2
                echo "                            YYYY-MM-DD; got '$ancla'." >&2
                exit 2 ;;
            esac
            aa="${ancla%%-*}"; resto="${ancla#*-}"; mm="${resto%%-*}"; dd="${ancla##*-}"
            visto=0
            for forma in "$ancla" "$aa$mm$dd" "$dd$mm$aa" "$aa-$mm" "$aa$mm"; do
                case "$ruta" in *"$forma"*) visto=1; break ;; esac
            done
            if [ "$visto" -eq 0 ]; then
                echo "check-screenshot-freshness: ⛔ COULD NOT CHECK: '$ruta' declares evidence from $ancla, but its" >&2
                echo "                            path does not contain that date in a recognized format." >&2
                echo "                            Evidence whose value depends on its date needs a dated path." >&2
                exit 2
            fi
            ;;
        *)
            echo "check-screenshot-freshness: ⛔ COULD NOT CHECK: unknown class '$clase' for '$ruta'." >&2
            echo "                            Allowed classes: snapshot-de-version | evidencia-fechada." >&2
            echo "                            An unidentified image cannot be pinned." >&2
            exit 2 ;;
        esac

        PIN_RUTA+=("$ruta"); PIN_CLASE+=("$clase"); PIN_ANCLA+=("$ancla")
        PIN_RAZON+=("$razon"); PIN_USADO+=(0)
    done < "$PINS"
fi

# Measure lag in whole days, with tolerance supplied by the caller.
TOLERANCIA="${OLIVARES_SCREENSHOT_MAX_LAG_DAYS:-0}"
# Write every stale capture to the list even when console output is bounded.
LISTA_COMPLETA="${OLIVARES_SCREENSHOT_STALE_LIST:-${TMPDIR:-/tmp}/screenshot-stale.tsv}"
: > "$LISTA_COMPLETA"
case "$TOLERANCIA" in ''|*[!0-9]*)
    echo "check-screenshot-freshness: ⛔ COULD NOT CHECK: nonnumeric tolerance '$TOLERANCIA'." >&2
    exit 2 ;;
esac

# Resolve the named component and two levels of textual imports for its clock.
# Shared UI files are included when resolved through those imports.
# Unresolved components fall back to the layout/features clock; this is not a
# complete transitive dependency analysis.
INDICE_FUENTES="$(mktemp)"
trap 'rm -f "$INDICE_FUENTES"' EXIT
find web/src -type f \( -name '*.tsx' -o -name '*.ts' \) 2>/dev/null > "$INDICE_FUENTES" || true

# Call memoized resolvers in the parent shell and receive their results through globals.
# Command substitutions or a piped main loop would discard cache writes.
# Cache both successful and empty resolutions.

declare -A _TS_FICHERO=()
declare -A _FUENTES=()
declare -A _IMPORTS=()

# imports_de leaves existing relative and @/ imports in IMPORTS_OUT.
# Only single-quoted from declarations are recognized; results are cached per file.
imports_de() {
    local f="$1" d spec p ext acc
    IMPORTS_OUT=""
    [ -f "$f" ] || return 0
    if [ -n "${_IMPORTS[$f]+x}" ]; then IMPORTS_OUT="${_IMPORTS[$f]}"; return 0; fi
    d="$(dirname "$f")"
    acc="$(grep -oE "from '[^']+'" "$f" 2>/dev/null | sed "s/^from '//; s/'$//" | while IFS= read -r spec; do
        case "$spec" in
            @/*)       p="web/src/${spec#@/}" ;;
            ./*|../*)  p="$(cd "$d" 2>/dev/null && printf '%s' "$(realpath -m --relative-to="$RAIZ" "$spec" 2>/dev/null)")" ;;
            *)         continue ;;
        esac
        [ -n "$p" ] || continue
        for ext in '.tsx' '.ts' '/index.tsx' '/index.ts' ''; do
            if [ -f "$p$ext" ]; then printf '%s\n' "$p$ext"; break; fi
        done
    done)"
    _IMPORTS[$f]="$acc"
    IMPORTS_OUT="$acc"
    return 0
}

# file_timestamp returns the path-specific latest commit time in TS_OUT, or 0.
# Keep git log -1 -- <path> semantics: a bulk --name-only map can omit merge history
# and change timestamps. Cache by measured revision and path.
TS_OUT=0
file_timestamp() {
    local f="$1" t k
    local k="$SUJETO|$f"
    if [ -n "${_TS_FICHERO[$k]+x}" ]; then TS_OUT="${_TS_FICHERO[$k]}"; return 0; fi
    t="$(git log -1 --format=%ct -- "$f" 2>/dev/null)"
    _TS_FICHERO[$k]="${t:-0}"
    TS_OUT="${t:-0}"
    return 0
}

# fuentes_de_captura returns the named component and two import levels in FUENTES_OUT.
# A missing component returns 1 with empty output so the caller uses the fallback clock.
FUENTES_OUT=""
IMPORTS_OUT=""
fuentes_de_captura() {
    local base slug raiz n1 n2 f acc
    base="$(basename "$1" .png)"
    slug="${base%-dark}"; slug="${slug%-light}"
    if [ -n "${_FUENTES[$slug]+x}" ]; then
        FUENTES_OUT="${_FUENTES[$slug]}"
        [ -n "$FUENTES_OUT" ] || return 1
        return 0
    fi
    raiz="$(grep -E "/(${slug}|${slug}-view|${slug}-page)\.tsx$" "$INDICE_FUENTES" 2>/dev/null | head -1)"
    if [ -z "$raiz" ]; then _FUENTES[$slug]=""; FUENTES_OUT=""; return 1; fi
    acc="$raiz"
    imports_de "$raiz"; n1="$IMPORTS_OUT"
    if [ -n "$n1" ]; then
        acc="${acc}
${n1}"
        while IFS= read -r f; do
            [ -n "$f" ] || continue
            imports_de "$f"; n2="$IMPORTS_OUT"
            [ -n "$n2" ] && acc="${acc}
${n2}"
        done <<NIVEL1
${n1}
NIVEL1
    fi
    acc="$(printf '%s\n' "$acc" | sort -u | sed '/^$/d')"
    _FUENTES[$slug]="$acc"
    FUENTES_OUT="$acc"
    return 0
}

# Return the comparison timestamp in RELOJ and its source in RELOJ_ORIGEN.
RELOJ=0
RELOJ_ORIGEN=UI_TS
reloj_de_captura() {
    local maxi f
    RELOJ="$UI_TS"; RELOJ_ORIGEN=UI_TS
    fuentes_de_captura "$1" || return 0
    [ -n "$FUENTES_OUT" ] || return 0
    maxi=0
    while IFS= read -r f; do
        [ -n "$f" ] || continue
        file_timestamp "$f"
        [ "$TS_OUT" -gt "$maxi" ] && maxi="$TS_OUT"
    done <<FUENTES
${FUENTES_OUT}
FUENTES
    if [ "$maxi" -gt 0 ]; then RELOJ="$maxi"; RELOJ_ORIGEN=fuente; fi
    return 0
}

VIEJAS=0
FIJADAS=0
DENTRO=0
PEOR=0
POR_FUENTE=0
POR_UITS=0
while IFS= read -r f; do
    [ -z "$f" ] && continue
    ts="$(git log -1 --format=%ct -- "$f" 2>/dev/null)"
    [ -z "${ts:-}" ] && continue
    # Use this capture's resolved source clock, falling back to UI_TS.
    reloj_de_captura "$f"
    if [ "$RELOJ_ORIGEN" = "fuente" ]; then POR_FUENTE=$((POR_FUENTE+1)); else POR_UITS=$((POR_UITS+1)); fi
    [ "$ts" -ge "$RELOJ" ] && continue

    fijada=0
    for i in "${!PIN_RUTA[@]}"; do
        case "$f" in "${PIN_RUTA[$i]}"*) fijada=1; PIN_USADO[$i]=1; break ;; esac
    done
    if [ "$fijada" -eq 1 ]; then
        FIJADAS=$((FIJADAS+1))
        continue
    fi

    d=$(( (RELOJ - ts) / 86400 ))
    [ "$d" -gt "$PEOR" ] && PEOR="$d"
    if [ "$d" -le "$TOLERANCIA" ]; then
        DENTRO=$((DENTRO+1))
        continue
    fi

    VIEJAS=$((VIEJAS+1))
    # Keep the complete stale list; display only the first 12 entries and report the
    # list path.
    printf '%s\t%s\n' "$f" "$d" >> "$LISTA_COMPLETA"
    [ "$VIEJAS" -le 12 ] && echo "check-screenshot-freshness: ⛔ $f is $d day(s) older than the UI it depicts"
done <<EOF
$CAPS
EOF

# Reject changed HEAD after reading capture inputs.
verifica_sujeto

# Every registered pin must actually skip an older matching capture; unused declarations
# exit 2.
for i in "${!PIN_RUTA[@]}"; do
    if [ "${PIN_USADO[$i]}" -eq 0 ]; then
        echo "check-screenshot-freshness: ⛔ COULD NOT CHECK: declaration '${PIN_RUTA[$i]}' covers no screenshots." >&2
        echo "                            The path changed or the image was recaptured; remove it from the register." >&2
        exit 2
    fi
done

# Print every honored pin with its class and anchor even when the result succeeds.
for i in "${!PIN_RUTA[@]}"; do
    echo "check-screenshot-freshness: · pinned ${PIN_RUTA[$i]} [${PIN_CLASE[$i]} → ${PIN_ANCLA[$i]}]"
done

# Report how many captures used resolved sources and how many used the fallback.
echo "check-screenshot-freshness: timestamps — $POR_FUENTE from source imports, $POR_UITS from UI_TS (source unresolved)"
echo "check-screenshot-freshness: $VIEJAS more than $TOLERANCIA day(s) behind · $DENTRO within tolerance (worst: $PEOR) · $FIJADAS with declared age · $N examined"

# Enforce stale captures when the push changes selected UI paths, a stale image or
# an existing text file containing its path. Otherwise report the same stale list
# without failing the unrelated push.
# Missing trunk, merge base or a nonempty diff keeps enforcement enabled.
TRUNK_SHOT="${OLIVARES_SCREENSHOT_TRUNK:-origin/main}"
COBRA=1
POR_QUE_COBRA='could not determine the push scope (deny-closed)'
if [ "$VIEJAS" -gt 0 ]; then
    MB=""
    git rev-parse --verify --quiet "$TRUNK_SHOT" >/dev/null 2>&1 && \
        MB="$(git merge-base "$TRUNK_SHOT" HEAD 2>/dev/null || true)"
    if [ -n "$MB" ]; then
        TOCADO="$(git diff --name-only "$MB"...HEAD 2>/dev/null || true)"
        if [ -n "$TOCADO" ]; then
            COBRA=0
            POR_QUE_COBRA=''
            # This enforcement scan selects layout/features paths; it does not derive
            # paths
            # from the per-capture import closure.
            while IFS= read -r t; do
                [ -z "$t" ] && continue
                case "$t" in
                    web/src/components/layout/*|web/src/features/*)
                        COBRA=1; POR_QUE_COBRA="the push changes the UI dated by the captures ($t)"; break ;;
                esac
            done <<TOC
$TOCADO
TOC
            # Also enforce edits to stale images or files containing their paths.
            if [ "$COBRA" -eq 0 ]; then
                while IFS="$(printf '\t')" read -r vieja _dias; do
                    [ -z "$vieja" ] && continue
                    while IFS= read -r t; do
                        [ -z "$t" ] && continue
                        if [ "$t" = "$vieja" ]; then
                            COBRA=1; POR_QUE_COBRA="the push changes capture $vieja"; break
                        fi
                        # Only existing text files can supply capture references; binary
                        # matches do not count.
                        if [ -f "$t" ] && grep -Iq -F -- "$vieja" "$t" 2>/dev/null; then
                            COBRA=1; POR_QUE_COBRA="the push changes $t, which references $vieja"; break
                        fi
                    done <<TOC2
$TOCADO
TOC2
                    [ "$COBRA" -eq 1 ] && break
                done < "$LISTA_COMPLETA"
            fi
        fi
    fi
fi

if [ "$VIEJAS" -gt 0 ] && [ "$COBRA" -eq 0 ]; then
    echo "check-screenshot-freshness: ⚠ $VIEJAS screenshot(s) are more than $TOLERANCIA day(s) behind"
    echo "                            (worst: $PEOR), but this push changes none of them, their UI,"
    echo "                            or their references. Reported without blocking:"
    echo "                            the backlog needs work, but is outside this push's scope."
    echo "                            Complete list: $LISTA_COMPLETA"
    echo "check-screenshot-freshness: OK (with warning) — scoped to the push diff against $TRUNK_SHOT."
    exit 0
fi

if [ "$VIEJAS" -gt 0 ]; then
    echo "check-screenshot-freshness: · the complete list of $VIEJAS stale screenshots"\
         "(output above is limited to 12) is at: $LISTA_COMPLETA" >&2
    echo "check-screenshot-freshness: ⛔ public material depicts a product state that no longer exists:" >&2
    echo "                            $VIEJAS screenshot(s) are more than $TOLERANCIA day(s) behind (worst: $PEOR)." >&2
    echo "                            Recapture (scripts/docs-captures.sh), or declare in" >&2
    echo "                            design/screenshot-pins.txt why the image remains accurate." >&2
    exit 1
fi

# Lag tolerance is caller policy; do not derive it from the current worst lag.
echo "check-screenshot-freshness: OK — no screenshots are more than $TOLERANCIA day(s) behind the UI."
exit 0
