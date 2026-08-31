> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0014: Publicación pública y CI en GitHub Actions + Docker

- **Status:** accepted
- **Fecha:** 2026-06-04
- **Responsables de la decisión:** Fran Olivares (decisión de arranque)
- **Referencias:** decisiones de arranque del roadmap (Release/CI)

## Contexto y planteamiento del problema

El desarrollo ocurre en un repositorio privado; la cadena de suministro pública y
verificable necesita una superficie de CI/publicación ampliamente confiable y transparente
(para identidades de firma sin claves —keyless—, procedencia SLSA y distribución pública
de artefactos).

## Factores de la decisión

- Una identidad de publicación pública y verificable (OIDC) y un registro de transparencia para la firma.
- Distribución de contenedores estándar y ampliamente confiable.
- Mantener el desarrollo diario en privado hasta que una versión esté curada y publicada.

## Opciones consideradas

- **GitHub Actions + Docker para todos los artefactos públicos; un repositorio de desarrollo privado.**
- **CI self-hosted también para las versiones públicas.**

## Resultado de la decisión

Opción elegida: **GitHub Actions + Docker para todo lo público, siempre**; **el desarrollo
ocurre en un repositorio privado**. La identidad OIDC de GitHub Actions del workflow de
publicación es lo que atestiguan las firmas sin claves y la procedencia SLSA, y las
imágenes/charts se publican en un registro OCI público.

### Consecuencias

- **Bueno:** las firmas y la procedencia se encadenan a una identidad pública y verificable;
  distribución estándar; el desarrollo permanece privado hasta que se publica intencionadamente.
- **Malo / compromisos:** el repositorio público es una exportación curada del repositorio
  de desarrollo privado, no un espejo en vivo.
- **Neutral:** publicar el repositorio público es una acción deliberada y controlada por una puerta (gated).

## Por qué se rechazaron las alternativas

- **CI self-hosted para las versiones públicas** — una identidad de firma self-hosted es
  mucho más difícil de verificar para terceros que una identidad OIDC pública de GitHub
  Actions con un registro de transparencia.
