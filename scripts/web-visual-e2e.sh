#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Visual e2e for the management views. Unlike scripts/web-e2e.sh (which boots a
# real engine for the setup→login smoke), this is HERMETIC: it builds the SPA to a
# private dir, serves it with `vite preview`, and runs the Playwright spec, which
# seeds a session + intercepts every /v1 call with fixtures. So it renders the four
# management views (capabilities, catalog, permissions, deploy, knowledge) in light
# AND dark with no backend, and writes a screenshot per view per theme into
# web/playwright-report/. Building to a private dir keeps it immune to other build
# outputs under core/internal/webui/dist.
#
# Usage: scripts/web-visual-e2e.sh
# Requires: pnpm and Playwright's chromium (`pnpm --dir web exec playwright install chromium`).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# ⛔ AISLAMIENTO DE ENTORNO GIT, Y AQUÍ NO ES SÓLO HIGIENE: ES LO QUE IMPIDE QUE ESTE GUION
#    CONSTRUYA DENTRO DEL GATE DE OTRO. Medido y cazado en vivo el 2026-08-26.
#
#    `lint:git-env` corre en el hook `pre-push`. No LLAMA a los guiones de su clase: los EJECUTA
#    con `--olivares-git-env-probe` para ver si se aíslan. Este guion está en la clase (empareja
#    `mktemp -d` con git) y no cargaba la librería, así que la sonda lo ejecutaba entero — y este
#    guion reconstruye la consola en SU PROPIO worktree (`ROOT` sale de `dirname "$0"`, y
#    `vite.config.ts` va con `emptyOutDir: true`). Resultado: el gate vaciaba y reescribía
#    `core/internal/webui/dist` del árbol que se estaba empujando, y el push moría al FINAL con
#    «UN GATE MODIFICO EL ARBOL DE TRABAJO» tras pagarse entero. Coste medido: 1 h 48.
#
#    Y sólo se NOTABA cuando el bundle commiteado de esa rama estaba obsoleto: si estaba al día el
#    rebuild salía byte-idéntico y no dejaba rastro. Por eso parecía intermitente y por eso una
#    prueba en un árbol al día lo «refutaba» — un falso negativo.
#
#    Cargar la librería lo corta de raíz por el camino que el repo ya tiene:
#    `check-git-env-isolation.sh:156` da por aislado —SIN EJECUTARLO— a todo guion que la cargue.
#    `scripts/web-e2e.sh` la lleva desde antes y es la prueba de que el patrón funciona.
#    (Salir pronto ante la bandera NO vale: `:143-152` exige que la corrida envenenada falle Y se
#    comporte DISTINTO de la limpia, y dos salidas tempranas idénticas se cargan al miembro.)
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=lib/git-env.sh
. "$_olivares_git_env" || {
  echo "ERROR: cannot source $_olivares_git_env — refusing to run git beside a mktemp sandbox" >&2
  exit 1
}
unset _olivares_git_env
WEB="$ROOT/web"
PORT="${VISUAL_E2E_PORT:-5210}"
DIST="$(mktemp -d)"

cleanup() {
  [ -n "${PID:-}" ] && kill "$PID" 2>/dev/null || true
  rm -rf "$DIST"
}
trap cleanup EXIT

echo "==> Building the SPA to a private dir ($DIST)"
pnpm --dir "$WEB" exec vite build --outDir "$DIST" --emptyOutDir

echo "==> Serving the frozen build on 127.0.0.1:$PORT"
pnpm --dir "$WEB" exec vite preview --outDir "$DIST" --port "$PORT" --strictPort >"$DIST/preview.log" 2>&1 &
PID=$!

for _ in $(seq 1 40); do
  if curl -sf "http://127.0.0.1:$PORT/" >/dev/null 2>&1; then break; fi
  sleep 0.5
done

echo "==> Running the visual spec"
PLAYWRIGHT_BASE_URL="http://127.0.0.1:$PORT" \
  pnpm --dir "$WEB" exec playwright test e2e/management-views.spec.ts "$@"

echo "==> Screenshots in web/playwright-report/"
