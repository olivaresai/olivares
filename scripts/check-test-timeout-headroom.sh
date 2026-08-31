#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-test-timeout-headroom.sh — el tope por binario es un ACANTILADO; esto lo convierte en cuesta.
#
# CENSUS-SUBJECT: external
#   Su sujeto es una CORRIDA DE CI (un log), no el árbol. Pasar sobre un repo vacío sería correcto;
#   lo que NO puede hacer es pasar sin log, y por eso sin duraciones responde NO_HE_PODIDO_MIRAR.
#
# WHY THIS EXISTS, medido el 2026-08-19 y con dos precedentes del MISMO fallo.
#
# `go test -timeout N` mata el binario del paquete al llegar a N. El pánico que imprime nombra el
# test EN VUELO, que casi nunca es el culpable: el 2026-08-19, en la corrida 32233087960,
# modules/compliance murió a los 45m00 nombrando un test que llevaba **22 s**, y modules/governance
# nombrando uno de **3 s**. Ninguno colgaba. Lo que se agotó fue el PRESUPUESTO DEL PAQUETE.
#
# ⛔ Y EL MODO DE FALLO NO ES EL ROJO: ES EL VERDE DE LA VÍSPERA. Un paquete a 44 de 45 minutos
# pasa, y pasa EN SILENCIO. No hay ninguna señal entre «cómodo» y «muerto». Medido en el job
# race-modules de esa noche: 54,0 → 64,4 → 57,9 → 58,5 → 66,3 → **79,0 min**, verde hasta el último.
#
# Este repositorio YA sabía todo esto, y ahí está el argumento entero. El comentario de
# `test:race-hot:modules` en el Taskfile dice, del 2026-07-25:
#
#     modules/governance measured 1120s (18m41s) […] i.e. 93% of a 20m per-binary cap, so the
#     cap was one slow runner away from failing a passing suite.
#
# Alguien calculó ese margen A MANO, lo escribió en PROSA, y nadie lo volvió a mirar. Veinticinco
# días después ese mismo paquete pasa de 45 minutos —2,4×— y el tope se había subido de 20m a 45m
# esa misma madrugada por tres paquetes muertos a 1200,4 s exactos. **Subir el tope ya se probó:
# compró horas.** Un número en prosa envejece en silencio; éste envejeció 2,4×.
#
# ⇒ Lo que faltaba no era un tope mayor: era un instrumento que vigile el MARGEN.
#
# EL TOPE NO SE ESCRIBE AQUÍ, SE LEE DEL LOG. Es la decisión de diseño que sostiene lo demás: el
# cap sale de la misma línea `go test … -timeout 45m …` que produjo las duraciones, por JOB. Así
# este gate no puede discrepar de la realidad que mide, y cuando alguien vuelva a subir el tope
# —que volverá— el umbral se mueve con él sin que nadie tenga que acordarse. Un cap escrito a mano
# aquí se habría podrido exactamente igual que el de la prosa que este fichero viene a corregir.
#
# LAS TRES RESPUESTAS
#   LIMPIO (0)             todos los paquetes por debajo del umbral de margen
#   ROTO (1)               al menos uno lo cruza — SE NOMBRAN, con su porcentaje
#   NO_HE_PODIDO_MIRAR (2) sin log, sin duraciones, o un job con duraciones y SIN cap legible
#
# El tercer caso incluye deliberadamente «un job cuyo cap no sé leer». Saltárselo dejaría un verde
# que no cubre ese job, que es la clase de ceguera que el canon §0-COBERTURA prohíbe: un gate dice
# lo que su DESCUBRIMIENTO alcanza, no lo que comprueba.
#
# USO
#   scripts/check-test-timeout-headroom.sh <fichero-log>   # offline, determinista (lo que usa la batería)
#   scripts/check-test-timeout-headroom.sh --fetch [run-id] # baja el log del último mainline-ci
#
# UMBRAL: OLIVARES_HEADROOM_PCT (por defecto 75). Con el cap de 45m son 33m45s. Sobre los datos
# del 2026-08-19 habría nombrado eventing (36,1 min = 80%) ANTES de que reventara, y sobre los del
# 2026-07-25 habría nombrado governance al 93%.

set -euo pipefail

UMBRAL="${OLIVARES_HEADROOM_PCT:-75}"

case "${1:-}" in
  --fetch)
    run="${2:-}"
    if [ -z "$run" ]; then
      run=$(gh run list --workflow mainline-ci.yml --limit 1 --json databaseId -q '.[0].databaseId' 2>/dev/null || true)
    fi
    if [ -z "$run" ]; then
      echo "NO_HE_PODIDO_MIRAR — no hay ninguna corrida de mainline-ci que leer"
      exit 2
    fi
    LOG=$(mktemp "${TMPDIR:-/tmp}/headroom-XXXXXX")
    trap 'rm -f "$LOG"' EXIT
    if ! gh run view "$run" --log > "$LOG" 2>/dev/null; then
      echo "NO_HE_PODIDO_MIRAR — no he podido descargar el log de la corrida $run"
      exit 2
    fi
    echo "corrida $run"
    ;;
  "")
    echo "NO_HE_PODIDO_MIRAR — falta el fichero de log (o --fetch)"
    exit 2
    ;;
  *)
    LOG="$1"
    if [ ! -r "$LOG" ]; then
      echo "NO_HE_PODIDO_MIRAR — no puedo leer '$LOG'"
      exit 2
    fi
    ;;
esac

# Un solo recorrido: cap por job, duraciones por job, y el veredicto.
# El log de `gh run view --log` viene como  <job>\t<paso>\t<timestamp> <contenido>, y el CONTENIDO
# lleva sus propios tabuladores (`ok\tpaquete\t1.2s`), así que se separa el prefijo por posición y
# el resto se trata como texto — partir todo por \t mezclaría las dos capas.
salida=$(LC_ALL=C awk -v umbral="$UMBRAL" '
  function segundos(txt,   n, u) {
    # 45m | 2700s | 1h30m0s → segundos. Devuelve -1 si no lo entiende.
    if (txt ~ /^[0-9]+h[0-9]+m[0-9.]+s$/) { split(txt, p, /[hms]/); return p[1]*3600 + p[2]*60 + p[3] }
    if (txt ~ /^[0-9.]+h$/) { n = txt; sub(/h$/, "", n); return n * 3600 }
    if (txt ~ /^[0-9.]+m$/) { n = txt; sub(/m$/, "", n); return n * 60 }
    if (txt ~ /^[0-9.]+s$/) { n = txt; sub(/s$/, "", n); return n + 0 }
    return -1
  }
  {
    linea = $0
    job = ""
    # prefijo: dos campos separados por tabulador antes del contenido
    if (match(linea, /\t/)) {
      job = substr(linea, 1, RSTART - 1)
      resto = substr(linea, RSTART + 1)
      if (match(resto, /\t/)) resto = substr(resto, RSTART + 1)
    } else {
      resto = linea
    }
    if (job == "") job = "(sin-job)"

    # 0) INVOCACIONES de go test, para poder distinguir «sin tope» de «no lo veo».
    # `go test` sin -timeout NO es un tope desconocido: es el DEFECTO DOCUMENTADO de Go, 10m.
    # Medido el 2026-08-19 sobre la corrida 32233087960: `examples` corre
    # `go test ./... && ./scripts/check-boundary.sh` y produce 3 duraciones, asi que su tope es
    # 600s y es deducible. `control-plane` MEZCLA invocaciones con y sin, y ahi un numero por job
    # no es defendible. `fuzz` tiene duraciones y NINGUNA orden en el log: esa ceguera es real.
    # Tres situaciones distintas que antes daban la misma respuesta.
    # ⛔ EMPIEZA por `go test`, no lo CONTIENE. Medido el 2026-08-19 y es un defecto que estuvo
    # publicado: el job `examples` imprime
    #   «next: cd …/.examples-tmp/bring-your-own-… && go test ./... && ./scripts/check-boundary.sh»
    # que es una INSTRUCCION AL LECTOR, no una orden ejecutada. Con `contiene`, el gate derivaba
    # un tope de 600s a partir de PROSA y lo presentaba como dato — exactamente lo que un gate
    # que mide holgura no puede hacer. Un tope inventado es peor que ninguno: ninguno se declara.
    contenido = resto
    # SIN llaves de repeticion: el awk de esta caja no aplica expresiones de intervalo y `{4}`
    # se quedaba sin efecto EN SILENCIO — la normalizacion no hacia nada y la bateria cayo a
    # 22/5 sin que el patron pareciera el culpable. Medido el 2026-08-19.
    sub(/^[0-9-]+T[0-9:.]+Z[ \t]*/, "", contenido)                      # sello del runner
    sub(/^##\[group\]Run[ \t]+/, "", contenido)                        # el eco del propio Actions
    sub(/^\+[ \t]+/, "", contenido)                                    # `set -x`
    sub(/^[ \t]+/, "", contenido)
    # Prefijo de entorno: `GOWORK=off go test ./...` es una invocacion, no prosa. Los smokes de
    # examples usan exactamente esa forma. Se quitan los pares NOMBRE=valor y un `env` inicial,
    # en bucle, porque puede haber varios. `sub` sin `{n}` por la misma razon que arriba.
    while (contenido ~ /^env[ \t]/ || contenido ~ /^[A-Za-z_][A-Za-z_0-9]*=[^ \t]*[ \t]/) {
      sub(/^env[ \t]+/, "", contenido)
      sub(/^[A-Za-z_][A-Za-z_0-9]*=[^ \t]*[ \t]+/, "", contenido)
    }
    if (contenido ~ /^go test([ \t]|$)/ && contenido !~ /-timeout[ =]/) {
      # Abre un TRAMO cuyo tope es el defecto documentado de Go. Por INVOCACION, no por job:
      # `control-plane` mezcla ordenes con y sin -timeout, y darle un solo numero al job
      # inventaria holgura para la mitad de sus paquetes o alarma para la otra mitad.
      cap_actual[job] = 600
      derivado_actual[job] = 1
      jobs_derivados[job] = 1
    }

    # 1) el cap, de la propia linea de comando que produjo las duraciones
    if (match(resto, /-timeout[ =][0-9hms.]+/)) {
      t = substr(resto, RSTART, RLENGTH)
      sub(/^-timeout[ =]/, "", t)
      s = segundos(t)
      if (s > 0) {
        # Abre un tramo NUEVO. Los paquetes de aqui en adelante, en este job, se miden contra
        # ESTE tope y no contra el minimo de todo el job.
        cap_actual[job] = s
        derivado_actual[job] = 0
      }
      next
    }

    # 2) las duraciones por paquete
    if (match(resto, /(^|[^A-Za-z])(ok|FAIL)[ \t]+[^ \t]+[ \t]+[0-9.]+s([ \t]|$)/)) {
      trozo = substr(resto, RSTART, RLENGTH)
      nf = split(trozo, c, /[ \t]+/)
      # c[] puede empezar con basura si el match arranco en el separador
      for (i = 1; i <= nf; i++) if (c[i] == "ok" || c[i] == "FAIL") break
      if (i + 2 > nf) next
      estado = c[i]; paquete = c[i+1]; dur = c[i+2]
      if (paquete !~ /\//) next
      sub(/s$/, "", dur)
      clave = job "|" paquete
      if (!(clave in visto) || dur + 0 > tiempo[clave]) {
        visto[clave] = 1; tiempo[clave] = dur + 0; estado_de[clave] = estado; job_de[clave] = job
        # El tope VIGENTE cuando se imprimio esta duracion, capturado aqui y no en END.
        if (job in cap_actual) { cap_de[clave] = cap_actual[job]; deriv_de[clave] = derivado_actual[job] }
        else if (clave in cap_de) { delete cap_de[clave] }
      }
    }
  }
  END {
    total = 0; sin_cap = 0; peor = 0
    for (clave in visto) {
      total++
      j = job_de[clave]
      # MEZCLA: el job tiene un -timeout explicito Y ademas una invocacion sin el. El cap es por
      # INVOCACION y las duraciones son por PAQUETE, asi que atribuir uno de los dos al job entero
      # inventaria holgura o inventaria alarma. Se dice, no se elige.
      # Sin tramo vigente no hay nada que deducir: la duracion salio antes de cualquier orden
      # visible. Eso es ceguera de verdad, y se dice; no se rellena con un numero plausible.
      if (!(clave in cap_de)) { sin_cap++; jobs_sin_cap[j] = 1; continue }
      cap_j = cap_de[clave]; derivado = deriv_de[clave]
      pct = tiempo[clave] * 100.0 / cap_j
      if (pct > peor) peor = pct
      if (pct >= umbral + 0) {
        split(clave, k, "|")
        printf "CRUZA\t%.1f\t%s\t%.1f\t%.1f\t%s\n", pct, k[2], tiempo[clave] / 60, cap_j / 60, (derivado ? estado_de[clave] " · tope = defecto de Go 10m" : estado_de[clave])
      }
    }
    printf "RESUMEN\t%d\t%d\t%.1f\n", total, sin_cap, peor
    for (j in jobs_sin_cap) printf "SINCAP\t%s\n", j
    for (j in jobs_derivados) printf "DERIVADO\t%s\n", j
  }
' "$LOG")

resumen=$(printf '%s\n' "$salida" | grep '^RESUMEN' | head -1)
total=$(printf '%s' "$resumen" | cut -f2)
sin_cap=$(printf '%s' "$resumen" | cut -f3)
peor=$(printf '%s' "$resumen" | cut -f4)

if [ "${total:-0}" -eq 0 ]; then
  echo "NO_HE_PODIDO_MIRAR — el log no trae NINGUNA duración de paquete ('ok'/'FAIL' + tiempo)."
  echo "  Sin duraciones no hay margen que medir, y un 0 aquí se leería como holgura."
  exit 2
fi

# `grep` sin coincidencias devuelve 1, y bajo `set -e` eso mata la asignacion ENTERA: el
# script salia 1 sin imprimir una linea. El `|| true` es lo que separa «no hay ninguno» de
# «he fallado». Medido el 2026-08-19 al anadir estas tres.
derivados=$(printf '%s\n' "$salida" | grep '^DERIVADO' | cut -f2 | sort || true)
if [ -n "$derivados" ]; then
  echo "TOPE DERIVADO del defecto documentado de Go (10m), no leído de la orden, en:"
  printf '%s\n' "$derivados" | sed 's|^|    job: |'
  echo "  Un 'go test' sin -timeout NO tiene tope desconocido: tiene 600s. Decirlo es lo que"
  echo "  separa un dato deducido de un dato leído — los dos valen, y no son el mismo."
fi

if [ "${sin_cap:-0}" -gt 0 ]; then
  echo "NO_HE_PODIDO_MIRAR — $sin_cap paquete(s) con duración impresa ANTES de cualquier orden de"
  echo "  test visible en su job, así que no hay tramo del que deducir el tope:"
  # `grep` sin coincidencias devuelve 1 y bajo `set -e` eso mata la asignacion ENTERA: el script
  # salia 1 sin imprimir una sola linea. El `|| true` separa «no hay ninguno» de «he fallado».
  ciegos=$(printf '%s\n' "$salida" | grep '^SINCAP' | cut -f2 | sort || true)
  if [ -n "$ciegos" ]; then printf '%s\n' "$ciegos" | sed 's|^|      job: |'; fi
  echo "  Saltarlos dejaría un verde que no los cubre. Un gate dice lo que su DESCUBRIMIENTO alcanza."
  exit 2
fi

cruzan=$(printf '%s\n' "$salida" | grep -c '^CRUZA' || true)
if [ "${cruzan:-0}" -gt 0 ]; then
  echo "ROTO — $cruzan de $total paquete(s) por encima del ${UMBRAL}% de su tope por binario:"
  printf '%s\n' "$salida" | grep '^CRUZA' | sort -t$'\t' -k2 -rn \
    | awk -F'\t' '{printf "    %5.1f%%  %-58s %5.1f de %.0f min  (%s)\n", $2, $3, $4, $5, $6}'
  echo "  El tope mata el BINARIO del paquete: el pánico nombrará el test en vuelo, no al culpable."
  exit 1
fi

printf 'LIMPIO — %d paquete(s) medidos, el peor al %.1f%% de su tope (umbral %s%%).\n' \
  "$total" "$peor" "$UMBRAL"
