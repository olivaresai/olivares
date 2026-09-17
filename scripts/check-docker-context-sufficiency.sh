#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Can every `dockers:` entry actually BUILD in the context GoReleaser gives it?
#
# ⛔ WHY THIS EXISTS, and it is not hypothetical. On 2026-09-01 the release for v26.8.0 became the
# first in this project's history to REACH the docker build, and died there:
#
#     Dockerfile.fips:57  COPY core/internal/webui/dist/index.html ...
#     ERROR: failed to compute cache key ... not available in the build context
#     context dir listed: DISCLAIMER.md, Dockerfile, LICENSE, <licence texts>, NOTICE, olivares-fips
#
# GoReleaser does not hand a Dockerfile the repository. It builds a temp dir holding the binaries of
# the referenced `ids:` plus `extra_files`, and NOTHING else. Dockerfile.fips and Dockerfile.stig
# were source builds (`COPY . .`, go.work, web/) and could therefore NEVER have built that way. Two
# of the four images were unbuildable from the day they were wired, and nothing said so, because
# NOT ONE check in this repository asserted anything about the `dockers:` entries. The defect did
# not slip past a gate; it walked through a gap where no gate was.
#
# WHAT IT CHECKS, and it is one question per entry: for the stages BuildKit will actually reach,
# is every path COPYed FROM THE CONTEXT satisfied by `extra_files` or by a binary of `ids:`?
#
# Reachability matters and is not a detail: BuildKit prunes the stages the target does not reach,
# which is exactly what lets a dual-mode Dockerfile serve both a plain `docker build .` (whole repo
# as context) and GoReleaser (synthetic context). So the census must be computed PER ENTRY, with
# that entry's own `--build-arg`s applied — the same file answers differently for different args.
#
# DERIVED, NEVER A TYPED LIST (REL-38 shape). Entries, dockerfiles, args and extra_files all come
# from .goreleaser.yaml; the stage graph comes from the Dockerfile. A new `dockers:` entry is in
# scope the day it is added, and a Dockerfile that grows a new COPY is caught by the next run.
#
# Three answers: 0 CLEAN · 1 an entry cannot build · 2 could not look (missing file, unparseable).
# "Could not look" is never spent as a pass: a config this cannot read is a refusal.
set -uo pipefail

# El entorno git ambiental gana al `cd`: con un `GIT_DIR` heredado —y git lo exporta a todo hook
# `pre-push`, que es donde este gate corre— `--show-toplevel` puede responder por OTRO repositorio,
# y entonces esto leeria el .goreleaser.yaml de otro arbol y emitiria su veredicto sobre el mio.
# Solo lee, asi que no puede corromper nada; pero un veredicto sobre el sujeto equivocado es
# exactamente lo que este fichero existe para impedir en otra capa.
# Fail-closed: no poder aislar es «no he podido mirar», nunca «no hacia falta».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "check-docker-context-sufficiency: 2 · no puedo cargar $_olivares_git_env (aislamiento git-env)" >&2
	exit 2
}
unset _olivares_git_env

ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || {
	echo "check-docker-context-sufficiency: 2 · not a git work tree, so there is no corpus to derive" >&2
	exit 2
}
cd "$ROOT" || exit 2

CFG="${OLIVARES_GORELEASER_CONFIG:-.goreleaser.yaml}"
[ -f "$CFG" ] || { echo "check-docker-context-sufficiency: 2 · no $CFG" >&2; exit 2; }

python3 - "$CFG" <<'PY'
import re, sys, os

try:
    import yaml
except Exception as e:                                   # pragma: no cover - environment
    print(f"check-docker-context-sufficiency: 2 · no puedo importar yaml ({e})", file=sys.stderr)
    sys.exit(2)

cfg_path = sys.argv[1]
try:
    cfg = yaml.safe_load(open(cfg_path, encoding='utf-8'))
except Exception as e:
    print(f"check-docker-context-sufficiency: 2 · {cfg_path} no parsea ({e})", file=sys.stderr)
    sys.exit(2)

binaries = {b.get('id'): (b.get('binary') or b.get('id')) for b in (cfg.get('builds') or [])}
entries  = cfg.get('dockers') or []
if not entries:
    print("check-docker-context-sufficiency: 2 · no hay entradas `dockers:`; eso no es un arbol limpio", file=sys.stderr)
    sys.exit(2)


def parse_dockerfile(path):
    """(stages en orden, aristas stage->dependencias, COPY de contexto por stage)."""
    stages, edges, ctx = [], {}, {}
    cur, anon = None, 0
    cont, buf = False, ''
    for raw in open(path, encoding='utf-8'):
        line = raw.rstrip('\n')
        if cont:
            buf += ' ' + line.strip()
        else:
            buf = line.strip()
        cont = buf.endswith('\\')
        if cont:
            buf = buf[:-1]
            continue
        s = buf
        if not s or s.startswith('#'):
            continue
        m = re.match(r'^FROM\s+(\S+)(?:\s+AS\s+(\S+))?', s, re.I)
        if m:
            base, name = m.group(1), m.group(2)
            if not name:
                name = f'<anon{anon}>'
                anon += 1
            cur = name
            stages.append(name)
            edges.setdefault(cur, set()).add(base)
            ctx.setdefault(cur, [])
            continue
        m = re.match(r'^COPY\s+(.*)$', s, re.I)
        if m and cur:
            parts = m.group(1).split()
            flags = [p for p in parts if p.startswith('--')]
            paths = [p for p in parts if not p.startswith('--')]
            frm = next((f.split('=', 1)[1] for f in flags if f.lower().startswith('--from=')), None)
            if frm:
                edges.setdefault(cur, set()).add(frm)
            elif len(paths) >= 2:
                ctx.setdefault(cur, []).extend(paths[:-1])   # el ultimo es el destino
    return stages, edges, ctx


def reachable(stages, edges, args):
    def subst(x):
        for k, v in args.items():
            x = re.sub(r'\$\{?' + re.escape(k) + r'\}?', v, x)
        return x
    names, target = set(stages), stages[-1]
    seen, stack, unresolved = set(), [target], []
    while stack:
        n = stack.pop()
        if n in seen:
            continue
        seen.add(n)
        for d in edges.get(n, ()):
            d2 = subst(d)
            if '$' in d2 or (d != d2 and d2 not in names):
                unresolved.append((n, d, d2))
            if d2 in names and d2 not in seen:
                stack.append(d2)
    return seen, unresolved


def covered(path, allowed_files, allowed_dirs):
    if path in allowed_files:
        return True
    p = path.rstrip('/')
    return any(p == d or p.startswith(d.rstrip('/') + '/') for d in allowed_dirs)


rc, checked = 0, 0
for e in entries:
    eid  = e.get('id') or '<sin id>'
    dfp  = e.get('dockerfile')
    if not dfp:
        print(f"check-docker-context-sufficiency: 2 · la entrada {eid} no nombra dockerfile", file=sys.stderr)
        sys.exit(2)
    if not os.path.isfile(dfp):
        print(f"check-docker-context-sufficiency: 2 · {eid} apunta a {dfp}, que no existe", file=sys.stderr)
        sys.exit(2)

    args = {}
    for f in (e.get('build_flag_templates') or []):
        m = re.match(r'^--build-arg=([A-Za-z_][A-Za-z0-9_]*)=(.*)$', f.strip('"'))
        if m:
            args[m.group(1)] = m.group(2)

    extra = e.get('extra_files') or []
    allowed_files = set(extra)
    allowed_dirs  = set(extra)                       # una entrada puede ser un directorio
    for bid in (e.get('ids') or []):
        allowed_files.add(binaries.get(bid, bid))    # el binario que GoReleaser deposita

    stages, edges, ctx = parse_dockerfile(dfp)
    if not stages:
        print(f"check-docker-context-sufficiency: 2 · {dfp} no tiene ningun FROM", file=sys.stderr)
        sys.exit(2)
    seen, unresolved = reachable(stages, edges, args)

    for st, dep, dep2 in unresolved:
        print(f"check-docker-context-sufficiency: UNRESOLVED — {eid} ({dfp})")
        print(f"    stage `{st}` depends on `{dep}` which resolves to `{dep2}`, and no stage has that")
        print(f"    name. BuildKit would look for an IMAGE by that name. build-args seen: {args or '{}'}")
        rc = 1

    faltan = []
    for st in sorted(seen):
        for p in ctx.get(st, []):
            if not covered(p, allowed_files, allowed_dirs):
                faltan.append((st, p))
    checked += 1
    if faltan:
        rc = 1
        print(f"check-docker-context-sufficiency: CANNOT BUILD — {eid} ({dfp})")
        print(f"    GoReleaser's context holds only: {', '.join(sorted(allowed_files)) or '(nothing)'}")
        print(f"    build-args applied: {args or '{}'}")
        for st, p in faltan:
            print(f"    stage `{st}` COPYs `{p}` from the context, and nothing puts it there.")
        print("    repair: either add the path to `extra_files`, or keep the stage that needs it OUT")
        print("            of the graph for this entry (a build-arg selecting a prebuilt-binary")
        print("            stage, as Dockerfile.fips does). A source-build stage cannot be fixed")
        print("            with extra_files: it needs the whole repository.")

if rc == 0:
    print(f"check-docker-context-sufficiency: CLEAN — {checked} dockers entr{'y' if checked==1 else 'ies'}, every context COPY is satisfied.")
sys.exit(rc)
PY
