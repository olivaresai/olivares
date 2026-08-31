#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-prose-gates.sh — los gates BARATOS que la prosa rompe, en un solo comando.
#
# ⛔ POR QUE EXISTE, y es un agujero de MI propio flujo, no del hook. El gate completo en `main`
# es aritmetica imposible (~150 min mientras `main` se mueve cada 2,3), asi que el bypass
# sancionado es `git push --no-verify`. Pero `--no-verify` no se salta solo el gate pesado: se
# salta TAMBIEN los fast-lints, y entre ellos `lint:export`, que es el bloqueante n1 de
# publicacion.
#
# Consecuencia medida el 2026-08-19: rompi `lint:export` DOS VECES en la misma noche con la misma
# palabra —prosa en espanol dentro de ficheros exportables— y la segunda tuvo que reportarla otro
# carril. Entre una y otra pasaron tres horas con el bloqueante de publicacion en rojo.
#
# Esto NO sustituye al carril rapido: es lo que se corre antes de un push que anade comentarios,
# y cuesta segundos en vez de diez minutos.
#
# ⛔ Y NO ES UN CONTROL. Lo senalo otro carril contrastando esta misma herramienta, y tiene
# razon: nada obliga a correrlo, es una COSTUMBRE — exactamente el modo de fallo de la enfermedad
# que dice curar. La prueba esta tres parrafos mas arriba: se me olvido dos veces en una noche, y
# este guion se olvida igual de facil. Correrlo es mejor que no tenerlo; contar con el como si
# fuese una barrera es el error. La barrera de verdad tendria que ser CI con disparador
# automatico sobre `lint:export`, que es la mitad que INT-08 esta tocando. `--no-verify` se salta
# el hook entero por diseno de git: no hay forma de que el hook se autoinvoque.
#
# ⛔ QUE NO CUBRE, dicho aqui para que su verde no se lea como cobertura de la clase: la prosa
# tambien rompe `lint:docs-honesty` (106 s) y `lint:public-counts`, y estan FUERA a proposito por
# coste — este guion existe para costar segundos. Un gate que promete una clase y cubre un
# subconjunto se lee como cobertura, asi que aqui va la lista de lo que deliberadamente no mira.
set -uo pipefail
cd "$(dirname "$0")/.."
rc=0
nopude=0
for t in lint:export lint:inbox lint:spdx lint:actions; do
	out="$(timeout 900 task "$t" 2>&1)"
	trc=$?
	# ⛔ TRES RESPUESTAS, Y ESTE BUCLE LAS COLAPSABA. La primera version hacia
	# `if out="$(task "$t")"`, asi que CUALQUIER codigo distinto de 0 salia como rojo. Sus propios
	# sujetos tienen caminos de exit 2 —export-public.sh, check-spdx.sh y check-export-closure.sh
	# tienen dos cada uno—, de modo que un arbol que el escrubador NO PUDO LEER salia identico a
	# una fuga real, y la linea final mandaba «arreglalo ANTES del push» cuando no habia nada que
	# arreglar. Separar limpio/roto/no-he-podido-mirar es lo que este arbol lleva meses haciendo en
	# cada gate; el envoltorio que los junta no puede ser el que lo deshaga.
	#
	# `task` aplasta todo fallo a 201, asi que un 2 propio solo se ve con `task -x`; por eso el
	# reintento, y solo cuando hace falta.
	if [ "$trc" -eq 201 ]; then
		real="$(timeout 900 task -x "$t" 2>&1)"
		case "$real" in *"exit status 2"*) trc=2 ;; esac
	fi
	case "$trc" in
	0) printf '  %-22s OK\n' "$t" ;;
	2)
		printf '  %-22s ⚠ NO HE PODIDO MIRAR\n' "$t"
		printf '%s\n' "$out" | tail -6 | sed 's/^/      /'
		nopude=1
		;;
	*)
		printf '  %-22s ⛔\n' "$t"
		printf '%s\n' "$out" | tail -6 | sed 's/^/      /'
		rc=1
		;;
	esac
done

if [ "$rc" -ne 0 ]; then
	echo "check-prose-gates: ⛔ arreglalo ANTES del push; --no-verify no lo mira por ti" >&2
	exit 1
fi
if [ "$nopude" -ne 0 ]; then
	echo "check-prose-gates: ⚠ NO HE PODIDO MIRAR uno o mas gates — NO hay nada que arreglar," >&2
	echo "  hay algo que no se ha medido, y eso NO es lo mismo que publicable." >&2
	exit 2
fi
echo "check-prose-gates: OK — la prosa de este arbol es publicable (no cubre docs-honesty ni public-counts)"
exit 0
