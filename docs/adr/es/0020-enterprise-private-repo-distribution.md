> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0020: Edición enterprise distribuida desde un repositorio privado separado

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** ADR-0010 (license is attestation-only), ADR-0011 (license boundary),
  `LICENSING.md`

## Contexto y planteamiento del problema

El modelo de licencias es open core: el núcleo/los módulos/la web AGPL constituyen la
edición community completa, el SDK/los conectores son Apache-2.0 y la línea `enterprise/`
es código comercial aditivo que solo se compila con `-tags enterprise` (ADR-0011). Pero,
hasta ahora, el código fuente de `enterprise/` **se distribuía en el repositorio público**.
Como el gate de activación es el build tag (no la licencia, que es solo de atestación según
ADR-0010) y la licencia nunca controla el runtime, cualquiera podía ejecutar
`git clone && go build -tags enterprise` y obtener gratis el binario comercial completo.
La ventaja competitiva comercial descansaba por entero en la licencia legal (un sistema
de honor sobre código fuente visible).

## Motivadores de la decisión

- Hacer que el gating por build tag sea **real**, no cosmético: nadie debería poder compilar
  el binario comercial a partir del código fuente público.
- Mantener el binario community AGPL **idéntico bit a bit**: sin rug-pull ni retirada de
  funcionalidades que ya se distribuyeran como abiertas.
- Preservar la frontera de licencias por directorio (ADR-0011) y la licencia solo de
  atestación (ADR-0010), ambas sin cambios.

## Opciones consideradas

- **Mantener `enterprise/` en el repositorio público** (el modelo de GitLab con `ee/` en un
  único repositorio, source-available). Es honesto, pero la ventaja competitiva se basa en
  un sistema de honor sobre código fuente visible y libremente compilable.
- **Mover `enterprise/` a un repositorio privado separado** (el modelo de Grafana: código
  fuente OSS público + binario enterprise descargable compilado desde código privado).

## Resultado de la decisión

Opción elegida: **mover `enterprise/` a un repositorio privado separado**. El repositorio
público deja de contener el árbol `enterprise/`, el wiring de `cmd/olivares` con
`//go:build enterprise` y cualquier herramienta que compile con `-tags enterprise`. El
binario comercial se compila en el repositorio privado superponiendo el árbol comercial y
el wiring sobre un checkout fijado del árbol público (el árbol público es un submódulo; el
wiring se superpone en el `package main` de `cmd/olivares`, algo que `go.work` no puede
lograr únicamente mediante la selección de módulos).

Esto solo cambia la **distribución**, no las licencias:

- **ADR-0011 (frontera de licencias) permanece sin cambios:** `enterprise/` sigue siendo
  `LicenseRef-Olivares-Commercial`; la frontera AGPL/Apache permanece intacta.
- **ADR-0010 (licencia solo de atestación) permanece sin cambios:** el binario abierto sigue
  sin leer nunca una licencia para activar o desactivar nada. La licencia adquiere
  relevancia *material* únicamente porque el código fuente que lee una declaración
  atestada (los entitlements de los add-ons) deja de ser público, no porque la licencia haya
  empezado a controlar el runtime.

### Consecuencias

- Un `git clone` del repositorio público + `go build -tags enterprise` ya no produce el
  binario comercial: el código fuente que necesita es privado. El gating ahora es real.
- El binario AGPL predeterminado no cambia: nunca enlazó `enterprise/`.
- El gate de paridad de esquema open≡enterprise (necesita ambas ediciones) se traslada al
  repositorio privado, el único árbol capaz de compilar ambas.
- Dos repositorios y un pequeño paso de ensamblaje por superposición son el coste; el
  artefacto de la versión pública no se ve afectado (ya se compilaba con `-tags release`,
  nunca con `-tags enterprise`).

## Enmienda (2026-07-28) — el entitlement de plazas citado arriba ha desaparecido

La decisión de distribución se mantiene: `enterprise/` vive en un repositorio privado y
el gating por build tag es real. Lo que ya no se sostiene es el *ejemplo* usado en las
Consecuencias: «el código fuente que lee una declaración atestada (el entitlement de
plazas)». La decisión B10 (2026-07-27) eliminó el límite de usuarios, por lo que no hay
entitlement de plazas y ninguna compilación lee una licencia para limitar usuarios; las
declaraciones atestadas restantes se leen solo para habilitar los add-ons aditivos. La
frase original se conserva tal como está porque registra lo que era cierto cuando se tomó
este ADR. Decisión actual: el canon comercial de precios (mantenido en privado)
(`self_hosted.users: unlimited`) y `LICENSING.md`
