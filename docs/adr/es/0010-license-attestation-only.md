> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0010: La licencia es solo atestación — nunca limita funcionalidades

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** diseño de licencias (decisión final); contrato de API/authz/auditoría (§7, §13.5)

## Contexto y planteamiento del problema

Un producto comercial open-core debe decidir qué *hace* su aplicación de licencia en
tiempo de ejecución. La tentación es limitar funcionalidades tras una comprobación de
licencia. Para un producto de seguridad que además es una posible superficie de elusión
de la autorización, eso entra en conflicto con una filosofía de "licencia dual pura, sin
nada capado".

## Factores de decisión

- No mutilar el producto abierto.
- No convertir la validación de licencia en una superficie de elusión de la autorización.
- Funcionar air-gapped, sin servidor de licencias.

## Opciones consideradas

- **Validación de licencia solo de atestación** que nunca bloquea nada.
- **Limitación de funcionalidades** por nivel de licencia.

## Resultado de la decisión

Opción elegida: **solo atestación**. La validación de licencia registra al titular y el
estado y es informativa; **nunca deshabilita, degrada ni bloquea** ninguna solicitud,
módulo ni arranque **en el binario abierto (AGPL)**. La validación es **offline** (una
firma Ed25519; sin servidor de licencias). El binario abierto es la plataforma CORE de
gobernanza completa; la edición comercial añade los add-ons `enterprise/` separados y
aditivos (esto es open core, no «el mismo producto completo» — véanse la enmienda más
abajo y ADR-0011).

### Consecuencias

- **Bueno:** el producto abierto nunca queda mutilado; las comprobaciones de licencia no
  pueden convertirse en una elusión de la autorización; el producto funciona air-gapped.
- **Malo / compromisos:** la diferenciación comercial proviene de los *términos de la
  licencia* y de los módulos `enterprise/` separados, no de limitar el núcleo.
- **Neutral:** las pruebas de licencia son fail-open por diseño.

## Por qué se rechazaron las alternativas

- **Limitación de funcionalidades** — empeora la edición abierta como producto, erosiona
  la confianza y convierte una licencia falsificable en una comprobación relevante para
  la seguridad. Rechazada.

## Enmienda (2026-06-23)

Este ADR se mantiene, con un alcance preciso: **la clave de licencia nunca limita el
binario ABIERTO.** El modelo es **open core** (enmienda de ADR-0011), por lo que la frase
anterior «el producto gratuito y el licenciado son el mismo producto completo» era
inexacta y queda corregida arriba: la edición comercial tiene los add-ons `enterprise/`
aditivos (que son los «módulos `enterprise/` separados» que este ADR siempre menciona).
Una declaración atestada solo la *consume* la compilación enterprise cerrada, y solo para
habilitar esos add-ons aditivos: una decisión local en la propia compilación comercial del
cliente. La nube de Olivares tampoco debe confiar nunca en el estado autodeclarado para la
facturación.

**Enmienda (2026-07-27, B10).** El único consumo ligado a plazas que esta nota describía
antes — `enterprise/seats` leyendo `license.Claims.MaxUsers` para levantar un límite de
plazas de usuarios de community — ha desaparecido: las cuentas de usuarios autoalojadas
son ILIMITADAS en todas las ediciones, se eliminó el límite de 3 y `MaxUsers` es solo de
visualización en todas partes. Esto refuerza este ADR en vez de matizarlo: ahora ninguna
compilación, abierta o comercial, lee una licencia para limitar usuarios. Véanse
`LICENSING.md` y el canon comercial de precios (mantenido en privado).
