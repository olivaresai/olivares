> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0015: Cadena de suministro — versiones firmadas, SBOM, procedencia SLSA, OpenVEX, distroless

- **Status:** accepted
- **Fecha:** 2026-06
- **Responsables de la decisión:** Fran Olivares
- **Referencias:** decisiones de stack (T6/T7); diseño de cadena de suministro y verificación de versiones

## Contexto y planteamiento del problema

Para un producto de seguridad, una versión sin firmar o no verificable es inaceptable. Los
compradores necesitan verificar *qué han descargado* —incluso completamente sin conexión, en
entornos air-gapped (aislados de la red)— y conocer la procedencia y el estado de
vulnerabilidades conocidas de cada artefacto.

## Factores de la decisión

- Verificabilidad criptográfica de cada artefacto, en línea y sin conexión.
- Procedencia (quién lo construyó, a partir de qué fuente) y un inventario de materiales de software (SBOM).
- Una imagen de runtime mínima y fijada (pinned).

## Opciones consideradas

- **Firmas cosign/sigstore + SBOM (syft) + procedencia SLSA Build L3 (SLSA v1.2) + OpenVEX +
  imágenes distroless fijadas por digest**, con una ruta de verificación sin conexión y un
  paquete air-gap.
- **Solo checksums / versiones sin firmar.**

## Resultado de la decisión

Opción elegida: el **conjunto completo de cadena de suministro**. Las versiones incluyen firmas
cosign, SBOM SPDX + CycloneDX, procedencia SLSA Build L3 y atestaciones OpenVEX; las imágenes
de contenedor son **distroless, fijadas por digest**. Un script de verificación comprueba toda
la cadena, incluyendo un modo **completamente sin conexión**, y un **paquete air-gap** lleva
una clave pública para que un sitio desconectado pueda verificarlo todo sin un registro de
transparencia.

### Consecuencias

- **Bueno:** cada artefacto es verificable, en línea o sin conexión; la procedencia y un SBOM
  se entregan con cada versión; la imagen de runtime es mínima e inmutable (por digest).
- **Malo / compromisos:** más maquinaria de publicación que mantener; el paquete air-gap exige
  que el SBOM/VEX/procedencia se suministren al empaquetador.
- **Neutral:** el despliegue es siempre por digest, nunca por tag.

## Por qué se rechazaron las alternativas

- **Solo checksums / sin firmar** — no aporta procedencia, ni raíz de confianza sin conexión,
  ni declaración de vulnerabilidades; inaceptable en un producto de seguridad.

## Adenda (2026-07-03): formulación SLSA v1.2 + evaluación del track Source

La formulación SLSA se normaliza como **SLSA Build L3 (SLSA v1.2)**. En SLSA v1.2,
el track Build termina en L3, por lo que esta ADR solo afirma el nivel del track Build.

La evaluación del track Source sigue siendo independiente. Source L1-L3 exigiría
revisiones de fuente retenidas, además de atestaciones de procedencia del sistema de
control de versiones; Source L3 añade aplicación continua con evidencia de
manipulación, por ejemplo mediante gittuf o atestaciones de la plataforma.

Estado actual: la protección de ramas está automatizada en
`scripts/apply-branch-protection.sh`, pero no se han desplegado atestaciones de
procedencia de la fuente.

Decisión: no se afirma ningún nivel del track Source; se seguirá dicho track y se
revisará la decisión en el lanzamiento público.
