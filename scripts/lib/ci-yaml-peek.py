# SPDX-License-Identifier: AGPL-3.0-only
"""Read step fields, owning jobs and task commands using the standard library.

POR QUE EXISTE, y no es preferencia de estilo. Las dos baterias que lo usan corren como paso de
`mainline-ci`, y ese job corre en un runner AUTOALOJADO (`hetzner`, srv17), no en una imagen de
GitHub. Medido el 2026-08-30 sobre el arbol entero: de todos los `run:` de todos los workflows,
UNO SOLO usa python (`pg-majors-evaluate.py`) y ese importa unicamente stdlib. Es decir: NADA en
este repositorio demuestra que PyYAML se alcance en ese runner. Un gate nuevo cuya primera
instruccion fuera `import yaml` seria una dependencia de entorno sin medir — exactamente la clase
que el claim que trae este fichero esta curando un piso mas arriba, y la que el 2026-08-27 mato
dos pushes de 75 minutos con un `import yaml` en la linea 1346 de 1400.

QUE NO ES: un parser de YAML. Lee la forma concreta y estable de estos dos ficheros (dos espacios
de sangria, pasos a seis, claves de paso a ocho) y **rehusa** en cuanto lo que ve no encaja: la
respuesta a la duda es 2, «no he podido mirar», nunca una cadena vacia que el llamante lea como
ausencia.

Salidas: 0 imprime lo pedido · 2 no he podido mirar (fichero ilegible o forma inesperada)
         3 missing or ambiguous; equal values in distinct jobs are accepted.
"""
import re
import sys


def _lineas(ruta):
    try:
        with open(ruta, encoding="utf-8") as fh:
            return fh.read().splitlines()
    except OSError as exc:
        sys.stderr.write(f"{exc}\n")
        raise SystemExit(2)


def _region(lineas, cabecera, sangria):
    """Las lineas bajo `cabecera` hasta la siguiente clave de la MISMA sangria."""
    ini = None
    for i, ln in enumerate(lineas):
        if ln == cabecera:
            if ini is not None:
                raise SystemExit(2)  # dos veces la misma cabecera: no se cual
            ini = i + 1
    if ini is None:
        raise SystemExit(3)
    pref = " " * sangria
    for j in range(ini, len(lineas)):
        ln = lineas[j]
        if not ln.strip() or ln.lstrip().startswith("#"):
            continue
        hueco = len(ln) - len(ln.lstrip(" "))
        if hueco <= sangria and not ln.startswith(pref + " "):
            return lineas[ini:j]
    return lineas[ini:]


def _bloques(region, marca):
    """Parte una region de lista en sus elementos, cada uno empezando por `marca`.

    La primera linea se NORMALIZA: en YAML la primera clave de un elemento viaja en la propia
    linea del guion (`      - name: X`), no a la sangria de las demas. Se sustituye el `- ` por
    dos espacios, que ocupa las mismas columnas, y a partir de ahi TODAS las claves del bloque
    estan a la misma sangria. Sin esto, `name:` y un `cmd: |` inicial resultan invisibles — y lo
    fueron: la primera version de este fichero fallo exactamente esos dos casos contra PyYAML.
    """
    out, act = [], None
    for ln in region:
        if ln.startswith(marca):
            if act is not None:
                out.append(act)
            act = [marca[:-2] + "  " + ln[len(marca):]]
        elif act is not None:
            act.append(ln)
    if act is not None:
        out.append(act)
    return out


def _valor(bloque, clave, sangria):
    """El valor de `clave` en un bloque. Escalar en linea, o bloque literal tras `|`."""
    pref = " " * sangria + clave + ":"
    if sum(ln.startswith(pref) for ln in bloque) > 1:
        raise SystemExit(2)
    for i, ln in enumerate(bloque):
        if not ln.startswith(pref):
            continue
        resto = ln[len(pref):].strip()
        if resto not in ("|", "|-", ">"):
            return resto
        cuerpo = []
        hueco = None
        for sig in bloque[i + 1:]:
            if not sig.strip():
                cuerpo.append("")
                continue
            h = len(sig) - len(sig.lstrip(" "))
            if hueco is None:
                hueco = h
            if h < hueco:
                break
            cuerpo.append(sig[hueco:])
        while cuerpo and not cuerpo[-1]:
            cuerpo.pop()
        return "\n".join(cuerpo) + "\n"
    return None


def _pasos(ruta, job):
    lineas = _lineas(ruta)
    reg = _region(lineas, f"  {job}:", 2)
    sub = _region(reg, "    steps:", 4)
    return _bloques(sub, "      - ")


def _jobs(lineas):
    """Read every job in the supported layout; do not skip unknown headers."""
    region = _region(lineas, "jobs:", 0)
    jobs = []
    for line in region:
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        indent = len(line) - len(line.lstrip(" "))
        if indent < 6 and line.lstrip(" ").startswith("\t"):
            raise SystemExit(2)
        if indent == 2:
            match = re.fullmatch(r"  ([A-Za-z_][A-Za-z0-9_-]*):", line)
            if match is None or match[1] in jobs:
                raise SystemExit(2)
            jobs.append(match[1])
        elif indent < 2:
            raise SystemExit(2)
    if not jobs:
        raise SystemExit(2)
    return jobs


def _pasos_de(lineas, job):
    """Only a reusable-workflow job may omit its local steps."""
    region = _region(lineas, f"  {job}:", 2)
    try:
        steps = _region(region, "    steps:", 4)
    except SystemExit as error:
        if error.code != 3:
            raise
        if any(line.startswith("    steps:") for line in region):
            raise SystemExit(2)
        if _valor(region, "uses", 4) is not None:
            return []
        raise SystemExit(2)
    blocks = _bloques(steps, "      - ")
    if not blocks:
        raise SystemExit(2)
    ids = set()
    for block in blocks:
        ident = _valor(block, "id", 8)
        if ident is not None:
            if ident in ids:
                raise SystemExit(2)
            ids.add(ident)
    return blocks


def _busca(ruta, predicate):
    lines = _lineas(ruta)
    found = []
    for job in _jobs(lines):
        for block in _pasos_de(lines, job):
            if predicate(block):
                found.append((job, block))
    return found


def _un_valor(found, field):
    if not found:
        raise SystemExit(3)
    if len({job for job, _ in found}) != len(found):
        raise SystemExit(3)
    values = {(_valor(block, field, 8) or "") for _, block in found}
    if len(values) != 1:
        raise SystemExit(3)
    return values.pop()


def main(argv):
    if len(argv) < 2:
        sys.stderr.write("uso: ci-yaml-peek.py <orden> ...\n")
        return 2
    orden = argv[1]

    if orden == "step-field-anyjob":  # <workflow> <id> <field>
        path, ident, field = argv[2:5]
        found = _busca(path, lambda block: _valor(block, "id", 8) == ident)
        sys.stdout.write(_un_valor(found, field))
        return 0

    if orden == "step-field-for-consumer":  # <workflow> <provider> <consumer> <field>
        path, provider, consumer, field = argv[2:6]
        found = _busca(path, lambda block: _valor(block, "id", 8) in (provider, consumer))
        owners = {job for job, block in found if _valor(block, "id", 8) == consumer}
        selected = [(job, block) for job, block in found
                    if job in owners and _valor(block, "id", 8) == provider]
        if not owners or {job for job, _ in selected} != owners:
            return 3
        sys.stdout.write(_un_valor(selected, field))
        return 0

    if orden == "step-if-byname-anyjob":  # <workflow> <name fragment>
        path, fragment = argv[2:4]
        found = _busca(path, lambda block: fragment in (_valor(block, "name", 8) or ""))
        sys.stdout.write(_un_valor(found, "if"))
        return 0

    if orden == "step-jobs":  # <workflow> <id>
        path, ident = argv[2:4]
        found = _busca(path, lambda block: _valor(block, "id", 8) == ident)
        if not found:
            return 3
        sys.stdout.write("".join(job + "\n" for job, _ in found))
        return 0

    if orden == "step-field":          # <workflow> <job> <id> <campo>
        ruta, job, ident, campo = argv[2:6]
        hall = [b for b in _pasos(ruta, job) if _valor(b, "id", 8) == ident]
        if len(hall) != 1:
            return 3
        v = _valor(hall[0], campo, 8)
        sys.stdout.write("" if v is None else v)
        return 0

    if orden == "step-if-byname":      # <workflow> <job> <trozo-del-nombre>
        ruta, job, trozo = argv[2:5]
        hall = [b for b in _pasos(ruta, job) if trozo in (_valor(b, "name", 8) or "")]
        if len(hall) != 1:
            return 3
        v = _valor(hall[0], "if", 8)
        sys.stdout.write("" if v is None else v)
        return 0

    if orden == "task-cmd":            # <taskfile> <tarea> <trozo-del-cmd>
        ruta, tarea, trozo = argv[2:5]
        lineas = _lineas(ruta)
        reg = _region(lineas, f"  {tarea}:", 2)
        sub = _region(reg, "    cmds:", 4)
        hall = []
        for b in _bloques(sub, "      - "):
            v = _valor(b, "cmd", 8)
            if v and trozo in v:
                hall.append(v)
        if len(hall) != 1:
            return 3
        sys.stdout.write(hall[0])
        return 0

    sys.stderr.write(f"orden desconocida: {orden}\n")
    return 2


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
