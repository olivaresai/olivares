#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-md-links.py — every relative markdown link, image and HTML src in a tree must
# resolve to a file or directory that EXISTS **inside** that same tree.
#
# Born as the export leak gate's missing leg: the curated public export drops internal
# paths on purpose, and a shipped document that links to a dropped path renders as a 404
# exactly where the public repo tries to prove a claim. Resolving each reference against
# the tree needs no list of internal names: whatever the curation dropped, or whatever
# was simply mistyped, fails the same way a visitor would see it fail.
#
# Hardened after a second-opinion audit measured four green escapes in the first
# version: uppercase/spaced/unquoted HTML attributes, srcset comma handling, targets
# that resolve OUTSIDE the scanned root via ../, and fence handling that only knew
# three-backtick fences. Containment is now enforced with realpath/commonpath (symlink
# traversal included), HTML attributes are matched case-insensitively with optional
# whitespace and unquoted values, srcset skips data: URLs and tokenizes per candidate,
# and fences track their marker character and length per CommonMark's close rule.
#
# Known, stated limits (line-oriented scanner, no full CommonMark parser is available
# offline): a link whose target sits on a different line from its brackets, and links
# inside raw HTML comments, are not seen. Neither form occurs in this tree today; if one
# appears, the self-test below is where its fixture belongs.
#
# Scope rules:
#   * Only .md/.mdx files are scanned; --skip PREFIX excludes subtrees whose links live
#     in another address space (docs-site/src/content is ROUTE-space and gated by the
#     site's own build-time link check).
#   * External schemes (http/https/mailto/data/tel), pure #anchors and template
#     expressions are out of scope; anchor VALIDITY is the docs-site gate's job.
#   * A leading-/ target resolves against the scanned root (GitHub repo-relative form).
#
# Output: one "file:line: target [reason]" per unresolved reference on stdout; exit 0
# (the caller judges emptiness — the export gate's scan_in contract). --self-test builds
# red and green fixture trees and exits non-zero if any fixture misbehaves.
import os
import re
import signal
import subprocess
import sys
import tempfile

# stdout may feed a head/tail; SIGPIPE must terminate quietly, never traceback (this
# repo has shipped that lesson twice)
signal.signal(signal.SIGPIPE, signal.SIG_DFL)

MD_EXT = (".md", ".mdx")
INLINE = re.compile(r"!?\[[^\]]*\]\(\s*<?([^)\s>]+)>?(?:\s+\"[^\"]*\")?\s*\)")
REFDEF = re.compile(r"^\s*\[[^\]]+\]:\s+<?(\S+?)>?\s*(?:\"[^\"]*\")?\s*$")
# HTML attributes markdown renders — case-insensitive names, optional whitespace around
# '=', quoted or unquoted values (unquoted ends at whitespace or '>').
HTML_SRC = re.compile(r"\b(?:src|href)\s*=\s*(?:\"([^\"]*)\"|'([^']*)'|([^\s\"'>]+))", re.I)
HTML_SRCSET = re.compile(r"\bsrcset\s*=\s*(?:\"([^\"]*)\"|'([^']*)'|([^\s\"'>]+))", re.I)
FENCE = re.compile(r"^\s{0,3}(`{3,}|~{3,})")

SKIP_PREFIXES = ("http://", "https://", "mailto:", "data:", "tel:", "ftp:", "{", "$")


def srcset_candidates(value):
    """Per-candidate URLs of a srcset value. data: URLs may carry commas, so candidates
    are split on commas (whitespace-tolerant) — the practical form of the HTML candidate
    grammar for the path URLs this tree uses; a whole-value data: URL is skipped before
    the split, and a data: candidate mixed into a multi-candidate srcset is a stated
    limit with no occurrence here."""
    out = []
    for cand in re.split(r"\s*,\s*", value.strip()):
        cand = cand.strip()
        if not cand:
            continue
        url = cand.split()[0]
        out.append(url)
    return out


# ⛔ UN ATRIBUTO SÓLO EXISTE DENTRO DE UNA ETIQUETA, y hasta 2026-08-26 esto no lo exigía.
#    `HTML_SRC` lleva `\b(?:src|href)\s*=`, y `\b` casa tras una barra: la prosa
#    «Tus cinco dan `web/src=0`» de un buzón producía un hallazgo con destino `0`. Medido en
#    el árbol: ~21 falsos positivos de esta forma, todos en `sessions/`, y ninguno era un
#    enlace. Restringirlo al interior de `<...>` NO pierde las formas que la auditoría del
#    2026-08-04 midió como escapes (mayúsculas, espacios, valores sin comillas): esas siguen
#    siendo atributos y siguen viviendo dentro de la etiqueta.
TAG = re.compile(r"<[^<>]*>")


# ⛔ UN ENLACE DENTRO DE COMILLAS INVERTIDAS NO ES UN ENLACE. El guion honraba las CERCAS de
#    bloque y no los tramos EN LÍNEA, así que la prosa que MUESTRA la forma de un enlace se
#    contaba como enlace. Los dos casos medidos en el árbol el 2026-08-26 eran exactamente eso:
#      ESTADO-PROYECTO.md:3460   «las rutas relativas de imágenes (`![](../../../../assets/…`)»
#      docs/launch/reddit-pack.md:11  «The `![...](./assets/*.png)` references below are TODO»
#    Ambos documentan la forma; ninguno enlaza. Se retiran los tramos en línea ANTES de buscar,
#    con la regla de cierre de CommonMark (una serie de N backticks cierra con otra de N).
CODESPAN = re.compile(r"(`+)(?:(?!\1).)*?\1", re.S)


def strip_codespans(line):
    return CODESPAN.sub(" ", line)


def targets_in(line):
    line = strip_codespans(line)
    out = []
    for m in INLINE.finditer(line):
        out.append(m.group(1))
    m = REFDEF.match(line)
    if m:
        out.append(m.group(1))
    for tag in TAG.finditer(line):
        span = tag.group(0)
        for m in HTML_SRC.finditer(span):
            out.append(next(g for g in m.groups() if g is not None))
        for m in HTML_SRCSET.finditer(span):
            value = next(g for g in m.groups() if g is not None)
            if value.startswith("data:"):
                continue
            out.extend(srcset_candidates(value))
    return out


# ⛔ LA RAÍZ DEL GATE NO PUEDE SER UN `os.walk`, y es una medida: esta caja tiene **26 363**
#    markdown bajo `.claude/worktrees/` frente a **4 417** trackeados en TODO el repositorio.
#    Recorriéndolo entero, un fast-lint haría seis veces el trabajo del repositorio sobre
#    ficheros que no son del repositorio — y su duración y su verde dependerían de qué
#    worktrees efímeros haya en la caja ese día. Un control cuyo resultado depende del entorno
#    no es un control. Excluir `.claude` a mano tampoco vale: ese directorio sólo está ignorado
#    por `.git/info/exclude`, que es LOCAL y no viaja en el clon.
#
#    Por eso hay DOS modos y ninguno sobra: `--tracked` para un repositorio (determinista,
#    `git ls-files`) y el `os.walk` para un ÁRBOL SUELTO — que es el caso para el que este
#    guion nació: el export curado no es un repositorio git.
def tracked_md(root):
    out = subprocess.run(["git", "-C", root, "ls-files", "-z", "*.md", "*.mdx"],
                         capture_output=True, text=True)
    if out.returncode != 0:
        raise SystemExit(f"check-md-links: NO HE PODIDO MIRAR: git ls-files fallo en {root}")
    return [r for r in out.stdout.split("\0") if r]


def _candidates(root, files):
    """(dirpath, path, rel) de cada markdown a escanear. `files` = lista relativa ya elegida."""
    if files is not None:
        for rel in files:
            path = os.path.join(root, rel)
            yield os.path.dirname(path), path, rel
        return
    for dirpath, dirs, names in os.walk(root):
        dirs[:] = [d for d in dirs if d not in (".git", "node_modules")]
        for name in names:
            if not name.endswith(MD_EXT):
                continue
            path = os.path.join(dirpath, name)
            yield dirpath, path, os.path.relpath(path, root).replace(os.sep, "/")


def unresolved(root, skips, files=None, routes=False):
    root_real = os.path.realpath(root)
    findings = []
    for dirpath, path, rel in _candidates(root, files):
        if any(rel == s or rel.startswith(s.rstrip("/") + "/") for s in skips):
            continue
        try:
            with open(path, encoding="utf-8", errors="replace") as fh:
                lines = fh.read().splitlines()
        except OSError as exc:
            findings.append(f"{rel}:0: UNREADABLE ({exc})")
            continue
        fence = None  # (char, length) of the open fence
        for i, line in enumerate(lines, 1):
            fm = FENCE.match(line)
            if fm:
                marker = fm.group(1)
                if fence is None:
                    fence = (marker[0], len(marker))
                    continue
                if marker[0] == fence[0] and len(marker) >= fence[1]:
                    fence = None
                    continue
            if fence is not None:
                continue
            for target in targets_in(line):
                if target.startswith(SKIP_PREFIXES) or target.startswith("#"):
                    continue
                bare = target.split("#", 1)[0].split("?", 1)[0]
                if not bare:
                    continue
                if bare.startswith("/"):
                    # `routes`: en un sitio con enrutado propio (Starlight, Astro) un
                    # destino absoluto es una RUTA que resuelve en el build, no un
                    # fichero. Medido: 7 670 de los 8 854 hallazgos del árbol son de esa
                    # forma. Sin este modo el gate es inservible sobre el 87 % de lo que
                    # ve; con él, los relativos del MISMO fichero se siguen exigiendo.
                    if routes:
                        continue
                    cand = os.path.join(root, bare.lstrip("/"))
                else:
                    cand = os.path.join(dirpath, bare)
                if not os.path.exists(cand):
                    findings.append(f"{rel}:{i}: {target}")
                    continue
                # containment: the resolved target (symlinks included) must live
                # INSIDE the scanned tree — ../RELEASE-VERSION exists in the hub
                # but not in the export a visitor holds.
                cand_real = os.path.realpath(cand)
                if os.path.commonpath([root_real, cand_real]) != root_real:
                    findings.append(f"{rel}:{i}: {target} [escapes the scanned tree]")
    return findings


def self_test():
    failures = []
    with tempfile.TemporaryDirectory() as td:
        td_real = os.path.realpath(td)
        outside = tempfile.mkdtemp()
        os.makedirs(os.path.join(td, "docs"))
        os.makedirs(os.path.join(td, "assets"))
        open(os.path.join(td, "assets", "ok.png"), "w").close()
        open(os.path.join(td, "docs", "real.md"), "w").close()
        open(os.path.join(outside, "secret.md"), "w").close()
        os.symlink(os.path.join(outside, "secret.md"), os.path.join(td, "docs", "sneaky.md"))
        with open(os.path.join(td, "README.md"), "w", encoding="utf-8") as fh:
            fh.write(
                "[good](docs/real.md) [gooddir](docs/) [anchor](#x) [ext](https://x.y)\n"
                "![img](assets/ok.png)\n"
                '<img src="assets/ok.png"> <source srcset="assets/ok.png 2x, assets/ok.png">\n'
                "[rooted](/docs/real.md)\n"
                '<img src="data:image/png;base64,AAAA,BBBB">\n'
                "```\n[fenced](nowhere/at-all.md)\n```\n"
                "~~~\n[tilde-fenced](nowhere/either.md)\n~~~\n"
                "````\n```\n[inner-not-a-close](still/fenced.md)\n````\n"
                "[dead](gone/dead-script.sh)\n"
                "[deadimg]: gone/definition.png\n"
                '<IMG SRC=gone/upper.png>\n'
                "<img src = 'gone/spaced.png'>\n"
                "<source srcset=gone/one.png,gone/two.png>\n"
                "[deadrooted](/gone/rooted.md)\n"
                "[escape](../%s)\n"
                "[sneaky-link](docs/sneaky.md)\n" % os.path.basename(outside)
            )
        got = unresolved(td, [])
        want = ["gone/dead-script.sh", "gone/definition.png", "gone/upper.png",
                "gone/spaced.png", "gone/one.png", "gone/two.png", "/gone/rooted.md",
                "escapes the scanned tree", "sneaky.md"]
        for t in want:
            if not any(t in f for f in got):
                failures.append(f"red fixture NOT caught: {t}")
        # 7 dead refs + ../escape + symlink escape = 9 findings exactly (nothing green flagged)
        if len(got) != 9:
            failures.append(f"expected exactly 9 findings, got {len(got)}: {got}")
        os.makedirs(os.path.join(td, "routes"))
        with open(os.path.join(td, "routes", "page.md"), "w") as fh:
            fh.write("[route](/reference/modules/overview/)\n")
        if unresolved(td, ["routes"]) != got:
            failures.append("--skip did not exclude the skipped subtree")

        # ── CASO · el CÓDIGO DE SALIDA tiene que poder ser 1 ──────────────────────────────
        #    Sin esto lo demás es decoración: un gate que no puede enrojecer certifica.
        if main([td]) != 1:
            failures.append("main() devolvio 0 con hallazgos delante")
        limpio = tempfile.mkdtemp()
        with open(os.path.join(limpio, "ok.md"), "w", encoding="utf-8") as fh:
            fh.write("[ext](https://x.y) y nada mas\n")
        if main([limpio]) != 0:
            failures.append("main() devolvio no-cero sobre un arbol limpio")

        # ── CASO · un ATRIBUTO fuera de una etiqueta NO es un enlace ──────────────────────
        #    `web/src=0` en prosa producia un hallazgo con destino `0`: ~21 en el arbol real.
        with open(os.path.join(limpio, "prosa.md"), "w", encoding="utf-8") as fh:
            fh.write("Tus cinco dan `web/src=0` y el href=gone/x.png de esta frase es prosa.\n")
        if main([limpio]) != 0:
            failures.append("un src=/href= en PROSA se conto como enlace")
        #    ...y dentro de una etiqueta SIGUE contando, incluso sin comillas ni en minuscula.
        with open(os.path.join(limpio, "etiqueta.md"), "w", encoding="utf-8") as fh:
            fh.write("<IMG SRC=gone/dentro.png>\n")
        if main([limpio]) != 1:
            failures.append("un atributo DENTRO de una etiqueta dejo de contarse")
        os.remove(os.path.join(limpio, "etiqueta.md"))

        # ── CASO · --routes: un destino ABSOLUTO es una RUTA de sitio, no un fichero ──────
        with open(os.path.join(limpio, "ruta.md"), "w", encoding="utf-8") as fh:
            fh.write("[r](/reference/modules/overview/)\n[rel](./no-existe.md)\n")
        if main([limpio]) != 1:
            failures.append("sin --routes, un destino absoluto muerto tiene que enrojecer")
        if main(["--routes", limpio]) != 1:
            failures.append("--routes no debe tapar un RELATIVO muerto del mismo fichero")
        os.remove(os.path.join(limpio, "ruta.md"))
        with open(os.path.join(limpio, "solo-ruta.md"), "w", encoding="utf-8") as fh:
            fh.write("[r](/reference/modules/overview/)\n")
        if main([limpio]) != 1:
            failures.append("un destino absoluto inexistente deberia enrojecer sin --routes")
        if main(["--routes", limpio]) != 0:
            failures.append("--routes no ignoro un destino absoluto")

        # ── CASO · --tracked no ve lo que git no ve ──────────────────────────────────────
        #    Es el acotado del gate: 26 363 markdown bajo .claude/worktrees frente a 4 417
        #    trackeados. Un muerto en un fichero IGNORADO no enrojece; en uno trackeado, si.
        repo = tempfile.mkdtemp()
        for cmd in (["init", "-q"], ["config", "user.email", "t@t"], ["config", "user.name", "t"]):
            subprocess.run(["git", "-C", repo] + cmd, check=True, capture_output=True)
        os.makedirs(os.path.join(repo, "ignorado"))
        with open(os.path.join(repo, ".gitignore"), "w") as fh:
            fh.write("ignorado/\n")
        with open(os.path.join(repo, "ignorado", "muerto.md"), "w") as fh:
            fh.write("[x](no-existe.md)\n")
        with open(os.path.join(repo, "vivo.md"), "w") as fh:
            fh.write("[ok](.gitignore)\n")
        subprocess.run(["git", "-C", repo, "add", "-A"], check=True, capture_output=True)
        subprocess.run(["git", "-C", repo, "commit", "-qm", "x"], check=True, capture_output=True)
        if main([repo]) != 1:
            failures.append("el modo arbol-suelto deberia ver el muerto del directorio ignorado")
        if main(["--tracked", repo]) != 0:
            failures.append("--tracked enrojecio por un fichero que git no rastrea")
        with open(os.path.join(repo, "roto.md"), "w") as fh:
            fh.write("[x](no-existe.md)\n")
        subprocess.run(["git", "-C", repo, "add", "roto.md"], check=True, capture_output=True)
        if main(["--tracked", repo]) != 1:
            failures.append("--tracked no vio un muerto en un fichero TRACKEADO")
    if failures:
        for f in failures:
            print(f"check-md-links self-test FAIL: {f}", file=sys.stderr)
        return 1
    print("check-md-links self-test OK: every dead or escaping reference red, every live one green")
    return 0


def main(argv):
    skips = []
    args = []
    tracked = False
    routes = False
    baseline = None
    it = iter(argv)
    for a in it:
        if a == "--skip":
            skips.append(next(it))
        elif a == "--tracked":
            tracked = True
        elif a == "--routes":
            routes = True
        elif a == "--baseline":
            baseline = next(it)
        elif a == "--self-test":
            return self_test()
        else:
            args.append(a)
    root = args[0] if args else "."
    files = tracked_md(root) if tracked else None
    found = unresolved(root, skips, files=files, routes=routes)
    # ── LÍNEA BASE: un TRINQUETE, no una amnistía ────────────────────────────────────────
    #    Los enlaces muertos que hoy existen son decisiones de CONTENIDO de otros dueños
    #    (capturas del material de lanzamiento que nadie ha hecho, un contrato que se cita y
    #    no existe). Congelarlos en silencio sería el defecto; la línea base los NOMBRA uno a
    #    uno con su razón, el gate rechaza cualquiera que NO esté en ella, y **rechaza también
    #    si la línea base tiene entradas que ya no ocurren**: así sólo puede encoger.
    if baseline:
        try:
            with open(baseline, encoding="utf-8") as fh:
                base = {l.split("\t", 1)[0].strip() for l in fh
                        if l.strip() and not l.startswith("#")}
        except OSError as exc:
            print(f"check-md-links: NO HE PODIDO MIRAR: {exc}", file=sys.stderr)
            return 2
        actual = {f.split(": ", 1)[0] for f in found}
        nuevos = [f for f in found if f.split(": ", 1)[0] not in base]
        rancios = sorted(base - actual)
        for f in nuevos:
            print(f"NUEVO {f}")
        for r in rancios:
            print(f"RANCIO {r} — ya no ocurre; quitalo de {baseline}")
        return 1 if (nuevos or rancios) else 0
    for f in found:
        print(f)
    # ⛔ HASTA 2026-08-26 ESTO ERA `return 0` INCONDICIONAL: imprimía los hallazgos y salía 0.
    #    No existía ninguna rama que devolviera no-cero. Yo mismo publiqué «cero enlaces
    #    muertos» leyendo ese 0 sobre una corrida con 8 854 hallazgos impresos delante.
    #    Y el daño real no era la cifra: la fila decía «cablear este guion como fast-lint», y
    #    cableado así habría sido un gate PERMANENTEMENTE VERDE, imposible de enrojecer,
    #    ocupando un puesto en el hook y dando confianza sin dar información.
    return 1 if found else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
