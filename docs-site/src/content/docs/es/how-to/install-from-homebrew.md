---
title: Instalar con Homebrew
description: >-
  La coordenada del cask de Homebrew en macOS para Olivares AI, lo que el cask
  hace con Gatekeeper y el estado de publicación del bump del tap v26.9.0.
draft: false
---

Esta es la vía macOS que `INSTALL.md` nombra como recomendada. Instala el
binario `olivares` firmado mediante el cask de Homebrew y limpia la
cuarentena de Gatekeeper. No es la vía de paquetes Linux
([Instalar desde un paquete](/how-to/install-from-packages/)) ni Docker
([Desplegar con Docker](/how-to/docker-deployment/)).

:::note[Beta — el cask v26.9.0 está publicado]
El testigo de superficies de instalación registra Homebrew como
**published** (`docs/releases/v26.9.0-install-surfaces.json`, medido el
2026-09-23T20:28:11Z): `Casks/olivares.rb` del tap nombra la versión 26.9.0 y cuatro
archivos de plataforma cuyos SHA-256 son los de la release. El productor es
`.goreleaser.yaml` `homebrew_casks:`; el trabajo de release actualiza el cask del tap. El
comando de abajo es la coordenada que nombra `INSTALL.md` (`brew install olivaresai/tap/olivares`).
:::

## 1. Instalar el cask

```sh
brew install olivaresai/tap/olivares
```

Homebrew comprueba cada descarga del cask contra su SHA-256 registrado
(`INSTALL.md`). El cask instala el binario firmado y **limpia la cuarentena
de Gatekeeper**.

Los binarios Darwin están firmados por cosign (confianza de cadena de
suministro) y **aún no están notarizados por Apple**. Una descarga manual del
archivo queda en cuarentena; el cask lo gestiona, o lo limpias con
`xattr -d com.apple.quarantine olivares` como muestra `INSTALL.md` en la vía
manual.

## 2. Primer arranque

```sh
olivares quickstart
```

Valores seguros: TLS activo, loopback, sin credenciales predeterminadas. El
motor imprime la URL de la consola y el token de configuración de un solo
uso. Sigue con [Tu primera hora](/how-to/first-hour/).

Un estate sintético efímero (loopback, texto plano) es solo para mirar:

```sh
olivares serve --seed-demo --insecure --data-dir "$(mktemp -d)"
```

`--seed-demo` no es un recorrido del producto. Véase
[Tu primera hora](/how-to/first-hour/).

## Relacionado

- [Autoalojar el plano de control](/how-to/self-hosting/) — otras formas de instalación.
- [Verificar una versión](/how-to/verify-a-release/) — cosign, SBOM, procedencia.
- [Instalar desde un paquete](/how-to/install-from-packages/) — `.deb` / `.rpm` / `.apk` en Linux.
