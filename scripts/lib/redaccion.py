# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Redaccion en la FRONTERA DE SALIDA, compartida por los guiones de sembrado.

⛔ POR QUE ESTO ES UNA LIBRERIA Y NO UNA COPIA MAS. Esta funcion ha nacido dos veces en esta casa,
   y las dos veces se le escapo algo distinto:

     · en `seed-adoption-otlp.py`, por conocer FORMAS y no ESTRUCTURA: una credencial que no casaba
       ningun regex salia entera por dos rutas (the reviewer, A-01). Se curo reconociendo el userinfo por
       POSICION —lo que va entre `//` y el primer `@`— y aplicandolo al TEXTO QUE SALE, no solo a
       las URLs que el guion recordaba: el cuerpo de un 400 lo escribe el SERVIDOR y puede traer una
       credencial que nunca paso por el guion.
     · en `seed-estate-volume.py`, por no existir: `_pide` devolvia el cuerpo HTTP crudo y `salir` lo
       imprimia, asi que un receptor que reflejara la cabecera `Authorization` filtraba el token
       (the reviewer, A-05).

   Dos guiones, el mismo fallo, dos curas separadas es como se pierde la segunda. Aqui hay UNA.

⛔ Y LA REGLA QUE LA HACE UTIL: se aplica en la SALIDA, no en cada punto de llamada. Enunciar bien
   el principio y aplicarlo en dos sitios es exactamente como se fugo la primera vez.
"""
from __future__ import annotations

import re

# Un userinfo es una POSICION en la cadena. Reconocerlo asi es lo unico que cubre credenciales que
# nadie ha visto todavia — un regex de formas solo tapa las que ya conocemos.
_RX_USERINFO = re.compile(r"//([^/\s@]{1,512})@")
# Respaldo por forma, para credenciales sueltas que no vienen dentro de una URL.
_FORMAS = (
    r"\bsk-[A-Za-z0-9_-]{16,}", r"\bsk_[A-Za-z0-9_]{16,}", r"\bAKIA[0-9A-Z]{16}\b",
    r"\bwhsec_[A-Za-z0-9+/=_-]{12,}", r"\bgh[pousr]_[A-Za-z0-9]{16,}",
    r"\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}",
)
# ⛔ CREDENCIALES EN CABECERA, POR ESTRUCTURA Y CON CUALQUIER VALOR (the reviewer, A-01 sobre
#    `817bc4a4d`). Un receptor que conteste 400 reflejando `Authorization: Bearer <lo-que-sea>`
#    devuelve el secreto, y ni los valores recordados ni el userinfo posicional ni los regex de
#    formas conocidas lo tapan: es una TERCERA frontera, la de una cabecera estructurada. Se
#    reconoce por el NOMBRE de la cabecera —que si es un conjunto cerrado y conocido— y se tapa el
#    valor entero, sin mirar a que se parece.
#
# ⛔ TRES ENTRADAS, NO CINCO, Y LO DECIDIO UNA MEDIDA. Estaban las cinco —`authorization`,
#    `proxy-authorization`, `x-api-key`, `x-auth-token`, `api-key`— y al exigir un mutante POR
#    ENTRADA (the reviewer, A-03) dos resultaron REDUNDANTES: con `\b`, `authorization` ya casa dentro
#    de `proxy-authorization` —la frontera cae entre el guion y la `a`— y `api-key` casa dentro de
#    `x-api-key`. Sus mutantes no podian fugar porque no habia nada que quitar. Medido contra las
#    CINCO formas: las tres que quedan las tapan todas. Una entrada que ningun mutante puede matar
#    es una entrada que no hace falta.
_RX_CABECERA = re.compile(
    r"(?i)\b(authorization|api-key|auth-token)"
    r"(\s*[:=]\s*)(?:(bearer|basic|token)\s+)?(\S+)")
_RX_BEARER = re.compile(r"(?i)\b(bearer)\s+(\S+)")


class Redactor:
    """Recuerda lo sensible que se le declare y lo tapa en cualquier texto que salga."""

    def __init__(self):
        self._vistos: set[str] = set()

    def recuerda(self, *piezas: str) -> None:
        """Declara secretos CONOCIDOS —un token, una contrasena— para taparlos aunque el texto que
        los repita venga de fuera. Es lo que cierra el caso del cuerpo que refleja `Authorization`:
        el guion SI sabe su token, asi que no necesita reconocerlo por su forma."""
        for p in piezas:
            if p and len(str(p)) >= 8:
                self._vistos.add(str(p))

    def recuerda_url(self, url: str, con_ruta: bool = False) -> None:
        """Guarda lo sensible de una URL: userinfo y query, y si no es una URL, la cadena.

        ⛔ LIMITE DECLARADO, porque estaba sin declarar y eso es lo que lo hacia peligroso: **un
           secreto SIN FORMA reconocible dentro de un SEGMENTO DE RUTA no queda cubierto**. Medido:
           con `https://host/v1/agents/Xk9Qm2Lp7Rt4Vb1Nz6Yw/run`, el userinfo y la query salen
           tapados y **la ruta sale entera**. Quien lea «guarda lo sensible de una URL» supondria lo
           contrario, y una frontera que se lee mas ancha de lo que es engaña mas que una estrecha.

        ⛔ Y NO SE TAPA LA RUTA POR HEURISTICA, que es lo que probe primero. Un filtro razonable
           —16+ caracteres con letras y digitos— respeta TODAS las rutas reales de estos guiones
           (`v1`, `adoption`, `guardian`, `rules`…: cero falsos positivos) pero tambien tapa
           `agent-claude-invoice-11`, que es el MARCADOR con el que los mensajes reconocen una fila
           sembrada. Taparlo no protege nada y deja los diagnosticos sin el dato que los hace utiles.
           Un secreto en la ruta es indistinguible de un id de recurso sin saber la API.

           Por eso la cobertura es EXPLICITA: `con_ruta=True` la pide quien SABE que su ruta lleva
           un secreto. Elegir por el llamante seria adivinar; que lo diga el que sabe, no cuesta
           nada y no degrada el resto.
        """
        if not url:
            return
        if con_ruta:
            try:
                from urllib.parse import unquote, urlsplit
                for seg in (urlsplit(url).path or "").split("/"):
                    if seg and len(seg) >= 4:
                        self._vistos.add(seg)
                        llano = unquote(seg)
                        if llano != seg and len(llano) >= 4:
                            self._vistos.add(llano)
            except Exception:
                pass
        try:
            from urllib.parse import unquote, urlsplit
            u = urlsplit(url)
            for pieza in (u.username, u.password, u.query):
                if not pieza or len(pieza) < 4:
                    continue
                # ⛔ SE GUARDAN LAS DOS FORMAS, LA CODIFICADA Y LA DECODIFICADA. `urlsplit` devuelve
                #    lo que hay EN la URL —percent-encoded—, y una excepcion lo suele traer ya
                #    DECODIFICADO: la comparacion literal no casa y la credencial sale entera.
                #    MEDIDO con `sk-con/barra+y=signos`, que en la URL viaja como
                #    `sk-con%2Fbarra%2By%3Dsignos`: el texto con la forma codificada salia tapado y
                #    el de la forma decodificada FUGABA. Recordar una sola de las dos es recordar la
                #    que casualmente no aparece.
                self._vistos.add(pieza)
                try:
                    plano = unquote(pieza)
                except Exception:
                    plano = pieza
                if plano != pieza and len(plano) >= 4:
                    self._vistos.add(plano)
        except Exception:
            self._vistos.add(url)
        if "://" not in url and "//" not in url and len(url) >= 8:
            self._vistos.add(url)

    def __call__(self, texto: str) -> str:
        fuera = str(texto)
        for pieza in sorted(self._vistos, key=len, reverse=True):
            fuera = fuera.replace(pieza, "<oculto>")
        fuera = _RX_USERINFO.sub("//<oculto>@", fuera)
        # El orden importa: primero la cabecera entera (que puede llevar `Bearer`), luego el
        # `Bearer` suelto que aparezca sin cabecera delante.
        fuera = _RX_CABECERA.sub(
            lambda m: m.group(1) + m.group(2) + ((m.group(3) + " ") if m.group(3) else "") + "<oculto>",
            fuera)
        fuera = _RX_BEARER.sub(lambda m: m.group(1) + " <oculto>", fuera)
        for rx in _FORMAS:
            fuera = re.sub(rx, "<oculto>", fuera)
        return fuera


# ── La frontera tambien es de TRANSPORTE, no solo de texto ────────────────────────────────────
_ABRIDOR = None


def abre(pet, timeout=30):
    """Como `urllib.request.urlopen`, pero SIN SEGUIR REDIRECCIONES.

    ⛔ POR QUE, Y ESTA MEDIDO EN ESTA CASA, no deducido. `urlopen` con el abridor por defecto sigue
       los 30x y **copia TODAS las cabeceras al nuevo destino**, incluido `Authorization`. Montado
       un servidor que contesta 302 hacia otro puerto, el segundo origen recibio literalmente
       `Bearer <el token>`. Es decir: si la consola contesta una redireccion —por una mala
       configuracion, un proxy delante, o un `Location` que alguien controle— el token de operador
       sale del edificio sin que ningun guion haya hecho nada mal.

       ⛔ SE REHUSA EL CAMBIO DE ORIGEN, NO LA REDIRECCION. Mi primera version rechazaba TODOS los
       30x y eso era una regresion: un `Location: /final` del MISMO origen —que la consola puede
       emitir por una ruta canonica o una barra final— daba 200 antes y pasaba a fallar. Lo que
       pone la credencial fuera del edificio es el CAMBIO de esquema/host/puerto, no el salto en si.
       Asi que se compara el origen EFECTIVO y se delega en el manejador estandar cuando coincide;
       si cambia, se rehusa y se dice adonde queria mandarnos — con el `Location` pasado por el
       redactor, porque tampoco ese destino tiene por que ser publicable.

       Es la misma familia que el resto de este fichero: una credencial que cruza a un origen que no
       es el suyo es una fuga por la FRONTERA, aunque no se imprima nunca.
    """
    import urllib.error
    import urllib.request

    import urllib.parse

    global _ABRIDOR
    if _ABRIDOR is None:
        def _origen(u):
            """(esquema, host, puerto EFECTIVO). El puerto por defecto se resuelve a proposito:
            `http://h/x` y `http://h:80/x` son el MISMO origen y compararlos como texto diria que
            no."""
            t = urllib.parse.urlsplit(u)
            return (t.scheme.lower(), (t.hostname or "").lower(),
                    t.port or {"https": 443, "http": 80}.get(t.scheme.lower(), 0))

        class _SoloMismoOrigen(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, req, fp, code, msg, headers, newurl):
                # `urljoin` porque un `Location` puede ser RELATIVO (`/final`), y compararlo
                # crudo contra la URL absoluta diria «otro origen» siempre.
                destino = urllib.parse.urljoin(req.full_url, str(newurl))
                if _origen(destino) == _origen(req.full_url):
                    return super().redirect_request(req, fp, code, msg, headers, destino)
                raise urllib.error.HTTPError(
                    req.full_url, code,
                    "redireccion REHUSADA (%s): seguirla mandaria la cabecera Authorization a OTRO "
                    "origen. Destino: %s" % (msg, Redactor()(destino)),
                    headers, fp)

        _ABRIDOR = urllib.request.build_opener(_SoloMismoOrigen)
    return _ABRIDOR.open(pet, timeout=timeout)


def instala_excepthook(redactor, etiqueta="olivares"):
    """Hace que TAMBIEN una excepcion no capturada cruce la frontera.

    ⛔ POR QUE HACE FALTA, y es un agujero que se me escapo dos veces en dos guiones distintos
       (the reviewer: adopcion v6 y sembrador v2). `di()` y `salir()` redactan lo que pasa por ellas, y
       una excepcion que nadie captura **no pasa por ninguna**: sale por el traceback que imprime
       el interprete, con la URL entera y su credencial dentro. La redaccion mas cuidada del mundo
       no tapa lo que sale por un camino que no la cruza.

       Se instala en `main`, no en el import: un guion que se importe como modulo —los bancos lo
       hacen— no debe secuestrar el `excepthook` del proceso que lo importa.
    """
    import sys
    import traceback as _tb

    anterior = sys.excepthook

    def _hook(tipo, valor, tb):
        if issubclass(tipo, (KeyboardInterrupt, SystemExit)):
            anterior(tipo, valor, tb)
            return
        texto = "".join(_tb.format_exception(tipo, valor, tb))
        print(redactor(f"{etiqueta}: ⛔ NO HE PODIDO MIRAR: excepcion no capturada\n{texto}"),
              file=sys.stderr, end="")

    sys.excepthook = _hook
