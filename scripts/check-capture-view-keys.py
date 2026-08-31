#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Comprueba que las entradas de `VIEWS` en `web/e2e/docs-captures.spec.ts` sólo usen claves que su
# propio tipo declara. rc: 0 al día · 1 hay una clave no declarada · 2 no he podido mirar.
#
# ⛔ POR QUE EXISTE, Y NO ES ESTILO. Medido el 2026-08-30 sobre `main`: el arnés invoca
#    `view.despues` (`docs-captures.spec.ts:908`) y DOS entradas la usan, pero **`despues` no está
#    en el tipo de `VIEWS`** — sólo `prepara`. En TypeScript eso sería un error de compilación… si
#    algún `tsconfig` mirara el fichero, y **ninguno lo mira**: `tsconfig.app.json` incluye sólo
#    `src`, `tsconfig.node.json` sólo `vite.config.ts`, y nada menciona `e2e`. Verificado hoy, con
#    la línea delante. Así que el desacuerdo entre el tipo y el uso vive ahí sin que nada lo vea.
#
# ⛔ Y EL DAÑO NO ES EL TIPO DESACTUALIZADO: ES EL TYPO SILENCIOSO. El arnés hace
#    `if (view.despues) await view.despues(page)`. Una entrada que escriba `despuess`, o que use
#    `prepara` creyendo que es lo mismo, **no falla**: la condición sale falsa, no se pincha nada y
#    la captura se guarda igual — con la pestaña POR DEFECTO — mientras su `id` promete el estado
#    interno. Es la peor forma de verde que hay en este repositorio: la foto existe, el fichero
#    pesa, el gate pasa, y enseña la pantalla equivocada. Ya pasó una vez con otra cara y por eso
#    la propia spec dice de sí misma *«una captura que se guardara con el esqueleto puesto pasaría
#    cualquier comprobación de “existe el fichero”, y ya pasó una vez»* (`:591-593`).
#
# ⇒ Este gate compara las claves USADAS contra las DECLARADAS, que es la comprobación que `tsc`
#   haría si mirase el fichero. No sustituye a meterlo en un `tsconfig` —eso sería mejor y es de
#   quien gobierne la configuración del web—; corta el caso concreto mientras tanto.
import re
import sys

RC_LIMPIO, RC_HALLAZGO, RC_NO_PUDE_MIRAR = 0, 1, 2

RUTA = "web/e2e/docs-captures.spec.ts"


def _blanquea(m):
    """Sustituye por espacios del MISMO largo, para no mover ni un indice."""
    return " " * (m.end() - m.start())


def sin_prosa(src):
    """Quita comentarios y cadenas. NO es higiene: es la diferencia entre medir y contar prosa.

    ⛔ CALIBRADO CONTRA EL FICHERO REAL, y sin esto la sonda miente en las dos direcciones. Medido
       sobre `docs-captures.spec.ts`: `\bview\.` casa **cinco** veces con `adoption-view.tsx`, que
       es un NOMBRE DE FICHERO citado en prosa, y aparecen `vista.id` / `vista.heading` /
       `vista.path` que viven **solo dentro de comentarios en castellano**. Un gate que contara eso
       inventaria claves y se apagaria a la primera.

    ⛔⛔ Y LA VERSION ANTERIOR BLANQUEABA LA PLANTILLA ENTERA, `${...}` INCLUIDO — que es CODIGO, no
       prosa. El dano era un falso negativo del caso INSIGNIA de este gate: una invocacion escrita
       dentro de una plantilla (`page.goto(`/x/$-{view.despuess}`)`, sin el guion) es un typo que
       nada satisface jamas, y salia **rc 0**. Comprobado a mano ANTES de tocar nada: el token no
       sobrevivia a la limpieza.

       Y dejaba huella en el propio gate: la lista `- {"id", "path"}` de mas abajo se escribio a
       mano para callar el falso positivo «gancho muerto» de dos claves que SI se invocan… dentro
       de plantillas. Una exencion a mano tapando el agujero de la sonda en vez de arreglarla.
    """
    return _sin_cadenas(src)


def _fin_cadena(src, i):
    """Indice pasado el cierre de la cadena que abre en `i`. Sin cierre: fin de linea."""
    comilla, j, n = src[i], i + 1, len(src)
    while j < n:
        if src[j] == "\\":
            j += 2
            continue
        if src[j] == comilla:
            return j + 1
        if src[j] == "\n":
            return j
        j += 1
    return n


def _fin_interp(src, i):
    """Indice pasado el `}` que cierra la interpolacion cuya llave abre en `i`."""
    prof, j, n = 0, i, len(src)
    while j < n:
        c = src[j]
        if c in "'\"":
            j = _fin_cadena(src, j)
            continue
        if c == "`":
            j = _fin_plantilla(src, j)
            continue
        if c == "{":
            prof += 1
        elif c == "}":
            prof -= 1
            if prof == 0:
                return j + 1
        j += 1
    return n


def _fin_plantilla(src, i):
    """Indice pasado el cierre de la plantilla que abre en `i`."""
    j, n = i + 1, len(src)
    while j < n:
        if src[j] == "\\":
            j += 2
            continue
        if src[j] == "`":
            return j + 1
        if src[j : j + 2] == "${":
            j = _fin_interp(src, j + 1)
            continue
        j += 1
    return n


def _sin_cadenas(src):
    """Blanquea comentarios y cadenas CONSERVANDO el codigo de las interpolaciones.

    Devuelve una cadena del MISMO largo (espacios, y respeta los saltos), que es lo que permite que
    el recorte por posiciones de mas abajo siga valiendo.
    """
    fuera = list(src)

    def blanquea(a, b):
        for k in range(a, b):
            if fuera[k] != "\n":
                fuera[k] = " "

    i, n = 0, len(src)
    while i < n:
        c = src[i]
        if src[i : i + 2] == "/*":
            j = src.find("*/", i + 2)
            j = n if j < 0 else j + 2
            blanquea(i, j)
            i = j
        elif src[i : i + 2] == "//":
            j = src.find("\n", i)
            j = n if j < 0 else j
            blanquea(i, j)
            i = j
        elif c in "'\"":
            j = _fin_cadena(src, i)
            blanquea(i, j)
            i = j
        elif c == "`":
            j = i + 1
            blanquea(i, i + 1)
            while j < n:
                if src[j] == "\\":
                    blanquea(j, min(j + 2, n))
                    j += 2
                elif src[j] == "`":
                    blanquea(j, j + 1)
                    j += 1
                    break
                elif src[j : j + 2] == "${":
                    fin = _fin_interp(src, j + 1)
                    blanquea(j, j + 2)
                    fuera[j + 2 : fin - 1] = list(_sin_cadenas(src[j + 2 : fin - 1]))
                    blanquea(fin - 1, fin)
                    j = fin
                else:
                    blanquea(j, j + 1)
                    j += 1
            i = j
        else:
            i += 1
    return "".join(fuera)


FORMAS = (
    # ⛔ ESTA LISTA ES A MANO Y NO PUEDE DEJAR DE SERLO: son formas sintacticas de JavaScript, y no
    #    hay forma de derivarlas sin un parser. Lo que SI se puede es no fiarse de ella —ver
    #    `usos_no_modelados`—, que es la diferencia entre una lista incompleta y una lista que miente.
    r"for\s*\(\s*(?:const|let|var)\s+(\w+)\s+of\s+VIEWS\b",
    r"VIEWS\s*\.\s*(?:map|forEach|filter|find|flatMap)\s*\(\s*\(?\s*(\w+)",
    r"(\w+)\s*:\s*\(typeof\s+VIEWS\)\[number\]",
)


def usos_no_modelados(limpio):
    """Sitios donde el codigo TOCA `VIEWS` de una forma que este gate no sabe leer.

    ⛔ SIN ESTO, LA LISTA DE FORMAS MIENTE EN SILENCIO. Ya habia una guarda para «ninguna ligadura»
       (rc 2), y eso cubre el caso facil: una spec entera escrita de otra manera. **No cubre el
       caso mixto, que es el que se da en la vida real**: un `for … of VIEWS` que la lista SI
       modela conviviendo con un `VIEWS.reduce((acc, v) => …)` que no. Entonces la guarda pasa
       —hay ligaduras— y las invocaciones del segundo son invisibles. Medido con un fichero de
       prueba: `v.despuess`, un typo de invocacion, salia **rc 0 «sin hallazgos»**.

       Asi que la lista sigue siendo a mano y deja de ser una promesa: cada mencion a `VIEWS` que no
       caiga ni en la declaracion ni en una forma modelada se NOMBRA, y el gate dice que no pudo
       mirar en vez de decir que esta limpio.
    """
    cubierto = []
    for pat in FORMAS + (r"const\s+VIEWS\b", r"typeof\s+VIEWS\b"):
        cubierto += [m.span() for m in re.finditer(pat, limpio)]
    sueltos = []
    for m in re.finditer(r"\bVIEWS\b", limpio):
        if not any(a <= m.start() < b for a, b in cubierto):
            sueltos.append(limpio.count("\n", 0, m.start()) + 1)
    return sueltos


def ligaduras(limpio):
    """Nombres de variable que REPRESENTAN una entrada de VIEWS, DERIVADOS del codigo.

    ⛔ NO SE FIJAN A MANO. Hoy la unica es `view` (`for (const view of VIEWS)`), pero escribir
       «view» en el gate seria una lista de un elemento que caduca en cuanto alguien renombre o
       añada un `.map`. Se buscan las formas por las que un nombre QUEDA LIGADO a una entrada.
    """
    n = set()
    for pat in FORMAS:
        n |= set(re.findall(pat, limpio))
    return n


def razon_de_existir():
    """Comprueba —no recita— si algun `tsconfig` mira `e2e/`, que es la premisa de este gate.

    ⛔ ESTA FRASE SE IMPRIMIA COMO VEREDICTO SIN COMPROBARSE: «NINGUN tsconfig incluye `e2e/`». Se
       verifico UNA vez a mano, y desde entonces se afirmaba en cada rojo. El dia que alguien meta
       `e2e/` en un tsconfig —que seria lo DESEABLE, y la cabecera de este fichero lo dice— el gate
       seguiria diciendo que nadie lo hace, y su propia razon de existir se volveria falsa mientras
       la sigue imprimiendo. Una garantia escrita donde no hay control.

       Se mide por SUBCADENA y no parseando: un `tsconfig` es JSONC —comentarios de bloque, comas
       colgantes— y tres intentos de parsearlo aqui fallaron. Para la pregunta que se hace, «¿alguno
       NOMBRA e2e?», la subcadena es suficiente y se equivoca hacia el lado seguro: si aparece en un
       comentario, deja de afirmar en vez de afirmar de mas.
    """
    import glob
    ficheros = sorted(glob.glob("web/tsconfig*.json"))
    if not ficheros:
        return ("no he podido comprobar si algun `tsconfig` mira `e2e/`: no encuentro ninguno "
                "(¿ruta distinta?), asi que NO afirmo la premisa de este gate")
    nombran = []
    for f in ficheros:
        try:
            if "e2e" in open(f, encoding="utf-8").read():
                nombran.append(f)
        except OSError as e:
            return f"no he podido leer {f} ({type(e).__name__}): no afirmo la premisa de este gate"
    if nombran:
        return ("⚠ ALGUN tsconfig NOMBRA `e2e`: " + ", ".join(nombran) + " — si de verdad lo "
                "compila, `tsc` ya caza esto y este gate podria sobrar. Compruebalo.")
    return (f"`tsc` no puede cazarlo: ninguno de los {len(ficheros)} tsconfig de `web/` nombra "
            "`e2e` (medido ahora, no recordado)")


def universo():
    """Los ficheros de `web/e2e/` que DECLARAN una tabla `VIEWS`, enumerados, no listados a mano.

    ⛔ ESTE GATE FIJABA SU UNIVERSO A UN SOLO FICHERO (`RUTA`) y salia verde. Medido: `web/e2e/`
       tiene DOS tablas `VIEWS` —`docs-captures.spec.ts` y `management-views.spec.ts`— y la
       segunda no la miraba nadie. Un gate con el universo escrito a mano no da un falso verde
       ruidoso: da uno silencioso, porque lo que no enumera no existe para el.
    """
    import glob
    return sorted(f for f in glob.glob("web/e2e/*.spec.ts")
                  if "const VIEWS" in open(f, encoding="utf-8", errors="replace").read())


def revisa(ruta):
    try:
        src = open(ruta, encoding="utf-8").read()
    except OSError as e:
        print(f"check-capture-view-keys: NO HE PODIDO MIRAR: {e}", file=sys.stderr)
        return RC_NO_PUDE_MIRAR

    # ── el TIPO ───────────────────────────────────────────────────────────────────────────────
    # ⛔ EL TIPO SE LEE SIN PROSA, IGUAL QUE TODO LO DEMAS. Este gate ya blanqueaba comentarios y
    #    cadenas para el cuerpo del array y para el barrido de invocaciones, y el LADO DEL TIPO era
    #    el unico que corria sobre el fuente en crudo. Consecuencia medida sobre la spec REAL: si se
    #    retira `despues` de la declaracion y se deja MENCIONADA dentro de un `/* */`, el gate la
    #    cuenta en `declaradas en el tipo` y sale **rc 0 «sin hallazgos»** — es decir, un comentario
    #    que nombra la clave que falta basta para apagar el hallazgo. Y no es un caso inventado: es
    #    palabra por palabra el estado que la cabecera de este gate describe como su motivo
    #    (`despues` invocada en `:908` y ausente del tipo). El gate existia para cazar eso.
    #
    #    `sin_prosa` sustituye por espacios del MISMO largo, asi que los indices no se mueven y el
    #    recorte por profundidad de mas abajo —que depende de posiciones— sigue valiendo.
    src_sin_prosa = sin_prosa(src)
    m = re.search(r"const VIEWS:\s*\{(.*?)\}\[\]\s*=", src_sin_prosa, re.S)
    if not m:
        print("check-capture-view-keys: NO HE PODIDO MIRAR: no encuentro la declaracion "
              "`const VIEWS: { … }[] =`; si la spec cambio de forma, este gate no la entiende "
              "y NO puede decir que este limpia", file=sys.stderr)
        return RC_NO_PUDE_MIRAR
    declaradas = set(re.findall(r"^\s*(\w+)\??\s*:", m.group(1), re.M))
    if not declaradas:
        print("check-capture-view-keys: NO HE PODIDO MIRAR: el tipo de VIEWS no declara ni una "
              "clave — eso es que no lo he leido bien, no que no tenga", file=sys.stderr)
        return RC_NO_PUDE_MIRAR

    # ── el USO ────────────────────────────────────────────────────────────────────────────────
    # Se recorta el literal del array por conteo de llaves desde `= [` hasta su cierre, para no
    # arrastrar el resto del fichero (que tiene objetos a patadas y daria claves fantasma).
    inicio = src.index("const VIEWS:")
    corchete = src.index("[", src.index("= [", inicio))
    prof, fin = 0, None
    for i in range(corchete, len(src)):
        if src[i] == "[":
            prof += 1
        elif src[i] == "]":
            prof -= 1
            if prof == 0:
                fin = i
                break
    if fin is None:
        print("check-capture-view-keys: NO HE PODIDO MIRAR: el literal de VIEWS no cierra",
              file=sys.stderr)
        return RC_NO_PUDE_MIRAR
    cuerpo = src[corchete:fin]

    # ⛔ SE QUITAN COMENTARIOS Y CADENAS ANTES DE BUSCAR CLAVES. Sin esto, la prosa de los
    #    comentarios —que en esta spec es larguisima y cita `id:`, `path:` y nombres de campo—
    #    entraria como si fuera codigo. Es el defecto que este repositorio ya ha medido: «un regex
    #    de tokens cuenta la prosa del fichero que mide». Se sustituyen por espacios del MISMO
    #    largo para no mover ni un indice: la profundidad de abajo depende de las posiciones.
    def blanquea(m):
        return " " * (m.end() - m.start())

    limpio = re.sub(r"/\*.*?\*/", blanquea, cuerpo, flags=re.S)
    limpio = re.sub(r"//[^\n]*", blanquea, limpio)
    limpio = re.sub(r"'(?:\\.|[^'\\\n])*'|\"(?:\\.|[^\"\\\n])*\"|`(?:\\.|[^`\\])*`",
                    blanquea, limpio, flags=re.S)

    # ⛔ LA CLAVE DE UNA ENTRADA SE RECONOCE POR PROFUNDIDAD, NO POR UNA LISTA DE EXCEPCIONES.
    #    La primera version llevaba un allowlist de nombres anidados (`timeout`, `name`, `state`…)
    #    y daba SIETE falsos positivos: `body`, `status`, `json`… son claves de un objeto de
    #    `page.route(...).fulfill(...)` DENTRO de un closure, no claves de `VIEWS`. Una lista de
    #    excepciones es «la forma de gate que mas veces hemos encontrado rota» (canon §0-COBERTURA):
    #    caduca con el primer nombre nuevo y produce ruido que hace que el gate se ignore. La
    #    profundidad no caduca: una clave de entrada vive a UN nivel de llave dentro del array.
    usadas, prof_llave, prof_corchete = set(), 0, 0
    # ⛔ ESTA ALTERNANCIA NO VEIA LA PRIMERA CLAVE DE UNA ENTRADA ESCRITA EN LINEA, y era un FALSO
    #    NEGATIVO en el sitio que mas duele. `re.finditer` NO solapa: en `{ id: 'x'` la rama
    #    `[{}\[\]]` casaba y CONSUMIA la llave, asi que al llegar a ` id:` el escaner ya no tenia
    #    delante ni `^` (no es inicio de linea) ni `{` ni `,`, y la clave no entraba en `usadas`.
    #
    #    Medido sobre la spec REAL: cambiando `{ id: 'inventory'` por `{ idd: …` el gate imprimia
    #    «sin hallazgos: tipo, entradas e invocaciones concuerdan» y salia 0 — con una entrada que
    #    ha perdido justo la clave con la que el arnes NOMBRA el test y el fichero de la captura
    #    (`docs-captures.spec.ts`: `test(\`capture ${view.id} …\`)`). El MISMO typo una clave mas
    #    alla si salia 1: era la POSICION, no el typo. Y el caso del banco que dice dar sentido al
    #    gate ponia su typo en tercera posicion, asi que media otra cosa.
    #
    #    La cura es una MIRADA ATRAS: `(?<=[\{,])` inspecciona el caracter anterior SIN consumirlo,
    #    asi que la rama de las llaves sigue contando profundidad y la clave de detras se ve igual.
    #    Se conserva la rama `^` para la forma multilinea.
    for m2 in re.finditer(r"[{}\[\]]|(?<=[\{,])\s*(\w+)\s*:|^\s*(\w+)\s*:", limpio, re.M):
        tok = m2.group(0).strip()
        clave = m2.group(1) or m2.group(2)
        if clave is None:
            if tok == "{":
                prof_llave += 1
            elif tok == "}":
                prof_llave -= 1
            elif tok == "[":
                prof_corchete += 1
            elif tok == "]":
                prof_corchete -= 1
            continue
        # dentro del array (corchete 1) y en el objeto de la entrada (llave 1): eso es una clave.
        if prof_corchete == 1 and prof_llave == 1:
            usadas.add(clave)

    if not usadas:
        print("check-capture-view-keys: NO HE PODIDO MIRAR: no he reconocido ni una clave de "
              "entrada; la spec ha cambiado de forma y este gate no la entiende", file=sys.stderr)
        return RC_NO_PUDE_MIRAR
    hallazgos = sorted(k for k in usadas if k not in declaradas)

    # ── las INVOCACIONES, que es el lado donde el fallo es silencioso ────────────────────────
    #
    # ⛔ ESTE BLOQUE FALTABA Y ERA JUSTO EL QUE EL GATE PROMETIA (the reviewer). La version anterior
    #    cruzaba el TIPO con las CLAVES DE ENTRADA y salia rc 0 ante el unico typo que de verdad
    #    hace daño: el de la INVOCACION. Reproducido con un fixture sintetico — entrada correcta,
    #    arnes leyendo `view.despuess` — y daba **rc 0 diciendo «ninguna»**. Construi un control que
    #    media lo de al lado y me lo cobre como si cubriera lo de en medio.
    limpio_todo = sin_prosa(src)
    nombres = ligaduras(limpio_todo)
    if not nombres:
        print("check-capture-view-keys: NO HE PODIDO MIRAR: no encuentro ninguna variable ligada a "
              "una entrada de VIEWS (ni `for … of VIEWS`, ni `.map`, ni un parametro tipado). La "
              "spec cambio de forma y NO puedo decir que las invocaciones esten bien",
              file=sys.stderr)
        return RC_NO_PUDE_MIRAR
    sueltos = usos_no_modelados(limpio_todo)
    if sueltos:
        print(f"check-capture-view-keys: NO HE PODIDO MIRAR: {ruta} toca `VIEWS` en la(s) linea(s) "
              f"{sueltos} de una forma que no se leer (no es la declaracion ni un `for … of` ni un "
              "`.map`/`.forEach`/`.filter`/`.find`/`.flatMap` ni un parametro tipado). Las claves "
              "que se invoquen por ahi me son invisibles, asi que NO puedo decir que este limpio",
              file=sys.stderr)
        return RC_NO_PUDE_MIRAR

    # ⛔ QUIEN INVOCA CADA CLAVE, NO «LA PRIMERA POR ORDEN ALFABETICO». El diagnostico decia
    #    `{sorted(nombres)[0]}.{k}` — con dos ligaduras (`for (const alfa of VIEWS)` y
    #    `VIEWS.map((zeta) => …)`) el typo de `zeta` se anunciaba como `alfa.despuess`. Medido: el
    #    gate mandaba al lector al bucle equivocado, y el bucle equivocado esta LIMPIO, asi que el
    #    rojo parecia un falso positivo. Un diagnostico que senala mal se archiva como ruido.
    invocadas = set()
    quien = {}
    for n in sorted(nombres):
        for k in re.findall(rf"\b{re.escape(n)}\.(\w+)", limpio_todo):
            invocadas.add(k)
            quien.setdefault(k, []).append(n)

    def como(k):
        """La invocacion literal, con TODAS sus ligaduras si son varias."""
        return " / ".join(f"{n}.{k}" for n in dict.fromkeys(quien.get(k, sorted(nombres)[:1])))

    nombre_fichero = ruta.rsplit("/", 1)[-1]
    # ⛔ UN HALLAZGO POR CLAVE, CON SU RAZON MAS ESPECIFICA. La primera version listaba `despuess`
    #    DOS veces —«no declarada» y «ninguna entrada la trae»— porque las dos son ciertas a la vez.
    #    Un gate que repite se lee como ruido y se apaga; el orden de abajo es de mas informativo a
    #    menos, y se queda el primero que case.
    hallazgos = {}

    def anota(k, porque):
        hallazgos.setdefault(k, porque)

    for k in sorted(invocadas - usadas - declaradas):
        anota(k, f"el arnes la INVOCA ({como(k)}), el tipo no la declara y NINGUNA "
                 "entrada la trae: es un typo de invocacion y nada la satisfara jamas")
    for k in sorted(invocadas - declaradas):
        anota(k, f"el arnes la INVOCA ({como(k)}) y el tipo no la declara")
    for k in sorted(usadas - declaradas):
        anota(k, "una ENTRADA la usa y el tipo no la declara")
    # Declarada, puesta por alguna entrada, y que el arnes no lee nunca: gancho muerto.
    # ⛔ AQUI HABIA UNA EXENCION A MANO: `- {"id", "path"}`, con el comentario «el arnes las consume
    #    por otra via». La via era una PLANTILLA (`mgmt-${view.id}-${theme}.png`), y no las veia
    #    porque `sin_prosa` blanqueaba las plantillas enteras. O sea: la exencion no describia una
    #    excepcion del dominio, TAPABA UN AGUJERO DE LA SONDA — y de paso ocultaba el falso negativo
    #    gemelo, que era el caso insignia de este gate. Arreglada la sonda, la exencion sobra y se
    #    retira: una lista de exentos es donde van a morir los defectos que nadie vuelve a mirar.
    for k in sorted((usadas & declaradas) - invocadas):
        anota(k, "las ENTRADAS la traen y el arnes NO la invoca nunca: gancho muerto")

    print(f"check-capture-view-keys: {ruta}")
    print(f"  declaradas en el tipo : {sorted(declaradas)}")
    print(f"  usadas en entradas    : {sorted(usadas)}")
    print(f"  invocadas por el arnes: {sorted(invocadas)}  (ligadura: {sorted(nombres)})")
    if not hallazgos:
        print("  sin hallazgos: tipo, entradas e invocaciones concuerdan")
        return RC_LIMPIO
    for k, porque in sorted(hallazgos.items()):
        # ⛔ CLAVE **Y** FICHERO EN CADA LINEA. Sin el fichero, quien lea el rojo en un gancho con
        #    ciento sesenta patas no sabe donde mirar; el banco protege esta forma con un mutante.
        print(f"  ⛔ `{k}` en {nombre_fichero}: {porque}")
    print(f"  ⇒ {razon_de_existir()}", file=sys.stderr)
    return RC_HALLAZGO


def main(argv):
    if len(argv) > 1:
        return revisa(argv[1])
    rutas = universo()
    if not rutas:
        print("check-capture-view-keys: NO HE PODIDO MIRAR: no encuentro NINGUNA tabla `VIEWS` en "
              "`web/e2e/*.spec.ts`. O han cambiado de sitio o estoy corriendo desde otro "
              "directorio; lo que NO puedo es decir que esta limpio", file=sys.stderr)
        return RC_NO_PUDE_MIRAR
    print(f"check-capture-view-keys: {len(rutas)} tabla(s) VIEWS: {', '.join(rutas)}")
    # ⛔ SE REVISAN TODAS Y GANA LA PEOR: parar en el primer rojo dejaria el resto sin medir, que
    #    es la cortina que este proyecto ya se ha comido (un rojo temprano oculta lo de detras).
    peor = RC_LIMPIO
    for r in rutas:
        rc = revisa(r)
        peor = max(peor, rc) if rc != RC_NO_PUDE_MIRAR and peor != RC_NO_PUDE_MIRAR else RC_NO_PUDE_MIRAR
    return peor


if __name__ == "__main__":
    sys.exit(main(sys.argv))
