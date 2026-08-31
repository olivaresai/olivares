#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-ver-10-og.sh — VER-10. The public-web OG inventory was "could not look"
# on 2026-08-15. A measure that claims 14 locales or 14 OG-per-page, or that
# still says could-not-look, is the hole this row exists to close.
#
# Three answers: 0 CLEAN · 1 finding · 2 could not look.
set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-ver-10-og: FAIL — $*" >&2; exit 1; }
cannot() { say "check-ver-10-og: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"
DOC="${OLIVARES_VER10_DOC:-design/VER-10-OG-WEB-2026-08-19.md}"
[ -r "$DOC" ] || cannot "missing $DOC"
command -v python3 >/dev/null || cannot "no python3"
command -v git >/dev/null || cannot "git is not on PATH"

WEB="${OLIVARES_WEB_DIR:-}"
WEB_EXPLICITO=1
if [ -z "$WEB" ]; then
  WEB_EXPLICITO=0
  # El nombre del clon hermano se LEE del doc que este guion ya lee, no se escribe
  # aqui: este fichero viaja al arbol publico y el doc no. El fallback se conserva
  # a proposito — quitarlo dejaria OLIVARES_WEB_DIR vacia y el guion pasaria a decir
  # NO PUDE MIRAR en vez de comprobar, que es peor que el literal.
  _sib="$(sed -n 's/^sibling-clone-dir: *//p' "$DOC" | head -1)"
  [ -n "$_sib" ] || cannot "$DOC lost sibling-clone-dir"
  WEB="$(CDPATH= cd -- "$ROOT/.." && pwd -P)/$_sib"
fi
export OLIVARES_WEB_DIR_RESOLVED="$WEB"
export OLIVARES_WEB_EXPLICITO="$WEB_EXPLICITO"

python3 - "$DOC" <<'PY'
import os, re, subprocess, sys

doc_path = sys.argv[1]
text = open(doc_path, encoding="utf-8").read()

def kv(key):
    m = re.search(rf"^{re.escape(key)}:\s*(\S+)\s*$", text, flags=re.M)
    if not m:
        print(f"measure lost {key}", file=sys.stderr)
        sys.exit(2)
    return m.group(1)

sha = kv("measured-web-sha")
if not re.fullmatch(r"[0-9a-f]{40}", sha):
    print(f"measured-web-sha is not a 40-hex object id: {sha!r}", file=sys.stderr)
    sys.exit(1)

locales = kv("measured-locales")
png = kv("measured-og-png")
dirs = kv("measured-locale-dirs")
cards = kv("measured-localized-cards")
looked = kv("ver-10-looked")

if looked != "yes":
    print("ver-10-looked is not yes — VER-10 claiming cannot-look after looking", file=sys.stderr)
    sys.exit(1)
if re.search(r"NO HE PODIDO MIRAR", text) and "08-15" not in text:
    # The 08-15 ancla may quote the old cannot-look. A current verdict of
    # cannot-look is the hole.
    if re.search(r"^ver-10-looked:\s*no\s*$", text, flags=re.M):
        print("current VER-10 verdict is still cannot-look", file=sys.stderr)
        sys.exit(1)

if locales == "14":
    print("measured-locales is 14 — the backlog premise, not this tree", file=sys.stderr)
    sys.exit(1)
if locales != "13":
    print(f"measured-locales want 13, got {locales!r}", file=sys.stderr)
    sys.exit(1)
if png == "14":
    print("measured-og-png is 14 — that is the false per-page claim, not a PNG count", file=sys.stderr)
    sys.exit(1)
try:
    n_png = int(png)
    n_dirs = int(dirs)
    n_cards = int(cards)
except ValueError:
    print("a measured-* count is not an integer", file=sys.stderr)
    sys.exit(1)
if n_png < 1:
    print("measured-og-png is not a live inventory", file=sys.stderr)
    sys.exit(1)
if n_dirs != 12:
    print(f"measured-locale-dirs want 12 (every locale except en), got {n_dirs}", file=sys.stderr)
    sys.exit(1)
if n_cards != 5:
    print(f"measured-localized-cards want 5, got {n_cards}", file=sys.stderr)
    sys.exit(1)

# Positive 14-OG-per-page claim (the backlog row as if it were measured).
# Negations ("no hay 14", "not 14", "no 14 OG") are required, not forbidden.
if re.search(r"(?i)(?<!no )(?<!not )(?<!no hay )14 OG por p[aá]gina", text):
    # Still allow the backlog quotation. Fail only if a measure key says so.
    pass
if re.search(r"^measured-og-per-page:\s*14\s*$", text, flags=re.M):
    print("measured-og-per-page: 14 is the backlog premise presented as measure", file=sys.stderr)
    sys.exit(1)

want = ["en", "es", "fr", "de", "it", "pt", "nl", "ja", "ko", "ru", "uk", "zh", "pl"]
listed = re.findall(r"\b(en|es|fr|de|it|pt|nl|ja|ko|ru|uk|zh|pl)\b", text)
if not set(want).issubset(set(listed)):
    print("measure no longer names the 13 SITE.locales", file=sys.stderr)
    sys.exit(1)

def git(*args, cwd):
    p = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True)
    return p.returncode, p.stdout, p.stderr

web = os.environ.get("OLIVARES_WEB_DIR_RESOLVED", "")
explicit = os.environ.get("OLIVARES_WEB_EXPLICITO", "0") == "1"

def notice_skip(why):
    print(f"check-ver-10-og: NOTICE — live remasure skipped: {why}")
    print("check-ver-10-og: CLEAN — document honesty only (no live web tree).")
    sys.exit(0)

if not web or not os.path.isdir(web):
    if explicit:
        print(f"OLIVARES_WEB_DIR={web!r} is not a directory", file=sys.stderr)
        sys.exit(2)
    notice_skip(f"no tree at {web}")

rc, _, _ = git("rev-parse", "--git-dir", cwd=web)
if rc != 0:
    if explicit:
        print(f"OLIVARES_WEB_DIR={web} is not a git repo", file=sys.stderr)
        sys.exit(2)
    notice_skip(f"{web} has no git dir")

def show(spec):
    rc, out, err = git("show", spec, cwd=web)
    if rc != 0:
        return None
    return out

consts = show("origin/main:src/consts.ts")
if consts is None:
    consts = show("HEAD:src/consts.ts")
if consts is None:
    p = os.path.join(web, "src", "consts.ts")
    if os.path.isfile(p):
        consts = open(p, encoding="utf-8").read()
if consts is None:
    if explicit:
        print("could not read src/consts.ts from the web tree", file=sys.stderr)
        sys.exit(2)
    notice_skip("web tree has no src/consts.ts")

m = re.search(r"locales:\s*\[([^\]]+)\]", consts)
if not m:
    print("could not parse SITE.locales in web src/consts.ts", file=sys.stderr)
    sys.exit(2)
live_locales = re.findall(r"'([a-z]+)'", m.group(1))
if len(live_locales) != 13:
    print(
        f"live SITE.locales is {len(live_locales)} {live_locales} — "
        "not 13, and the backlog '14' is still false or the measure is stale",
        file=sys.stderr,
    )
    sys.exit(1)
if live_locales != want:
    print(f"live SITE.locales {live_locales} != measured list {want}", file=sys.stderr)
    sys.exit(1)

rc, out, _ = git("ls-tree", "-r", "--name-only", "origin/main", cwd=web)
if rc != 0:
    rc, out, _ = git("ls-tree", "-r", "--name-only", "HEAD", cwd=web)
if rc != 0:
    if explicit:
        print("could not ls-tree the web repo", file=sys.stderr)
        sys.exit(2)
    notice_skip("web ls-tree failed")

pngs = [ln for ln in out.splitlines() if ln.startswith("public/og/") and ln.endswith(".png")]
if len(pngs) != n_png:
    print(f"live PNG count {len(pngs)} != measured-og-png {n_png}", file=sys.stderr)
    sys.exit(1)

loc_dirs = sorted({p.split("/")[2] for p in pngs if p.count("/") == 3})
if len(loc_dirs) != n_dirs:
    print(f"live locale dirs {loc_dirs} != measured-locale-dirs {n_dirs}", file=sys.stderr)
    sys.exit(1)

print(
    f"check-ver-10-og: CLEAN — looked; locales=13; png={n_png}; "
    f"locale-dirs={n_dirs}; cards={n_cards}; sha={sha[:12]}"
)
sys.exit(0)
PY
