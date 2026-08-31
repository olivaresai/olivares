#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# subst-once.py — sustitución ANCLADA para baterías de mutación, y la mitad que importa
# es que FALLA cuando el ancla no está.
#
# ⛔ POR QUÉ EXISTE. Un mutante que no se aplica no es un caso débil: es un caso que
# APRUEBA el gate sin haberlo probado, y desde fuera se lee igual que un mutante que el
# gate cazó. En este repositorio ya costó dar por «verificado» un control ciego
# (`a-mutant-that-does-not-apply-reports-the-gate-as-blind`). `sed` no puede distinguir
# «sustituí» de «no encontré nada»: su rc es 0 en los dos casos.
#
# Y no usa PyYAML aunque su sujeto habitual sea YAML: PyYAML no está en ningún contenedor
# de este proyecto y no hay `pip` ni `ensurepip` (medido el 2026-08-19,
# `scripts/check-ci-env-reach.sh:17-23`). Aquí sólo hace sustitución de texto anclada; el
# que parsea YAML de verdad es el guard en Go que la batería ejercita.
#
# USO
#   subst-once.py <fichero> <ancla> <reemplazo>
#   subst-once.py --move-step <fichero-workflow> <trozo-del-nombre-del-paso>
#
# El segundo modo mueve un paso de GitHub Actions al FINAL de su lista de pasos, sin
# tocar nada más. Existe para probar invariantes de ORDEN, que son las que una
# comprobación de presencia no ve.
#
# Salida: 0 sustituido · 1 el ancla no estaba (o el paso no se pudo aislar).
import re
import sys


def die(msg: str) -> None:
    print(f"subst-once: {msg}", file=sys.stderr)
    raise SystemExit(1)


def move_step(path: str, needle: str) -> None:
    text = open(path, encoding="utf-8").read()
    # Un paso empieza en `      - name: …` y termina donde empieza el siguiente `      - `
    # de la misma indentación, o al final del fichero.
    pattern = re.compile(
        r"^      - name: [^\n]*" + re.escape(needle) + r"[^\n]*\n(?:(?!^      - ).*\n)*",
        re.M,
    )
    m = pattern.search(text)
    if not m:
        die(f"cannot isolate a step whose name contains {needle!r} in {path}")
    step = m.group(0)
    rest = text[: m.start()] + text[m.end():]
    if not rest.endswith("\n"):
        rest += "\n"
    open(path, "w", encoding="utf-8").write(rest + step)


def main() -> None:
    argv = sys.argv[1:]
    if argv and argv[0] == "--move-step":
        if len(argv) != 3:
            die("usage: subst-once.py --move-step <file> <step-name-fragment>")
        move_step(argv[1], argv[2])
        return
    if len(argv) != 3:
        die("usage: subst-once.py <file> <anchor> <replacement>")
    path, anchor, replacement = argv
    text = open(path, encoding="utf-8").read()
    if anchor not in text:
        die(f"anchor absent in {path}: {anchor[:120]!r}")
    open(path, "w", encoding="utf-8").write(text.replace(anchor, replacement, 1))


if __name__ == "__main__":
    main()
