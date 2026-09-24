#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-docs-capture-seeding.sh — the estate seeding gate of scripts/docs-captures.sh, pinned at the
# harness's own seam.
#
# WHY. On 2026-09-23 a hosted capture run published a set in which ten captures of five views had
# gone from populated to empty. The estate seeder could not find its targets and exited 2 (it could
# not look). The harness swallowed every nonzero exit of the seeder and published anyway.
#
# HOW. This battery cuts two blocks out of docs-captures.sh by their anchors: the estate seeding
# block and the publication block. It runs them in that order, in a scratch checkout that holds a
# committed capture set, with PUBLICAR=1. Between the two it stands in for the captures with new
# images, as a green run would leave them. Nothing else of the harness runs: there is no engine,
# browser or network. A Python start-up hook records every HTTP request a seeder attempts and
# answers none of them.
#
# WHAT IS PINNED.
#   · The seeder's exit decides. 0 continues and publishes; 1 exits 1; 2 and any other exit exit 2.
#     On every nonzero exit nothing is captured or published, and the committed set and its
#     manifest are byte-identical afterwards, even with an earlier partial capture on disk.
#   · The targets are the public fixture, passed explicitly and rooted at the checkout.
#   · The real seeder answers a missing, unreadable, malformed or structurally invalid fixture with
#     2 before it attempts any request, and the harness stops.
#   · The fixture is the approved one, byte for byte.
#   · The publication block refuses by itself unless the seed exited 0.
#   · A consumer that swallows the seeder's exit (the defect, built as a mutant) continues and
#     publishes, so the cases above can tell the two apart.
#
# Optional witnesses of the code before this correction; they are not needed to pass:
#   DOCS_CAPTURES_BEFORE=<file>       an earlier docs-captures.sh, run with a seeder exiting 2.
#   SEED_ESTATE_VOLUME_BEFORE=<file>  an earlier seed-estate-volume.py, run on invalid fixtures.
#
# Exit: 0 every case holds · 1 a case failed · 2 could not look.
set -uo pipefail
export LC_ALL=C

RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
# The scratch checkouts are git repositories: shed the ambient git environment before the first one.
# shellcheck source=lib/git-env.sh
. "$RAIZ/scripts/lib/git-env.sh" || {
	echo "test-docs-capture-seeding: CANNOT LOOK: scripts/lib/git-env.sh did not load" >&2
	exit 2
}
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=battery GIT_AUTHOR_EMAIL=battery@example.invalid
export GIT_COMMITTER_NAME=battery GIT_COMMITTER_EMAIL=battery@example.invalid

DC="$RAIZ/scripts/docs-captures.sh"
SEEDER="$RAIZ/scripts/seed-estate-volume.py"
REDACCION="$RAIZ/scripts/lib/redaccion.py"
FIXTURE="$RAIZ/scripts/fixtures/docs-capture-targets.json"
APPROVED_SHA256=6abd892c8beee25f5a8c4b4e99fea5e4486fc0d970aaf03b23640f728087104c

ok=0
fail=0
cannot=0
pass() { ok=$((ok + 1)); printf 'ok     %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf 'FAIL   %s\n' "$1"; }
blind() { cannot=$((cannot + 1)); printf 'CANNOT LOOK  %s\n' "$1"; }

for f in "$DC" "$SEEDER" "$REDACCION"; do
	[ -f "$f" ] || { echo "test-docs-capture-seeding: CANNOT LOOK: $f is missing" >&2; exit 2; }
done
T="$(mktemp -d)" || exit 2
trap 'chmod -R u+rwx "$T" 2>/dev/null; rm -rf "$T"' EXIT

# ── the harness's own blocks ──────────────────────────────────────────────────────────────────
# The seeding block starts at its target assignment (or, in the consumer before this correction, at
# its banner) and ends at the first `esac` or `}` in column 0. The publication block is the
# `PUBLICAR` conditional up to its `fi` in column 0.
cut_seed() {
	awk '!s && /^(ESTATE_TARGETS=|echo "==> Filling the estate)/ {s = 1}
		s {print}
		s && /^(esac|\})$/ {exit}' "$1"
}
cut_publish() {
	awk '!s && /^if \[ "\$\{PUBLICAR:-0\}" = "1" \]; then$/ {s = 1}
		s {print}
		s && /^fi$/ {exit}' "$1"
}
cut_seed "$DC" >"$T/seed.sh"
cut_publish "$DC" >"$T/publish.sh"
if ! grep -q 'seed-estate-volume\.py' "$T/seed.sh" || ! grep -qE '^(esac|\})$' "$T/seed.sh"; then
	echo "test-docs-capture-seeding: CANNOT LOOK: the estate seeding block of docs-captures.sh was not found" >&2
	exit 2
fi
if ! grep -q 'docs-site/public/console' "$T/publish.sh" || ! grep -q '^fi$' "$T/publish.sh"; then
	echo "test-docs-capture-seeding: CANNOT LOOK: the publication block of docs-captures.sh was not found" >&2
	exit 2
fi

# ── the request hook: every HTTP request a seeder attempts is written down and refused ───────────
mkdir -p "$T/hook"
cat >"$T/hook/sitecustomize.py" <<'PY'
import os
import urllib.error
import urllib.request

_log = os.environ.get("SEED_REQUESTS")
if _log:
    def _refuse(self, fullurl, *args, **kwargs):
        url = fullurl if isinstance(fullurl, str) else fullurl.full_url
        with open(_log, "a", encoding="utf-8") as fh:
            fh.write(url + "\n")
        raise urllib.error.URLError("refused by test-docs-capture-seeding: no network here")

    urllib.request.OpenerDirector.open = _refuse
PY

# ── the seeder double: records its arguments and exits as told ─────────────────────────────────
cat >"$T/seed-double.py" <<'PY'
import json
import os
import signal
import sys

with open(os.environ["SEED_CALLS"], "a", encoding="utf-8") as fh:
    fh.write(json.dumps(sys.argv[1:]) + "\n")
how = os.environ.get("SEED_DOUBLE_EXIT", "0")
if how == "SIGKILL":
    os.kill(os.getpid(), signal.SIGKILL)
sys.exit(int(how))
PY

# checkout <name> <seeder: double|real|FILE> -> a scratch checkout with a committed capture set,
# an earlier partial capture on disk, and the seeder and fixture in place. Prints its path.
checkout() {
	local C="$T/$1/root"
	mkdir -p "$C/docs-site/public/console" "$C/scripts/lib" "$C/scripts/fixtures" "$C/web/playwright-report/docs"
	printf 'committed view light\n' >"$C/docs-site/public/console/view-light.png"
	printf 'committed view dark\n' >"$C/docs-site/public/console/view-dark.png"
	printf '{"commit": "committed", "captures": 2}\n' >"$C/docs-site/public/console/manifest.json"
	# The harness's checkout ignores its reports; here the scripts are scratch too.
	printf 'web/playwright-report/\nscripts/\n' >"$C/.gitignore"
	case "$2" in
	double) cp "$T/seed-double.py" "$C/scripts/seed-estate-volume.py" ;;
	real) cp "$SEEDER" "$C/scripts/seed-estate-volume.py" ;;
	*) cp "$2" "$C/scripts/seed-estate-volume.py" ;;
	esac
	cp "$REDACCION" "$C/scripts/lib/redaccion.py"
	[ -f "$FIXTURE" ] && cp "$FIXTURE" "$C/scripts/fixtures/docs-capture-targets.json"
	# An earlier run's partial capture: it must never be published by a run that did not seed.
	printf 'stale partial capture\n' >"$C/web/playwright-report/docs/view-light.png"
	(cd "$C" && git init -q -b main . && git add -A && git commit -q -m committed-set) || return 1
	printf '%s' "$C"
}

# consume <checkout> <seed-block> [publish-block] — the seeding block, a marker, the stand-in
# captures and the publication block, in one shell with the harness's options. Prints the exit.
consume() {
	local C="$1" rc=0
	: >"$C/../calls"
	: >"$C/../requests"
	rm -f "$C/../continued"
	(cd "$C" && env ROOT="$C" PORT=9 TOKEN=battery-token TENANT=battery-tenant PUBLICAR=1 \
		PYTHONPATH="$T/hook${PYTHONPATH:+:$PYTHONPATH}" SEED_CALLS="$C/../calls" SEED_REQUESTS="$C/../requests" \
		bash -c '
			set -euo pipefail
			. "$1"
			echo continued >"$ROOT/../continued"
			rm -rf "$ROOT/web/playwright-report/docs"
			mkdir -p "$ROOT/web/playwright-report/docs"
			printf "new view light\n" >"$ROOT/web/playwright-report/docs/view-light.png"
			printf "new view dark\n" >"$ROOT/web/playwright-report/docs/view-dark.png"
			printf "{\"commit\": \"new\", \"captures\": 2}\n" >"$ROOT/web/playwright-report/docs/manifest.json"
			RC=0
			. "$2"
			exit "$RC"
		' consumer "$2" "${3:-$T/publish.sh}") >"$C/../out.txt" 2>&1 || rc=$?
	echo "$rc"
}
# export-closure: fixture web/playwright-report/docs/manifest.json — written by this battery in its scratch checkout

# The committed set, byte for byte, and whether git sees it touched.
committed_intact() {
	local C="$1"
	[ "$(cat "$C/docs-site/public/console/view-light.png")" = "committed view light" ] &&
		[ "$(cat "$C/docs-site/public/console/view-dark.png")" = "committed view dark" ] &&
		[ "$(cat "$C/docs-site/public/console/manifest.json")" = '{"commit": "committed", "captures": 2}' ] &&
		[ -z "$(git -C "$C" status --porcelain -- docs-site)" ]
}
published_new() {
	local C="$1"
	[ "$(cat "$C/docs-site/public/console/view-light.png")" = "new view light" ] &&
		[ "$(cat "$C/docs-site/public/console/manifest.json")" = '{"commit": "new", "captures": 2}' ]
}
refused() { # <checkout> <exit> <want-exit> — no capture taken, nothing published, the set intact
	[ "$2" = "$3" ] && [ ! -e "$1/../continued" ] && committed_intact "$1"
}

# ── 1 · the seeder's exit decides ────────────────────────────────────────────────────────────
C="$(checkout exit-0 double)" || exit 2
r="$(SEED_DOUBLE_EXIT=0 consume "$C" "$T/seed.sh")"
if [ "$r" = 0 ] && [ -e "$C/../continued" ] && published_new "$C"; then
	pass "seed exit 0: the run continues and publishes its captures"
else
	bad "seed exit 0 should continue and publish; exit $r ($(tail -2 "$C/../out.txt" | tr '\n' ' '))"
fi
want_args="[\"http://127.0.0.1:9\", \"battery-token\", \"battery-tenant\", \"--objetivos\", \"$C/scripts/fixtures/docs-capture-targets.json\"]"
if [ "$(cat "$C/../calls")" = "$want_args" ]; then
	pass "the seeder is given the public fixture explicitly, rooted at the checkout"
else
	bad "the seeder's arguments are $(cat "$C/../calls"), want $want_args"
fi
for c in "1:1:targets unmet" "2:2:inability" "3:2:an unexpected exit" "SIGKILL:2:death by a signal"; do
	how="${c%%:*}"; rest="${c#*:}"; want="${rest%%:*}"; what="${rest#*:}"
	C="$(checkout "exit-$how" double)" || exit 2
	r="$(SEED_DOUBLE_EXIT="$how" consume "$C" "$T/seed.sh")"
	if refused "$C" "$r" "$want"; then
		pass "seed exit $how ($what): the run exits $want before any capture; the committed set and manifest are intact"
	else
		bad "seed exit $how ($what) should exit $want before any capture; exit $r, continued=$([ -e "$C/../continued" ] && echo yes || echo no), set intact=$(committed_intact "$C" && echo yes || echo no)"
	fi
done
if [ "$(cat "$T/exit-2/root/web/playwright-report/docs/view-light.png")" = "stale partial capture" ] &&
	committed_intact "$T/exit-2/root"; then
	pass "an earlier partial capture on disk stays unpublished after a failed seed"
else
	bad "an earlier partial capture was replaced or published after a failed seed"
fi

# ── 2 · the defect, as a mutant and (optionally) as the consumer before this correction ─────────
python3 - "$T/seed.sh" "$T/seed-swallow.sh" <<'PY'
import sys
src = open(sys.argv[1], encoding="utf-8").read()
old = "|| SEED_RC=$?"
if src.count(old) != 1:
    sys.exit(1)
open(sys.argv[2], "w", encoding="utf-8").write(src.replace(old, '|| echo "(swallowed)"'))
PY
if [ -s "$T/seed-swallow.sh" ]; then
	C="$(checkout swallow double)" || exit 2
	r="$(SEED_DOUBLE_EXIT=2 consume "$C" "$T/seed-swallow.sh")"
	if [ -e "$C/../continued" ] && published_new "$C"; then
		pass "mutant that swallows the seeder's exit: with exit 2 it continues and publishes (the cases above discriminate)"
	else
		bad "the swallowing mutant did not continue and publish (exit $r): the refusal cases prove nothing"
	fi
else
	blind "the swallowing mutant could not be built: the gate does not capture the seeder's exit as \`|| SEED_RC=\$?\`"
fi
if [ -n "${DOCS_CAPTURES_BEFORE:-}" ]; then
	cut_seed "$DOCS_CAPTURES_BEFORE" >"$T/seed-before.sh"
	cut_publish "$DOCS_CAPTURES_BEFORE" >"$T/publish-before.sh"
	C="$(checkout before double)" || exit 2
	r="$(SEED_DOUBLE_EXIT=2 consume "$C" "$T/seed-before.sh" "$T/publish-before.sh")"
	if [ -e "$C/../continued" ] && published_new "$C"; then
		pass "the consumer before this correction: with seed exit 2 it continues and replaces the committed set (exit $r)"
	else
		bad "the consumer before this correction did not continue with seed exit 2 (exit $r)"
	fi
fi

# ── 3 · the publication block refuses by itself unless the seed exited 0 ──────────────────────────
for s in unset 1 2; do
	C="$(checkout "publish-alone-$s" double)" || exit 2
	mkdir -p "$C/web/playwright-report/docs"
	printf 'new view light\n' >"$C/web/playwright-report/docs/view-light.png"
	printf '{"commit": "new", "captures": 2}\n' >"$C/web/playwright-report/docs/manifest.json"
	r=0
	(cd "$C" && env ROOT="$C" PUBLICAR=1 bash -c '
		set -euo pipefail
		[ "$1" = unset ] || SEED_RC="$1"
		RC=0
		. "$2"
		exit "$RC"' block "$s" "$T/publish.sh") >"$C/../out.txt" 2>&1 || r=$?
	if [ "$r" != 0 ] && committed_intact "$C"; then
		pass "the publication block alone, seed exit $s: refuses (exit $r) and leaves the committed set intact"
	else
		bad "the publication block alone published or exited 0 with seed exit $s (exit $r)"
	fi
done

# ── 4 · the real seeder refuses an invalid fixture before any request, and the run stops ─────────
# fixture_case <name> <how> — builds the fixture <how>, runs the real seeder through the harness.
if [ "$(id -u)" = 0 ]; then EUID0=yes; else EUID0=no; fi
fixture_case() {
	local name=$1 how=$2 C r
	C="$(checkout "fx-$name" real)" || return 1
	local F="$C/scripts/fixtures/docs-capture-targets.json"
	case "$how" in
	missing) rm -f "$F" ;;
	directory) rm -f "$F" && mkdir "$F" ;;
	mode-000) chmod 000 "$F" ;;
	truncated) printf '{"version": 1, "superficies": [' >"$F" ;;
	not-utf8) printf '\377\376{}' >"$F" ;;
	*)
		python3 - "$F" "$how" <<'PY' || return 1
import json, sys
path, how = sys.argv[1], sys.argv[2]
d = json.load(open(path, encoding="utf-8"))
if how == "top-level-list":
    d = [d]
elif how == "version-2":
    d["version"] = 2
elif how == "surface-not-object":
    d["superficies"][3] = "routine-policies"
elif how == "listar-not-a-path":
    d["superficies"][5]["listar"] = 5
elif how == "objetivo-boolean":
    d["superficies"][2]["objetivo"] = True
elif how == "duplicate-id":
    d["superficies"][1]["id"] = d["superficies"][0]["id"]
elif how == "nonseeded-not-a-list":
    d["no_sembrables_por_api"] = "identities"
elif how == "nonseeded-without-ruta":
    del d["no_sembrables_por_api"][1]["ruta"]
elif how == "valid":
    pass
else:
    sys.exit(1)
json.dump(d, open(path, "w", encoding="utf-8"), indent=2)
PY
		;;
	esac
	r="$(consume "$C" "$T/seed.sh")"
	printf '%s %s' "$r" "$C"
}
for how in missing directory mode-000 truncated not-utf8 top-level-list version-2 surface-not-object \
	listar-not-a-path objetivo-boolean duplicate-id nonseeded-not-a-list nonseeded-without-ruta; do
	if [ "$how" = mode-000 ] && [ "$EUID0" = yes ]; then
		printf 'NOT EXERCISED  fixture mode-000: this battery runs as root, which reads it anyway; the directory case covers the read failure\n'
		continue
	fi
	out="$(fixture_case "$how" "$how")" || { blind "fixture $how: the case could not be built"; continue; }
	r="${out%% *}"; C="${out#* }"
	if refused "$C" "$r" 2 && [ ! -s "$C/../requests" ] && grep -q 'NO HE PODIDO MIRAR' "$C/../out.txt"; then
		pass "fixture $how: the seeder exits 2 before any request, and the run stops before any capture"
	else
		bad "fixture $how: exit $r, requests=[$(tr '\n' ' ' <"$C/../requests")], continued=$([ -e "$C/../continued" ] && echo yes || echo no): $(grep -m1 'seed-estate-volume' "$C/../out.txt" | cut -c1-160)"
	fi
done
# The hook sees requests: with the approved fixture the seeder passes validation and asks the engine.
out="$(fixture_case valid valid)" || out="x x"
r="${out%% *}"; C="${out#* }"
if [ -s "$C/../requests" ] && grep -q '/v1/agents' "$C/../requests" && [ "$r" = 2 ] && [ ! -e "$C/../continued" ]; then
	pass "control: the approved fixture passes validation and the seeder's first request is recorded (so 'no request' above means none)"
else
	bad "control: the approved fixture did not reach the engine through the hook (exit $r, requests=[$(tr '\n' ' ' <"$C/../requests" 2>/dev/null)])"
fi
if [ -n "${SEED_ESTATE_VOLUME_BEFORE:-}" ]; then
	for how in directory not-utf8 top-level-list nonseeded-not-a-list; do
		C="$(checkout "before-$how" "$SEED_ESTATE_VOLUME_BEFORE")" || continue
		F="$C/scripts/fixtures/docs-capture-targets.json"
		: >"$C/../requests"
		case "$how" in
		directory) rm -f "$F" && mkdir "$F" ;;
		not-utf8) printf '\377\376{}' >"$F" ;;
		top-level-list) python3 -c 'import json,sys; p=sys.argv[1]; json.dump([json.load(open(p))], open(p, "w"))' "$F" ;;
		nonseeded-not-a-list) python3 -c 'import json,sys; p=sys.argv[1]; d=json.load(open(p)); d["no_sembrables_por_api"]="identities"; json.dump(d, open(p, "w"))' "$F" ;;
		esac
		(cd "$C" && env PYTHONPATH="$T/hook${PYTHONPATH:+:$PYTHONPATH}" SEED_REQUESTS="$C/../requests" \
			python3 scripts/seed-estate-volume.py http://127.0.0.1:9 t t --objetivos "$F") >"$C/../out.txt" 2>&1
		printf 'before  seeder before this correction, fixture %s: exit %s, requests [%s]\n' "$how" "$?" \
			"$(tr '\n' ' ' <"$C/../requests" 2>/dev/null)"
	done
fi

# ── 5 · the fixture is the approved one ──────────────────────────────────────────────────────
if [ ! -f "$FIXTURE" ]; then
	bad "scripts/fixtures/docs-capture-targets.json is missing"
elif [ "$(sha256sum "$FIXTURE" | cut -d' ' -f1)" != "$APPROVED_SHA256" ]; then
	bad "scripts/fixtures/docs-capture-targets.json is not the approved file (sha256 $APPROVED_SHA256)"
else
	pass "the fixture is the approved file, byte for byte (sha256 ${APPROVED_SHA256:0:12}…)"
fi
if [ -f "$FIXTURE" ] && python3 - "$FIXTURE" <<'PY'; then
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
assert sorted(d) == ["no_sembrables_por_api", "superficies", "version"] and d["version"] == 1
assert len(d["superficies"]) == 13 and all(sorted(s) == ["bajo", "id", "listar", "objetivo"] for s in d["superficies"])
assert len(d["no_sembrables_por_api"]) == 2 and all(sorted(s) == ["id", "ruta"] for s in d["no_sembrables_por_api"])
PY
	pass "the fixture holds version 1, 13 surfaces (id, objetivo, listar, bajo) and 2 non-seeded pairs (id, ruta), nothing else"
else
	bad "the fixture's shape is not version 1, 13 surfaces and 2 non-seeded pairs with exactly their fields"
fi

# ── 6 · the consumer reads nothing outside the published tree ────────────────────────────────
# (The private launch directory's name is assembled here, so this file never refers to it.)
LAUNCH_DIR="docs/""launch"
if grep -q "$LAUNCH_DIR" "$DC"; then
	bad "docs-captures.sh still refers to the private launch directory"
else
	pass "docs-captures.sh refers to no private launch path"
fi

printf '\ntest-docs-capture-seeding: %d pass, %d fail, %d could not look\n' "$ok" "$fail" "$cannot"
[ "$fail" -eq 0 ] || exit 1
[ "$cannot" -eq 0 ] || exit 2
exit 0
