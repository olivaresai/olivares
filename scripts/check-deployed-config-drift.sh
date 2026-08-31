#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-deployed-config-drift.sh — ¿lo que el repositorio DECLARA es lo que el worker DESPLEGADO tiene?
#
# ⛔ POR QUÉ EXISTE, medido el 2026-08-17 y no supuesto. `wrangler.jsonc` declara para el entorno de
# sandbox `ENTERPRISE_VERSION = 26.8.0`. El worker **desplegado** contesta `0.0.0`. Nadie comparaba las
# dos cosas, así que la deriva llevaba viva sin fecha conocida.
#
# LO QUE ESO COSTABA, y es concreto: ese mismo día publiqué el primer artefacto de la historia en el
# bucket de sandbox bajo `enterprise/26.8.0/`, leyendo la versión **del repositorio**. Para el worker
# vivo esos bytes son **invisibles**, porque sirve `0.0.0` — el valor exacto que
# `commercial/license-worker/src/release-version.ts` existe para rechazar. La cadena de entrega no
# fallaba: entregaba nada, en silencio.
#
# ES LA MISMA FAMILIA QUE YA NOS MORDIÓ con los secretos puestos desde el panel de Cloudflare, que
# crean una VERSIÓN que nadie despliega y que `wrangler secret list` da por puesta. La regla que sale
# de las dos: **una configuración en el repositorio no es una configuración desplegada, y sólo la
# segunda contesta peticiones.**
#
# ⛔ SU LLAMANTE ES UNA PERSONA O CI, NO EL CARRIL RÁPIDO: consulta la API de Cloudflare, así que
# necesita red y credencial. Sin `CLOUDFLARE_API_TOKEN` sale **2**, nunca 0: no poder mirar no es estar
# limpio.
#
# TRES RESPUESTAS: 0 sin deriva · 1 deriva encontrada, con la tabla · 2 no he podido mirar.
set -uo pipefail

# ⛔ EL ENTORNO DE GIT SE DESINFECTA ANTES DE NADA. Lo pilló `lint:git-env` el 2026-08-18 sobre este
# mismo guion recién escrito: empareja `mktemp -d` con órdenes de git y NO cargaba esta librería, así
# que un `GIT_DIR` heredado —git lo exporta a sus hooks, pero SÓLO desde un worktree ENLAZADO, que es
# como trabajan los cinco carriles— haría que sus llamadas a git apuntasen al repositorio equivocado.
# El trinquete examina 40 miembros de la clase; éste entró rojo el día que nació.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "check-deployed-config-drift: ⛔ NO HE PODIDO MIRAR: no puedo cargar $_olivares_git_env" >&2
	exit 2
}
unset _olivares_git_env

ENTORNO="${1:-sandbox}"
case "$ENTORNO" in
sandbox) SCRIPT=olivares-license-worker-sandbox; ENVKEY=sandbox ;;
production) SCRIPT=olivares-license-worker-production; ENVKEY=production ;;
*)
	echo "check-deployed-config-drift: ⛔ NO HE PODIDO MIRAR: entorno '$ENTORNO' desconocido (sandbox|production)." >&2
	exit 2
	;;
esac

ROOT="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || echo .)}"
CFG="$ROOT/commercial/license-worker/wrangler.jsonc"
[ -f "$CFG" ] || { echo "check-deployed-config-drift: ⛔ NO HE PODIDO MIRAR: no encuentro $CFG." >&2; exit 2; }
[ -n "${CLOUDFLARE_API_TOKEN:-}" ] || { echo "check-deployed-config-drift: ⛔ NO HE PODIDO MIRAR: sin CLOUDFLARE_API_TOKEN." >&2; exit 2; }
command -v curl >/dev/null 2>&1 || { echo "check-deployed-config-drift: ⛔ NO HE PODIDO MIRAR: no encuentro curl." >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "check-deployed-config-drift: ⛔ NO HE PODIDO MIRAR: no encuentro python3." >&2; exit 2; }

API=https://api.cloudflare.com/client/v4
CUENTA="${CLOUDFLARE_ACCOUNT_ID:-}"
if [ -z "$CUENTA" ]; then
	CUENTA="$(curl -sS --max-time 30 -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" "$API/accounts" 2>/dev/null | python3 -c '
import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit(0)
r=d.get("result") if d.get("success") else None
if isinstance(r,list) and len(r)==1: print(r[0].get("id",""))
' 2>/dev/null)"
fi
[ -n "$CUENTA" ] || { echo "check-deployed-config-drift: ⛔ NO HE PODIDO MIRAR: no he resuelto EXACTAMENTE una cuenta." >&2; exit 2; }

# ⛔ LA RESPUESTA VIAJA POR FICHERO, NO POR STDIN, y no es estilo. La primera versión hacía
# `printf … | python3 - "$CFG" <<'PY'`: el heredoc OCUPA stdin con el propio programa, así que
# `json.load(sys.stdin)` no leía la respuesta de la API sino el resto del guion, y el gate contestó
# «respuesta ilegible» sobre una respuesta perfectamente legible. El script y sus datos peleándose por
# el mismo canal es un falso «no he podido mirar», que es el veredicto más caro de fabricar: parece
# prudencia y es un defecto.
TMPD="$(mktemp -d "${TMPDIR:-/tmp}/drift.XXXXXX" 2>/dev/null)" || { echo "check-deployed-config-drift: ⛔ NO HE PODIDO MIRAR: sin directorio temporal." >&2; exit 2; }
trap 'rm -rf "$TMPD"' EXIT
curl -sS --max-time 45 -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
	"$API/accounts/$CUENTA/workers/scripts/$SCRIPT/settings" >"$TMPD/settings.json" 2>/dev/null

salida="$(python3 - "$CFG" "$ENVKEY" "$TMPD/settings.json" <<'PY' 2>/dev/null
import json,sys,re

cfg_path, envkey, settings_path = sys.argv[1], sys.argv[2], sys.argv[3]

# El fichero es JSONC: se quitan los comentarios de linea antes de parsear, respetando las cadenas.
raw = open(cfg_path, encoding="utf-8", errors="replace").read()
out, i, n, in_str, esc = [], 0, len(raw), False, False
while i < n:
    c = raw[i]
    if in_str:
        out.append(c)
        if esc: esc = False
        elif c == "\\": esc = True
        elif c == '"': in_str = False
        i += 1; continue
    if c == '"': in_str = True; out.append(c); i += 1; continue
    if c == "/" and i+1 < n and raw[i+1] == "/":
        while i < n and raw[i] != "\n": i += 1
        continue
    if c == "/" and i+1 < n and raw[i+1] == "*":
        i += 2
        while i+1 < n and not (raw[i] == "*" and raw[i+1] == "/"): i += 1
        i += 2; continue
    out.append(c); i += 1
try:
    cfg = json.loads("".join(out))
except Exception as e:
    print("MIRAR|no he podido parsear wrangler.jsonc: %s" % str(e)[:70]); sys.exit(0)

declarado = ((cfg.get("env") or {}).get(envkey) or {}).get("vars") or {}
if not declarado:
    print("MIRAR|el entorno '%s' no declara vars en wrangler.jsonc: sin nada que comparar, no se dictamina" % envkey); sys.exit(0)

try:
    d = json.load(open(settings_path, encoding="utf-8"))
except Exception:
    print("MIRAR|respuesta ilegible de la API de settings"); sys.exit(0)
if not d.get("success"):
    print("MIRAR|la API dijo no: %s" % ([e.get("message") for e in d.get("errors",[])][:1])); sys.exit(0)

b = (d.get("result") or {}).get("bindings") or []
vivo = {x.get("name"): x.get("text") for x in b if x.get("type") == "plain_text"}

# CONTROL POSITIVO. Cero variables leidas saldria "sin deriva" por vacuidad, que es el falso verde
# caro: significaria que no hemos leido la configuracion, no que coincida.
if len(vivo) < 5:
    print("MIRAR|solo %d variable(s) legibles del worker: eso no es una configuracion, es una lectura rota" % len(vivo)); sys.exit(0)

deriva = []
for k, v in sorted(declarado.items()):
    if not isinstance(v, str):   # solo se comparan cadenas: un binding no-texto no vive en plain_text
        continue
    if k not in vivo:
        deriva.append((k, v, "<AUSENTE en el despliegue>"))
    elif vivo[k] != v:
        deriva.append((k, v, vivo[k]))

print("DATOS|%d|%d|%s" % (len(declarado), len(vivo), json.dumps(deriva)))
PY
)"

[ -n "$salida" ] || { echo "check-deployed-config-drift: ⛔ NO HE PODIDO MIRAR: el comparador no produjo veredicto." >&2; exit 2; }

case "$salida" in
MIRAR\|*)
	echo "check-deployed-config-drift: ⛔ NO HE PODIDO MIRAR: ${salida#MIRAR|}" >&2
	exit 2
	;;
esac

n_dec="$(printf '%s' "$salida" | cut -d'|' -f2)"
n_vivo="$(printf '%s' "$salida" | cut -d'|' -f3)"
deriva_json="$(printf '%s' "$salida" | cut -d'|' -f4-)"

n_der="$(printf '%s' "$deriva_json" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))' 2>/dev/null || echo "")"
[ -n "$n_der" ] || { echo "check-deployed-config-drift: ⛔ NO HE PODIDO MIRAR: no he podido contar la deriva." >&2; exit 2; }

echo "check-deployed-config-drift: $ENTORNO · declaradas en wrangler.jsonc=$n_dec · legibles en el despliegue=$n_vivo · DERIVA=$n_der"

if [ "$n_der" -eq 0 ]; then
	echo "check-deployed-config-drift: OK — lo desplegado coincide con lo declarado."
	exit 0
fi

printf '%s' "$deriva_json" | python3 -c '
import json,sys
for k,dec,viv in json.load(sys.stdin):
    print("  ⛔ %-26s repositorio=%-14r desplegado=%r" % (k, dec, viv))
' 2>/dev/null
echo "check-deployed-config-drift: ⛔ DERIVA — lo que contesta peticiones NO es lo que dice el repositorio." >&2
echo "  Una configuración en el repositorio no es una configuración desplegada. Sólo la segunda sirve" >&2
echo "  tráfico, y la primera es la que se lee al decidir qué publicar: así es como un artefacto" >&2
echo "  correcto se vuelve invisible sin que nada falle." >&2
echo "  repair: desplegar el entorno, o corregir wrangler.jsonc si el valor vivo es el bueno." >&2
exit 1
