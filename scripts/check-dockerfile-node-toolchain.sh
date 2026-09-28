#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-dockerfile-node-toolchain.sh — a Dockerfile stage on Node.js 25 or later must install
# corepack (or pnpm) before it runs it, and every install of either names an exact version.
#
# WHY. Node.js stopped bundling corepack in version 25. The web stages of Dockerfile,
# Dockerfile.fips and Dockerfile.stig moved to `FROM node:26-bookworm-slim` with a dependency
# bump (0395789361, 2026-06-23) and kept `RUN corepack enable` on the next line. A build of the
# same stage measured the result on 2026-09-25: "/bin/sh: 1: corepack: not found", exit 127.
# No workflow built these three files, so nothing failed here.
#
# THE RULE, per stage:
#   - a stage whose base is node 25 or later, or whose base cannot be resolved, may run
#     `corepack` only after installing it in the same stage (or in the stage it is FROM);
#   - such a stage may run `pnpm` (or `yarn`) only after an npm install of it, or after
#     `corepack enable` (with no name, or naming it) with a package.json copied into the stage
#     whose packageManager is pnpm@<exact version> (yarn@…); corepack takes the version from that
#     field, so the field is the pin. `corepack pnpm …` without a version reads the same field;
#   - in EVERY stage, `npm install -g corepack|pnpm|yarn`, `npx corepack|pnpm|yarn`,
#     `corepack prepare|install|use pnpm@…` and `corepack pnpm@… …` name an exact version (x.y.z,
#     ARG/ENV defaults resolved); `corepack up` never does; an npm-installed pnpm equals the
#     copied packageManager;
#   - `corepack enable` takes package manager names only (npm, pnpm, yarn): corepack refuses any
#     other name, such as the binary `pnpx`, so the gate refuses it too, in every stage;
#   - an `ONBUILD RUN` trigger runs in a later build FROM the stage, so it is judged with the
#     stage's state at its end.
# A stage on node 24 or earlier still has its bundled corepack and is not judged for installs.
#
# WHAT IT READS. Every Dockerfile* at the repository root, as instructions: a leading byte order
# mark is dropped, comment lines are dropped (also between continuation lines), escape-newline is
# joined, heredoc bodies belong to their RUN or ONBUILD RUN. Shell text is split into simple
# commands on operators, newlines, `(`, `)`, `$(` and backticks; `sh -c` / `bash -c` strings and
# command substitutions are read recursively. A `#` starts a comment only at the start of a
# word, as in the shell. Limits, declared: build-arg overrides are not known (defaults are
# judged); installers other than npm, npx and corepack (for example a curl | sh script) are not
# recognized, so a pnpm or yarn they provide is reported as not installed; CMD and ENTRYPOINT
# are not judged; an ONBUILD trigger is judged once, at the end of its stage, not again in each
# build that uses the image.
#
# Exit 0 = CLEAN. Exit 1 = VIOLATION, each named with file and line. Exit 2 = COULD NOT LOOK
# (no root, no Dockerfile at the root, an unreadable file, a file with no FROM, an instruction
# made only of an escape character, an unterminated heredoc, an unbalanced quote, or any other
# error of this program). Never a clean verdict for something it did not read.
#
# USAGE: scripts/check-dockerfile-node-toolchain.sh [ROOT]   (default: this script's repository)
set -euo pipefail
export LC_ALL=C

root="${1:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
if ! command -v python3 >/dev/null 2>&1; then
	printf 'check-dockerfile-node-toolchain: COULD NOT LOOK — python3 is not on PATH\n' >&2
	exit 2
fi

exec python3 - "$root" <<'PY'
import json
import os
import re
import shlex
import sys

PROG = "check-dockerfile-node-toolchain"
ROOT = sys.argv[1]
FIRST_WITHOUT_COREPACK = 25
EXACT = r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?"
EXACT_RE = re.compile(EXACT)
PMS = ("pnpm", "yarn")
SHIMS = {"pnpm": "pnpm", "pnpx": "pnpm", "yarn": "yarn", "yarnpkg": "yarn"}
INSTALLABLE = ("corepack",) + PMS
ENABLE_NAMES = ("npm",) + PMS  # the names corepack's enable accepts; npm enables no judged tool
NPM_INSTALL = {"install", "i", "in", "ins", "inst", "insta", "instal", "isnt", "isnta", "isntal", "isntall", "add"}
NPM_GLOBAL = {"-g", "--global", "--global=true", "--location=global"}
SHELLS = {"sh", "bash", "dash", "ash", "zsh"}
WRAPPERS = {"env", "exec", "command", "time", "nice", "nohup", "sudo"}
LEAD = {"{", "}", "!", "if", "then", "else", "elif", "while", "until", "do", "time"}
ASSIGN = re.compile(r"[A-Za-z_][A-Za-z0-9_]*=")
VAR = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)")


class CannotLook(Exception):
    pass


def instructions(name, text):
    """(line, KEYWORD, arguments, heredoc bodies) for each instruction of a Dockerfile."""
    lines = text.replace("\r\n", "\n").split("\n")
    escape = "\\"
    for raw in lines:
        m = re.match(r"#\s*([A-Za-z]+)\s*=\s*(\S+)\s*$", raw)
        if not m:
            break
        if m.group(1).lower() == "escape":
            escape = m.group(2)
    out, i, n = [], 0, len(lines)
    while i < n:
        s = lines[i].strip()
        if not s or s.startswith("#"):
            i += 1
            continue
        start, parts = i + 1, []
        while True:
            cur = lines[i].rstrip()
            i += 1
            if cur.endswith(escape):
                parts.append(cur[: -len(escape)])
                while i < n and (not lines[i].strip() or lines[i].lstrip().startswith("#")):
                    i += 1
                if i < n:
                    continue
            else:
                parts.append(cur)
            break
        logical = "".join(parts)
        if not logical.strip():
            raise CannotLook(f"{name}:{start}: an instruction made only of an escape character")
        m = re.match(r"\s*(\S+)\s*(.*)$", logical, re.S)
        kw, rest = m.group(1).upper(), m.group(2)
        bodies = []
        inner = rest.split(None, 1)[0].upper() if kw == "ONBUILD" and rest.split() else kw
        if inner in ("RUN", "COPY", "ADD"):
            for h in re.finditer(r"(?<!<)<<(?!<)(-?)([\"']?)([A-Za-z_][A-Za-z0-9_]*)\2", rest):
                strip_tabs, word, body = h.group(1) == "-", h.group(3), []
                while True:
                    if i >= n:
                        raise CannotLook(f"{name}:{start}: heredoc <<{word} is never terminated")
                    line = lines[i]
                    i += 1
                    if (line.lstrip("\t") if strip_tabs else line) == word:
                        break
                    body.append(line)
                bodies.append("\n".join(body))
        out.append((start, kw, rest, bodies))
    return out


def commands(text):
    """Simple commands (word lists) of a shell text."""
    out, cur = [], []
    st = {"word": None, "redirect": False}

    def flush_word():
        if st["word"] is not None:
            if not st["redirect"]:
                cur.append(st["word"])
            st["redirect"] = False
            st["word"] = None

    def flush_cmd():
        flush_word()
        if cur:
            out.append(list(cur))
        cur.clear()

    i, n, q = 0, len(text), None
    while i < n:
        c = text[i]
        if q == "'":
            if c == "'":
                q = None
            else:
                st["word"] += c
            i += 1
            continue
        if q == '"':
            if c == "\\" and i + 1 < n and text[i + 1] in '"\\$`':
                st["word"] += text[i + 1]
                i += 2
                continue
            if c == '"':
                q = None
            else:
                st["word"] += c
            i += 1
            continue
        if c == "\\":
            if text[i + 1 : i + 2] != "\n":
                st["word"] = (st["word"] or "") + text[i + 1 : i + 2]
            i += 2
            continue
        if c in "'\"":
            q, st["word"] = c, (st["word"] or "")
            i += 1
            continue
        if c == "#" and st["word"] is None:
            j = text.find("\n", i)
            i = n if j < 0 else j
            continue
        if c in " \t":
            flush_word()
            i += 1
            continue
        if c in "<>":
            if st["word"] is not None and st["word"].isdigit():
                st["word"] = None
            else:
                flush_word()
            while i < n and text[i] in "<>&|":
                i += 1
            st["redirect"] = True
            continue
        if c in ";&|()\n`" or (c == "$" and text[i + 1 : i + 2] == "("):
            flush_cmd()
            i += 2 if c == "$" else 1
            continue
        st["word"] = (st["word"] or "") + c
        i += 1
    if q:
        raise CannotLook("an unbalanced quote in shell text")
    flush_cmd()
    return out


def split_spec(spec):
    at = spec.find("@", 1)
    return (spec, None) if at < 0 else (spec[:at], spec[at + 1 :])


def positional(args):
    out, skip = [], False
    for a in args:
        if skip:
            skip = False
        elif a == "--install-directory":
            skip = True
        elif not a.startswith("-"):
            out.append(a)
    return out


def events(words, depth):
    """(kind, tool, detail) for one simple command: install, prepare, use, enable."""
    ev = []
    for w in words:
        for inner in re.findall(r"\$\((.*)\)", w) + re.findall(r"`([^`]*)`", w):
            ev += text_events(inner, depth + 1)
    k = 0
    while k < len(words):
        if words[k] in LEAD or ASSIGN.match(words[k]):
            k += 1
        elif os.path.basename(words[k]) in WRAPPERS:
            k += 1
            while k < len(words) and words[k].startswith("-"):
                k += 1
        else:
            break
    words = words[k:]
    if not words:
        return ev
    cmd, args = os.path.basename(words[0]), words[1:]
    if cmd in SHELLS:
        for j, a in enumerate(args):
            if re.fullmatch(r"-[A-Za-z]*c[A-Za-z]*", a) and j + 1 < len(args):
                ev += text_events(args[j + 1], depth + 1)
                break
    elif cmd == "npm":
        pos = [a for a in args if not a.startswith("-")]
        if pos and pos[0] in NPM_INSTALL and any(a in NPM_GLOBAL for a in args):
            for spec in pos[1:]:
                name, ver = split_spec(spec)
                if name in INSTALLABLE:
                    ev.append(("install", name, ver))
    elif cmd == "npx":
        pos = [a for a in args if not a.startswith("-")]
        if pos:
            name, ver = split_spec(pos[0])
            if name in INSTALLABLE:
                ev.append(("install", name, ver))
                ev.append(("use", name, None))
    elif cmd == "corepack":
        ev.append(("use", "corepack", None))
        pos = positional(args)
        sub = pos[0] if pos else ""
        name, ver = split_spec(sub)
        if sub == "enable":
            names = pos[1:]
            ev += [("bad-enable", x, None) for x in names if x not in ENABLE_NAMES]
            for tool in (PMS if not names else sorted({x for x in names if x in PMS})):
                ev.append(("enable", tool, None))
        elif sub in ("prepare", "install", "use"):
            for spec in pos[1:]:
                n, v = split_spec(spec)
                if n in PMS:
                    ev.append(("prepare", n, v))
        elif sub == "up":
            ev.append(("up", None, None))
        elif name in SHIMS:
            # `corepack pnpm@<spec> …` runs that version; `corepack pnpm …` runs packageManager's.
            ev.append(("prepare", SHIMS[name], ver) if ver is not None else ("through-corepack", SHIMS[name], None))
    elif cmd in SHIMS:
        ev.append(("use", SHIMS[cmd], None))
    return ev


def text_events(text, depth):
    if depth > 8:
        raise CannotLook("shell text nested more than 8 levels")
    ev = []
    for words in commands(text):
        ev += events(words, depth)
    return ev


def expand(s, env):
    def sub(m):
        name = m.group(1) or m.group(3)
        if env.get(name):
            return env[name]
        if m.group(2) is not None:
            return m.group(2)
        raise KeyError(name)

    try:
        return VAR.sub(sub, s)
    except KeyError:
        return None


def words_of(rest, where):
    s = rest.strip()
    if s.startswith("["):
        try:
            w = json.loads(s)
            if isinstance(w, list) and all(isinstance(x, str) for x in w):
                return w, True
        except ValueError:
            pass
    try:
        return shlex.split(s, comments=False, posix=True), False
    except ValueError as e:
        raise CannotLook(f"{where}: {e}")


def key_values(rest, where, legacy_env=False):
    w, _ = words_of(rest, where)
    if legacy_env and w and "=" not in w[0]:
        return [(w[0], " ".join(w[1:]))]
    return [tuple(x.split("=", 1)) if "=" in x else (x, None) for x in w]


def image_facts(image):
    """(is_node, major or None) of an image reference."""
    ref = image.split("@", 1)[0]
    slash, colon = ref.rfind("/"), ref.rfind(":")
    repo, tag = (ref[:colon], ref[colon + 1 :]) if colon > slash else (ref, "latest")
    if repo.rsplit("/", 1)[-1].lower() != "node":
        return False, None
    m = re.match(r"(\d+)(?:[.-]|$)", tag)
    return True, (int(m.group(1)) if m else None)


def package_manager(path):
    try:
        with open(path, encoding="utf-8") as f:
            data = json.load(f)
    except FileNotFoundError:
        return None
    except (OSError, ValueError) as e:
        raise CannotLook(f"{path}: {e}")
    pm = data.get("packageManager") if isinstance(data, dict) else None
    return pm if isinstance(pm, str) else ""


class Stage:
    def __init__(self, name, line, image, parent=None):
        self.name, self.line, self.image = name, line, image
        if parent is not None:
            self.judged, self.base = parent.judged, parent.base
            self.corepack, self.npm_tools, self.cp_tools = parent.corepack, dict(parent.npm_tools), set(parent.cp_tools)
            self.env, self.pkgs = dict(parent.env), list(parent.pkgs)
        else:
            if image is None:
                self.judged, self.base = True, "base not resolved, judged as node 25 or later"
            else:
                node, major = image_facts(image)
                self.judged = node and (major is None or major >= FIRST_WITHOUT_COREPACK)
                self.base = f"node {major}" if major is not None else ("node, version not in the tag" if node else "not node")
            self.corepack, self.npm_tools, self.cp_tools = False, {}, set()
            self.env, self.pkgs = {}, []
        self.args, self.reported, self.uses, self.onbuild = {}, set(), 0, []

    def label(self):
        return f'stage "{self.name or "unnamed"}" (FROM {self.image or "?"} at line {self.line}: {self.base})'


findings = []


def judge(fname):
    path = os.path.join(ROOT, fname)
    try:
        with open(path, encoding="utf-8-sig") as f:
            text = f.read()
    except (OSError, UnicodeDecodeError) as e:
        raise CannotLook(f"{fname}: {e}")
    gargs, stages, st, all_stages = {}, {}, None, []
    for line, kw, rest, bodies in instructions(fname, text):
        where = f"{fname}:{line}"
        if kw == "ARG":
            for k, v in key_values(rest, where):
                if st is None:
                    gargs[k] = v
                else:
                    st.args[k] = v if v is not None else gargs.get(k)
            continue
        if kw == "FROM":
            close(fname, st)
            w = [x for x in words_of(rest, where)[0] if not x.startswith("--")]
            if not w:
                raise CannotLook(f"{where}: FROM names no image")
            image = expand(w[0], gargs)
            name = w[2].lower() if len(w) >= 3 and w[1].lower() == "as" else None
            parent = stages.get(image.lower()) if image else None
            st = Stage(name, line, image, parent)
            all_stages.append(st)
            if name:
                stages[name] = st
            continue
        if st is None:
            continue
        env = dict(st.env)
        env.update({k: v for k, v in st.args.items() if v is not None})
        if kw == "ENV":
            for k, v in key_values(rest, where, legacy_env=True):
                st.env[k] = expand(v or "", env) or ""
        elif kw in ("COPY", "ADD"):
            w, _ = words_of(rest, where)
            if any(x.startswith("--from") for x in w):
                continue
            for src in [x for x in w if not x.startswith("--")][:-1]:
                p = os.path.normpath(os.path.join(ROOT, src))
                if src.endswith("package.json"):
                    st.pkgs.append(p)
                elif os.path.isfile(os.path.join(p, "package.json")):
                    st.pkgs.append(os.path.join(p, "package.json"))
        elif kw == "RUN":
            for kind, tool, detail in run_events(where, rest, bodies):
                step(fname, line, st, env, kind, tool, detail)
        elif kw == "ONBUILD":
            m = re.match(r"\s*(\S+)\s*(.*)$", rest, re.S)
            if m and m.group(1).upper() == "RUN":
                st.onbuild.append((line, m.group(2), bodies, env))
    close(fname, st)
    if not all_stages:
        raise CannotLook(f"{fname}: no FROM instruction, so there is no stage to judge")
    return all_stages


def run_events(where, rest, bodies):
    s = rest.strip()
    w, exec_form = words_of(s, where) if s.startswith("[") else (None, False)
    if exec_form:
        return events(w, 0)
    body = re.sub(r"^(?:--[A-Za-z-]+(?:=\S*)?\s+)*", "", s)
    try:
        return text_events(body + "".join("\n" + b for b in bodies), 0)
    except CannotLook as e:
        raise CannotLook(f"{where}: {e}")


def close(fname, st):
    # ONBUILD RUN triggers run in a later build FROM this stage, on the state the stage ends with.
    if st is None:
        return
    for line, rest, bodies, env in st.onbuild:
        for kind, tool, detail in run_events(f"{fname}:{line}", rest, bodies):
            step(fname, line, st, env, kind, tool, detail,
                 " (an ONBUILD trigger: it runs in a build FROM this stage, after the stage's own instructions)")
    st.onbuild = []


def report(fname, line, st, key, text):
    if key in st.reported:
        return
    st.reported.add(key)
    findings.append(f"{fname}:{line}: {text}")


def copied_pms(st):
    return [pm for pm in (package_manager(p) for p in st.pkgs) if pm]


def step(fname, line, st, env, kind, tool, ver, note=""):
    if kind in ("install", "prepare"):
        v = expand(ver, env) if ver else None
        if v is None or not EXACT_RE.fullmatch(v):
            report(fname, line, st, ("pin", tool, line),
                   f"installs {tool} without an exact version ({tool}@{ver or ''}); "
                   f"name one, for example {tool}@x.y.z — an unpinned tool is a different build every day" + note)
        if kind == "install" and tool == "corepack":
            st.corepack = True
        if kind == "install" and tool in PMS:
            st.npm_tools[tool] = v or "?"
        return
    if kind == "up":
        tool = next((pm.split("@", 1)[0] for pm in copied_pms(st) if pm.split("@", 1)[0] in PMS), "pnpm")
        report(fname, line, st, ("pin", "up", line),
               f"installs {tool} through corepack up, which moves packageManager to whatever version corepack "
               f"picks; name an exact one (corepack use {tool}@x.y.z)" + note)
        return
    if kind == "bad-enable":
        report(fname, line, st, ("enable-name", tool, line),
               f"corepack enable {tool} names no package manager; corepack accepts npm, pnpm and yarn and refuses "
               f"this name, so the build stops here (write corepack enable pnpm)" + note)
        return
    if kind == "enable":
        if st.corepack or not st.judged:
            st.cp_tools.add(tool)
        return
    if not st.judged:
        return
    st.uses += 1
    if tool == "corepack":
        if not st.corepack:
            report(fname, line, st, "corepack",
                   f"{st.label()} runs corepack, which Node.js does not bundle from version 25; "
                   f"install a pinned corepack first in this stage (npm install -g corepack@x.y.z)" + note)
            st.corepack = True
        return
    pms = [pm for pm in copied_pms(st) if pm.startswith(tool + "@")]
    exact = [m.group(1).split("+", 1)[0] for m in (re.fullmatch(re.escape(tool) + "@(" + EXACT + ")", pm) for pm in pms) if m]
    if kind == "use" and tool in st.npm_tools:
        if pms and (len(exact) != len(pms) or any(x != st.npm_tools[tool] for x in exact)):
            report(fname, line, st, ("pm", tool),
                   f"{st.label()} runs {tool} {st.npm_tools[tool]} from npm, but the copied package.json "
                   f"declares packageManager {', '.join(pms)}; keep one version" + note)
        return
    if kind == "through-corepack" or tool in st.cp_tools:
        if not exact or len(exact) != len(pms):
            report(fname, line, st, ("pm", tool),
                   f"{st.label()} runs {tool} through corepack, which takes its version from packageManager, "
                   f"and no package.json copied into this stage declares packageManager {tool}@<exact version>"
                   + (f" (found {', '.join(pms)})" if pms else "") + note)
        return
    report(fname, line, st, tool,
           f"{st.label()} runs {tool}, which nothing installed in this stage; install a pinned corepack and "
           f"run corepack enable {tool}, or npm install -g {tool}@<the packageManager version>" + note)


def main():
    if not os.path.isdir(ROOT):
        raise CannotLook(f"{ROOT} is not a directory")
    try:
        names = sorted(n for n in os.listdir(ROOT) if n.startswith("Dockerfile") and os.path.isfile(os.path.join(ROOT, n)))
    except OSError as e:
        raise CannotLook(f"cannot list {ROOT}: {e}")
    if not names:
        raise CannotLook(f"no Dockerfile* at {ROOT}; the root is wrong or the enumeration failed")
    stages = []
    for n in names:
        stages += judge(n)
    judged = [s for s in stages if s.judged]
    using = [s for s in judged if s.uses]
    if findings:
        for f in findings:
            print(f"{PROG}: VIOLATION — {f}", file=sys.stderr)
        print(f"{PROG}: {len(findings)} violation(s) in {len(names)} Dockerfile(s).", file=sys.stderr)
        return 1
    print(f"{PROG}: CLEAN — {len(names)} Dockerfile(s), {len(stages)} stage(s); {len(judged)} on node 25 or later "
          f"or unresolved, {len(using)} of them run corepack, pnpm or yarn after a pinned install.")
    return 0


try:
    sys.exit(main())
except CannotLook as e:
    print(f"{PROG}: COULD NOT LOOK — {e}", file=sys.stderr)
    print(f"{PROG}: this is not a clean verdict.", file=sys.stderr)
    sys.exit(2)
except Exception as e:  # a defect of this program is not a finding about the Dockerfiles
    print(f"{PROG}: COULD NOT LOOK — {type(e).__name__}: {e}", file=sys.stderr)
    print(f"{PROG}: this is not a clean verdict.", file=sys.stderr)
    sys.exit(2)
PY
