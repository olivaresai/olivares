#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-cli-registries.sh — los REGISTROS del CLI están completos. Corre las TRES pruebas que ya lo
# demuestran, no copias suyas.
#
#   · un comando VISIBLE aparece en la ayuda por temas          (commandGroups)
#   · un grupo de ayuda nombra un comando que existe            (la dirección inversa)
#   · toda clave de entorno que este paquete LEE está registrada (config_registry.go)
#
# Son la misma clase de defecto: algo existe y no aparece en el registro que lo hace encontrable o
# conocido. Y las tres pruebas YA EXISTÍAN.
#
# ⛔ POR QUÉ EXISTE, y no es «falta un gate»: LOS DOS GATES YA EXISTÍAN Y NO PARARON NADA.
#
#    `cmd_s438_test.go` tiene `TestEveryVisibleCommandIsGrouped` con el mensaje exacto —«visible
#    command %q has no help group — add it to commandGroups»— y `commandgroups_test.go` tiene la
#    dirección inversa. Aun así, el 2026-08-19 `grok-hook` aterrizó en `main` VISIBLE y SIN grupo,
#    y lo cazó una persona al integrar.
#
#    La razón es la misma que ya medí con `connectors/grok`, que llegó cableado y sin ofrecer: a
#    esas pruebas **sólo las alcanza `go test` sobre `cmd/olivares`**, que es el gate PESADO. Un
#    carril de rama no las corre nunca. Una prueba correcta que no corre en el carril donde se
#    empuja no es una guarda: es documentación ejecutable que nadie ejecuta.
#
# ⛔⛔ Y ES LA TERCERA VEZ, QUE ES LO QUE CONVIERTE ESTO EN UN PATRÓN Y NO EN TRES DESCUIDOS. En el
#     MISMO PR aterrizaron además **siete claves de entorno leídas y ninguna registrada** —seis en
#     `cmd_grokhook.go` y `OLIVARES_GROK_HOOK_PEP_CONFIG` en `grokhookpepserver.go`—, y también hay
#     prueba para eso desde antes: `TestConfigRegistryCoversEveryEnvKeyThisPackageReads`, cuyo
#     propio comentario recuerda que el gate «was green while four honored keys were unregistered»
#. Tres registros, tres pruebas correctas, y ninguna en el carril donde se empuja.
#
#     Por eso este guion no se llama `command-groups`: el nombre habría envejecido con la tercera.
#
# ⛔ Y EL CONTROL POSITIVO ES LA MITAD QUE VALE: `go test -run` con un patrón que no casa nada sale
#    **0**. Sin exigir ver el `--- PASS:` de cada prueba POR SU NOMBRE, este gate saldría verde para
#    siempre en cuanto alguien renombre una — que es exactamente el fallo que vino a impedir.
#
# Salida: 0 limpio · 1 un comando sin grupo o un grupo sin comando · 2 no se ha podido comprobar.
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ/cmd/olivares" 2>/dev/null || {
	echo "check-cli-registries: ⛔ NO HE PODIDO MIRAR: no existe $RAIZ/cmd/olivares" >&2
	exit 2
}

PRUEBAS="TestEveryVisibleCommandIsGrouped|TestCadaGrupoDeAyudaNombraUnComandoQueExiste|TestConfigRegistryCoversEveryEnvKeyThisPackageReads"
salida="$(go test -run "$PRUEBAS" -count=1 -v . 2>&1)"
rc=$?

# ── El control positivo, ANTES de mirar el rc ────────────────────────────────────────────
# Cada prueba, por su nombre. Un `-run` que no casa nada, un paquete que no compila con el filtro
# puesto, o una prueba renombrada salen todos por aquí y NUNCA por 0.
#
# La comprobación va con here-string y no con tubería: bajo `pipefail`, `printf | grep -q` devuelve
# **141 EN ÉXITO** porque `grep -q` cierra su entrada al primer acierto y `printf` recibe SIGPIPE.
# El caso que ACIERTA se leería como fallo. Es la trampa que `lint:sigpipe-booleans` vigila.
faltan=""
for t in TestEveryVisibleCommandIsGrouped TestCadaGrupoDeAyudaNombraUnComandoQueExiste TestConfigRegistryCoversEveryEnvKeyThisPackageReads; do
	grep -qE "^(--- )?(PASS|FAIL): +${t}\b" <<<"$salida" || faltan="${faltan} ${t}"
done
if [ -n "$faltan" ]; then
	echo "check-cli-registries: ⛔ NO HE PODIDO MIRAR: estas pruebas no llegaron a ejecutarse:${faltan}" >&2
	echo "                      Un 'go test -run' que no casa nada sale 0, así que esto NO es un pase." >&2
	echo "                      ¿Las han renombrado o movido de paquete? Actualiza PRUEBAS aquí." >&2
	printf '%s\n' "$salida" | tail -12 | sed 's/^/  /' >&2
	exit 2
fi

if [ "$rc" -ne 0 ]; then
	echo "check-cli-registries: ⛔ un registro del CLI está incompleto:" >&2
	printf '%s\n' "$salida" | grep -E "^\s+--- FAIL|_test\.go:|help group|no existe" | head -12 | sed 's/^/  /' >&2
	echo "                      Grupos de ayuda: \`commandGroups\` en cmd/olivares/main.go — un comando que" >&2
	echo "                      existe y no se encuentra es, para el usuario, un comando que no está." >&2
	echo "                      Claves de entorno: cmd/olivares/config_registry.go — una clave que se LEE" >&2
	echo "                      y no se declara no sale en \`config effective\` ni se puede redactar." >&2
	exit 1
fi

echo "check-cli-registries: OK — los registros del CLI están completos: grupos de ayuda y claves de entorno (3 prueba(s) ejecutadas)."
