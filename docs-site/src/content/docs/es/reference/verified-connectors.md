---
title: Conectores verificados (de terceros)
description: >-
  El índice curado de conectores de terceros cuyas releases los mantenedores
  han vuelto a verificar — frontera, firma, procedencia y revisión de datos
  mínimos — y cómo enviar el tuyo.
---

Esta página es el **índice curado de conectores de terceros**. Es el
complemento externo del [catálogo de conectores de primera parte](/es/reference/connectors/):
los conectores de primera parte se distribuyen dentro del producto; los conectores
que se listan aquí los construyen, publican y mantienen **sus editores** con el
[SDK de conectores](/es/how-to/build-a-connector/) público.

## Qué significa "verificado"

Una release listada ha sido vuelta a verificar por los mantenedores, a mano, contra
esta lista de comprobación:

1. **Frontera de licencia** — el conector se construye fuera del árbol y no enlaza nada
   del motor AGPL (`go list -deps` no muestra ningún
   `github.com/olivaresai/olivares/core`); solo importa el SDK Apache-2.0.
2. **Firma y procedencia** — el bundle de atestación Sigstore publicado
   verifica contra la identidad declarada del editor o su clave pública, y su
   digest de sujeto coincide con el binario publicado.
3. **Corrección del contrato** — `Descriptor.Name` está en notación de puntos y
   con namespace del proveedor, los `ConfigFields` declarados coinciden con lo que lee `Open`,
   los secretos se declaran `Secret: true` y se toman por referencia.
4. **Datos mínimos** — el conector emite referencias y metadatos, nunca
   payloads, prompts ni valores de secretos (revisión puntual de las rutas de emisión).

**Lo que no significa:** la verificación no es una auditoría de seguridad del
editor ni del sistema observado, no es un aval, y **no es una raíz de
confianza** — un operador que cablea un conector verificado sigue fijando la
clave o identidad del editor en `connector_trust` y el digest de la release en el bloque
`plugin` de la fuente. La admisión en el host sigue siendo deny-closed en cualquier caso.

Un conector privado no necesita figurar aquí para estar gobernado. Si un operador
fija su digest y su ancla de confianza en `connector_trust`, el motor aplica la misma
admisión deny-closed y el mismo gobierno en runtime. Este índice es un rastro de
certificación para el descubrimiento y la reverificación, no una raíz de confianza.

## Índice

Todavía no hay conectores de terceros listados — el programa abre con esta
release. Los conectores de primera parte están en el
[catálogo de conectores](/es/reference/connectors/).

| Conector (`Descriptor.Name`) | Editor | Tipo | Release verificada | Firma | Fuente |
|---|---|---|---|---|---|
| _ninguno todavía_ | | | | | |

## Enviar un conector

Abre un pull request sobre esta página añadiendo una fila a la tabla, enlazando:

- el repositorio de origen y la release (binario + `sha256` + bundle
  Sigstore);
- la identidad contra la que verificar (identidad OIDC + emisor para keyless, o la
  clave pública);
- la salida de `./scripts/check-boundary.sh` y la ejecución de tests en tu CI.

Los mantenedores reproducen la lista de comprobación anterior sobre los artefactos exactos de la release.
Una nueva release de un conector listado necesita una actualización de fila (la re-verificación es
por release, porque el veredicto se vincula al digest). Las releases obsoletas o retiradas
se eliminan.

## Relacionado

- [Construir y distribuir un conector](/es/how-to/build-a-connector/) — el ciclo de vida completo
- [Módulo XIV — catálogo interno y marketplace](/es/reference/modules/xiv-catalog/) —
  certificación en el producto (entradas de conector + admisión firmada)
- [Estabilidad de la API](/es/reference/api-stability/) — el contrato de estabilidad del SDK
- [Verificar una release](/es/how-to/verify-a-release/) — la misma disciplina de
  cadena de suministro para los propios artefactos del producto
