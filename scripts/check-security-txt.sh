#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-security-txt.sh — regression guard for the RFC 9116 security.txt that the
# docs site (docs.olivares.ai) serves at /.well-known/security.txt. Fails the gate
# if the file is missing, drops a mandatory field, expires (or is dated more than a
# year out), drifts from the SECURITY.md security contact, or loses the text/plain
# Content-Type header the RFC requires.
#
# This is the "Trust Tier 0" proof behind the verifiable-open-core pitch: a published,
# machine-discoverable security contact and disclosure-policy pointer. Signing/attesting
# proves provenance, not confidentiality — this guard only keeps the contact honest and
# in sync, it asserts nothing about code protection.
#
# Pure POSIX sh, no network. Wired into the REAL gate directly from .githooks/pre-push
# and .github/workflows/mainline-ci.yml — NOT only the `lint:` aggregate target, which
# nothing invokes (`lint:go` is red — and the cause is THE CODE, not the toolchain: medido el
# 2026-08-07 con golangci-lint 2.12.2 compilado con go1.26.5 sobre los ONCE módulos de `go.work`,
# **473 hallazgos en nueve de ellos**. La atribución vieja «golangci-lint lo mantiene rojo bajo
# go1.26» era FALSA y tenía coste real: mientras la culpa fuese de la toolchain no había nada que
# arreglar, y 473 hallazgos se leían como ruido esperado). A guard that does not run
# is worth nothing; see the orphaned scripts/check-docs-honesty.sh for the trap.
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
SECTXT="$ROOT/docs-site/public/.well-known/security.txt"
HEADERS="$ROOT/docs-site/public/_headers"
SECURITY_MD="$ROOT/SECURITY.md"
CONTACT_EMAIL="security@olivares.ai"

# WHERE WE ACTUALLY SERVE THE FILE. One URI per line, no blanks.
#
# RFC 9116 §2.5.2 requires the file to be served at EVERY Canonical it declares,
# and this gate deliberately makes NO network request: a gate that needs the
# network fails exactly where the network is worst, and this project already
# pulled a job off a runner for that reason. The offline equivalent is a
# reviewed record of where the file is published, checked in BOTH directions
# against what the file declares — so neither a dead pointer nor a silent
# omission survives.
#
# Until 2026-08-01 this was a single value and the gate REQUIRED that exact line
# to be present, so it was green while `docs.olivares.ai` returned HTTP 000 for
# months: a check that asserted a declaration and never asked whether anything
# answered. Both URIs below were measured returning 200 on 2026-08-01, after the
# subdomain was pointed at the web Worker. Adding a URI here is a commitment to
# serve it; removing one without removing the Canonical line fails the gate.
# ⛔ A REVIEWED RECORD IS ONLY AS TRUE AS ITS LAST REVIEW, AND NOTHING WAS REVIEWING IT.
# The 2026-08-01 note above says both URIs "were measured returning 200". Twelve days is how long
# that stayed unexamined, and the record carried no date, so nothing could tell a fresh
# measurement from a stale one. That is the defect being fixed here — not the values themselves.
#
# Re-measured 2026-08-13, FIVE attempts each, because one sample is not a measurement:
#
#   https://olivares.ai/.well-known/security.txt        5/5  200  text/plain (no charset)
#   https://docs.olivares.ai/.well-known/security.txt   5/5  200  text/plain (no charset)
#
# ⛔ AND THE FIRST CUT OF THIS COMMIT GOT IT BACKWARDS, WHICH IS WHY --probe NOW REPEATS.
# A single curl to docs.olivares.ai returned 000, and `socket.gethostbyname` raised, so the host
# was written off as non-existent: this record dropped it and the published file LOST a working
# Canonical line. Both were wrong. The host resolves (A 188.114.96.5/97.5, AAAA present) and
# serves the file every time once the box is not saturated — the failures were egress from this
# container while heavy gates ran, not the host. One sample off a loaded machine is not a
# measurement, and the verdict it produced was confident, wrong, and DELETED something that
# worked. Both URIs stay declared and served.
#
# ⛔ 2026-08-19 — LA ENTRADA DE docs.olivares.ai SE RETIRA: EL NOMBRE YA NO EXISTE.
# El párrafo de arriba dice, con razón para su fecha, que retirarla fue un error: se hizo con UNA
# muestra desde una caja saturada y borró un Canonical que servía. Por eso lo que sigue no es otra
# muestra suelta — es la medida CALIBRADA que aquella no tuvo, y el discriminante va explícito:
#
#   1 · Control positivo sobre el MISMO instrumento. `getent hosts` resuelve olivares.ai
#       (2a06:98c1:3120::5), alma.olivares.ai (2a06:98c1:3121::5) y licenses.olivares.ai en la misma
#       corrida, y falla SÓLO en docs.olivares.ai. Si fuese el egreso de esta caja, fallarían los
#       cuatro. Ésta es justo la calibración que faltaba en 2026-08-13.
#   2 · Resolutor INDEPENDIENTE del local. Cloudflare DoH: docs.olivares.ai → Status 3 (NXDOMAIN),
#       0 respuestas; olivares.ai → Status 0, 2 respuestas. NXDOMAIN no es «no contesta ahora»: es
#       «el nombre no está publicado».
#   3 · Cinco intentos HTTP, como exige este fichero: 5/5 000, y `socket.gethostbyname` levanta
#       gaierror -2.
#
# ⇒ El registro de 2026-08-13 era cierto: el subdominio existía y servía. Entre esa fecha y hoy
# DEJÓ de existir. Un registro revisado sólo vale hasta su última revisión, que es exactamente lo
# que este bloque lleva escrito desde el 08-13; hoy le toca a él.
#
# Y la dirección ya estaba decidida, así que esto aplica una decisión y no toma una — así se
# decidió el 2026-08-01, citado en core/api/stability.go:54: «ahora mismo están en
# olivares.ai/docs …
# [docs.olivares.ai] es irrelevante». El apex, medido hoy 5/5: 200 text/plain; charset=utf-8.
#
# Lo que decía este hueco y HA DEJADO DE SER CIERTO: describía docs.olivares.ai como sostenido por
# el Worker de marketing y el `Link` de estabilidad como apuntando a
# docs.olivares.ai/reference/api-stability/ desde core/api/stability.go:52. Ese enlace ya no existe:
# stabilityPolicyURL es hoy «https://olivares.ai/docs». La página Diátaxis sigue sin desplegarse.
#
# Each line is: <uri> <ISO date it was last measured serving>. The gate refuses an entry older
# than MAX_RECORD_AGE_DAYS, and `--probe` (network, never run by the hook) re-measures and prints
# the lines to paste back, so "re-measure" never means "remember how".
MAX_RECORD_AGE_DAYS=45
SERVED_CANONICALS="https://olivares.ai/.well-known/security.txt 2026-08-19"

fail() { echo "security-txt: FAIL — $1" >&2; exit 1; }

# --probe: the ONE place a network call is allowed, and the hook never runs it.
#
# The record above is only as true as its last review, and the review is what went missing. This
# makes re-measuring a single command whose output is the lines to paste back, so "re-measure"
# never means "remember how". It measures what the RFC requires and this file's own history
# missed: that the URI answers 200, AND that it answers with the charset (RFC 9116 §3) — the
# live host was measured on 2026-08-13 serving `text/plain` with no charset parameter at all,
# which the offline header check cannot see because the file it reads governs a different host.
if [ "${1:-}" = "--probe" ]; then
  # ⛔ TERCERA RESPUESTA: sin `curl` no se sondeó NADA. `fail` sale 1, que en este guion
  #    significa «el security.txt está mal» — y no lo está: es que no se pudo mirar el host. Un
  #    punto ciego con el nombre de un defecto manda a alguien a editar un fichero correcto.
  if ! command -v curl >/dev/null 2>&1; then
    echo "security-txt: ⛔ NO HE PODIDO MIRAR — '--probe' necesita curl y no está en este host. No se sondeó ningún canónico, así que esto NO dice nada sobre el fichero servido." >&2
    exit 2
  fi
  today="$(date -u +%F)"
  rc=0
  echo "security-txt: probing the declared Canonical URIs (network) — $today"
  # ⛔ FIVE ATTEMPTS, NOT ONE, AND THIS IS THE WHOLE LESSON OF THE COMMIT THAT ADDED IT.
  # A single probe returned 000 for docs.olivares.ai and was read as "the host does not exist".
  # It does: it resolves and serves the file, but answers intermittently from here. One sample
  # off an intermittent host is not a measurement, and the verdict it produced was confident and
  # wrong in the direction that DELETED a working Canonical from the published file.
  attempts=5
  for uri in $(sed -n 's/^[Cc]anonical:[[:space:]]*//p' "$SECTXT" | tr -d '\r'); do
    ok=0; codes=''; ctype=''
    i=0
    while [ "$i" -lt "$attempts" ]; do
      i=$((i + 1))
      out="$(curl -sS -o /dev/null --max-time 25 -w '%{http_code} %{content_type}' "$uri" 2>/dev/null || echo '000 ')"
      code="${out%% *}"
      codes="$codes $code"
      if [ "$code" = "200" ]; then ok=$((ok + 1)); ctype="${out#* }"; fi
    done
    printf '  %-56s %d/%d 200 (%s)  %s\n' "$uri" "$ok" "$attempts" "${codes# }" "${ctype:-<no 200 seen>}"
    [ "$ok" -gt 0 ] || { echo "    ^ never served the file in $attempts attempts; drop the Canonical or fix the host" >&2; rc=1; }
    [ "$ok" -eq "$attempts" ] || echo "    ^ INTERMITTENT — it serves, but not every time. Not a verdict on the host: this vantage point cannot tell a flaky host from flaky egress to it." >&2
    case "$ctype" in
      *charset=utf-8*) ;;
      *) echo "    ^ RFC 9116 §3 wants 'text/plain; charset=utf-8' — this host sends '${ctype:-<none>}'" >&2; rc=1 ;;
    esac
    echo "    record line: $uri $today"
  done
  exit "$rc"
fi

# ---- existence --------------------------------------------------------------
[ -f "$SECTXT" ] || fail "missing $SECTXT — the docs site must serve it at /.well-known/security.txt (RFC 9116)"

# ---- mandatory + expected RFC 9116 fields (names are case-insensitive, §2.4) ----
grep -qiE '^Contact:[[:space:]]*'   "$SECTXT" || fail "no Contact: field (RFC 9116 §2.5.3, MANDATORY)"
grep -qiE '^Expires:[[:space:]]*'   "$SECTXT" || fail "no Expires: field (RFC 9116 §2.5.5, MANDATORY)"
grep -qiE '^Canonical:[[:space:]]*' "$SECTXT" || fail "no Canonical: field (RFC 9116 §2.5.2)"
grep -qiE '^Policy:[[:space:]]*'    "$SECTXT" || fail "no Policy: field — must point at the disclosure policy (SECURITY.md)"

# Expires MUST appear exactly once (RFC 9116 §2.5.5).
exp_count="$(grep -ciE '^Expires:' "$SECTXT")"
[ "$exp_count" -eq 1 ] || fail "found $exp_count Expires: fields — RFC 9116 §2.5.5 requires exactly one"

# Preferred-Languages MUST appear at most once (RFC 9116 §2.5.8).
pl_count="$(grep -ciE '^Preferred-Languages:' "$SECTXT" || true)"
[ "$pl_count" -le 1 ] || fail "found $pl_count Preferred-Languages: lines — RFC 9116 §2.5.8 allows at most one"

# ---- Contact consistency with SECURITY.md -----------------------------------
grep -qiE "^Contact:[[:space:]]*mailto:${CONTACT_EMAIL}[[:space:]]*$" "$SECTXT" \
  || fail "Contact: is not 'mailto:${CONTACT_EMAIL}' — it must match the reporting address in SECURITY.md"
if [ -f "$SECURITY_MD" ]; then
  grep -q "$CONTACT_EMAIL" "$SECURITY_MD" \
    || fail "SECURITY.md no longer mentions ${CONTACT_EMAIL} — the security.txt Contact would be inconsistent"
fi

# ---- Canonical <-> served locations agree, BOTH ways (RFC 9116 §2.5.2) ------
# Direction 1: nothing is declared that we do not serve — the docs.olivares.ai
# regression, green for months against HTTP 000. Direction 2: nothing we serve
# is left undeclared, or a consumer that fetched the file from there cannot tell
# it is the genuine one.
declared="$(sed -n 's/^[Cc]anonical:[[:space:]]*//p' "$SECTXT" | tr -d '\r' | sed 's/[[:space:]]*$//')"
[ -n "$declared" ] || fail "no Canonical: value could be read (RFC 9116 §2.5.2)"
# The record is "<uri> <ISO date measured>", so compare on the URI and judge the date apart.
served_uris="$(printf '%s\n' "$SERVED_CANONICALS" | awk 'NF { print $1 }')"
for d in $declared; do
  printf '%s\n' "$served_uris" | grep -qxF "$d" \
    || fail "Canonical: declares ${d}, which is not in SERVED_CANONICALS — either serve it or drop the line (RFC 9116 §2.5.2 requires the file to be served at every Canonical)"
done
for s in $served_uris; do
  printf '%s\n' "$declared" | grep -qxF "$s" \
    || fail "we serve ${s} but the file does not declare it as Canonical (RFC 9116 §2.5.2)"
done

# ---- the record's OWN freshness (the defect that made this necessary) -------
# A reviewed record replaced a network call and then went stale in twelve days, green the whole
# time. Ageing it out is what turns "nobody has checked" into a red instead of a silence — the
# same distinction this repository draws everywhere else: clean, broken, or NOT LOOKED AT.
if date -u -d '1970-01-01' +%s >/dev/null 2>&1; then
  now_epoch="$(date -u +%s)"
  printf '%s\n' "$SERVED_CANONICALS" | while IFS= read -r line; do
    [ -n "$line" ] || continue
    uri="$(printf '%s' "$line" | awk '{print $1}')"
    when="$(printf '%s' "$line" | awk '{print $2}')"
    [ -n "$when" ] || { echo "security-txt: FAIL — SERVED_CANONICALS entry for ${uri} carries no measured-on date" >&2; exit 1; }
    when_epoch="$(date -u -d "$when" +%s 2>/dev/null)" \
      || { echo "security-txt: FAIL — SERVED_CANONICALS date ${when} for ${uri} is not a date" >&2; exit 1; }
    age_days="$(( (now_epoch - when_epoch) / 86400 ))"
    [ "$age_days" -le "$MAX_RECORD_AGE_DAYS" ] \
      || { echo "security-txt: FAIL — the record for ${uri} was last measured ${age_days} days ago (limit ${MAX_RECORD_AGE_DAYS}). Re-measure with: bash scripts/check-security-txt.sh --probe" >&2; exit 1; }
  done || exit 1
fi

# ---- Expires freshness: not in the past, and < ~1 year out (RFC 9116 §2.5.5) ----
expires_val="$(sed -n 's/^[Ee]xpires:[[:space:]]*//p' "$SECTXT" | head -n1 | tr -d '\r')"
[ -n "$expires_val" ] || fail "could not read the Expires: value"
# GNU date (present in CI and the dev container) parses RFC 3339. Strip any fractional
# seconds first for maximal portability. Probe the binary ONCE on a known-good value so
# "no GNU date" (degrade honestly — skip only the temporal check) is told apart from
# "Expires is garbage" (a real RFC 9116 §2.5.5 violation that MUST fail, never WARN-pass).
expires_norm="$(printf '%s' "$expires_val" | sed 's/\.[0-9][0-9]*Z$/Z/')"
if date -u -d '1970-01-01T00:00:00Z' +%s >/dev/null 2>&1; then
  exp_epoch="$(date -u -d "$expires_norm" +%s 2>/dev/null)" \
    || fail "Expires ($expires_val) is not a valid RFC 3339 timestamp (RFC 9116 §2.5.5)"
  now_epoch="$(date -u +%s)"
  [ "$exp_epoch" -gt "$now_epoch" ] \
    || fail "Expires ($expires_val) is in the past — renew it (RFC 9116 §2.5.5 says consumers treat an expired file as stale)"
  # 1 year + a leap day of slack = 366 days = 31622400 seconds.
  [ "$((exp_epoch - now_epoch))" -le 31622400 ] \
    || fail "Expires ($expires_val) is more than a year out — RFC 9116 §2.5.5 recommends less than one year"
else
  echo "security-txt: WARN — GNU 'date -u -d' unavailable; skipping Expires freshness check (field present: $expires_val)" >&2
fi

# ---- Content-Type header (RFC 9116 §3: text/plain; charset=utf-8) -----------
[ -f "$HEADERS" ] \
  || fail "missing $HEADERS — the docs site must set Content-Type for /.well-known/security.txt (RFC 9116 §3)"
grep -q '/\.well-known/security\.txt' "$HEADERS" \
  || fail "$HEADERS has no rule for /.well-known/security.txt (RFC 9116 §3 requires it served as text/plain; charset=utf-8)"
# The Content-Type MUST belong to the /.well-known/security.txt rule BLOCK specifically —
# not merely appear somewhere (e.g. on a /* catch-all) while security.txt is served as
# something else. In a Cloudflare _headers file a path line starts at column 0 and its
# header lines are the indented lines up to the next column-0 path line.
ct_ok="$(awk '
  /^[^[:space:]#]/ {
    line=$0; sub(/[[:space:]]+$/,"",line)
    in_block = (line == "/.well-known/security.txt"); next
  }
  in_block {
    h=tolower($0); sub(/[[:space:]]+$/,"",h)
    if (h ~ /^[[:space:]]*content-type:[[:space:]]*text\/plain;[[:space:]]*charset=utf-8$/) found=1
  }
  END { print (found ? "yes" : "no") }
' "$HEADERS")"
[ "$ct_ok" = yes ] \
  || fail "$HEADERS does not set 'Content-Type: text/plain; charset=utf-8' in the /.well-known/security.txt rule block (RFC 9116 §3)"

echo "security-txt: OK (RFC 9116 fields present, Expires fresh and < 1yr, Contact matches SECURITY.md, Content-Type set)"
