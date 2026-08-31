---
title: Open core y licencias
description: >-
  Open core: el producto completo es AGPL-3.0-only, el SDK y los conectores son
  Apache-2.0, y una pequeña línea enterprise aditiva es comercial. El binario AGPL
  nunca se mutila para empujarte a pagar, pero no es idéntico a la edición
  comercial. Qué significa para quienes hacen self-hosting y para los autores de
  conectores.
---

Olivares AI es **open core**. El **producto completo** se publica bajo la GNU
Affero General Public License, y el binario AGPL es la plataforma de gobierno al
completo — nunca mutilado desde dentro para empujarte hacia una edición de pago.
Encima de él se asienta un pequeño conjunto de add-ons comerciales **aditivos** en
`enterprise/`, compilados solo con `-tags enterprise` y ausentes del binario
público. Una licencia comercial proporciona la excepción legal al copyleft; las
capacidades de `enterprise/` se licencian como **add-ons separados y opcionales** —
de modo que las ediciones abierta y comercial **no** son idénticas,
mientras que nada de lo publicado en abierto se traslada jamás detrás del muro (el
modelo `ee/` de GitLab, no un muro de pago sobre funciones en el núcleo).

## La frontera de licencia

Las licencias siguen el árbol de fuentes. Cada fichero lleva una cabecera SPDX, y la
frontera se aplica en CI (un conector nunca puede importar el motor):

| Ruta | Licencia | Qué es |
|---|---|---|
| `core/` | **AGPL-3.0-only** | el motor: ingesta, bus de eventos, modelo de datos, runtime de módulos, API, authz, auditoría |
| `modules/` | **AGPL-3.0-only** | los 30 módulos (inventario, el mapa R/RW, FinOps, evals, guardrails, …) |
| `web/` | **AGPL-3.0-only** | la interfaz React |
| `sdk/` | **Apache-2.0** | las interfaces de conector/módulo, el contrato gRPC y los tipos compartidos |
| `connectors/` | **Apache-2.0** | los conectores (Claude, OpenAI, pgAudit, eBPF, cloud, Slack, SIEM, …) |
| `enterprise/` | **comercial** | add-ons aditivos, protegidos por build-tag, nunca en el binario público: federación multi-IdP, content firewall/DLP, hook hardening, catálogo compilado de threat-intel, egress de server-tools, CyberArk Conjur, cierre de incidentes (close-loop) (`LicenseRef-Olivares-Commercial`) |

El sitio de documentación que estás leyendo forma parte del producto AGPL.

## Qué significa esto para ti

- **Self-hosting del producto (AGPL).** Puedes ejecutar, estudiar, modificar y
  redistribuir el producto completo bajo la AGPL. Se aplica la cláusula de uso en red
  de la AGPL: si ofreces una versión modificada a terceros a través de una red, debes
  ofrecerles tu código fuente modificado. Para self-hosting interno esto rara vez es
  un problema; si quieres construir un producto *encima de* Olivares AI sin esa
  obligación, la licencia comercial existe exactamente para eso.
- **Construir conectores (Apache-2.0).** El SDK y los conectores son **Apache-2.0** —
  permisiva, sin copyleft. Puedes escribir un conector, mantenerlo propietario y
  distribuirlo como quieras. La frontera arquitectónica que hace esto seguro está
  aplicada: un conector Apache-2.0 **nunca importa el motor AGPL**; depende únicamente
  del SDK. Eso mantiene el ecosistema de conectores libre de la fricción del copyleft.
- **Una licencia comercial.** Las organizaciones que necesitan evitar las obligaciones
  de la AGPL (por ejemplo, integrando el producto en una oferta propietaria) pueden
  obtener una licencia comercial — contacto: **enterprise@olivares.ai** (precios
  bajo consulta). Los add-ons aditivos de `enterprise/` indicados arriba se
  licencian por separado, cada uno como un derecho opcional.

## Qué es abierto y qué es enterprise

El binario abierto es la plataforma de gobierno al completo; la línea `enterprise/`
es **aditiva**. Merece la pena destacar dos fronteras, porque el binario abierto
responde por ellas con honestidad en vez de fingirlas:

- **SSO** — el login de un solo IdP (OIDC + SAML 2.0) es **abierto** en el binario
  por defecto: login real, sin `-tags enterprise`. Varios IdP activos (por tenant /
  por dominio), la imposición de SSO (SSO-enforcement) y el SCIM gestionado son la
  línea enterprise reservada; activar un segundo IdP activo devuelve
  `multi_idp_requires_enterprise`.
- **Cuentas de usuario** — **ilimitadas en todas las ediciones**. El binario de
  comunidad no tiene tope de usuarios, y el enterprise tampoco: ningún estado de
  licencia (válida, caducada, ausente) puede limitar cuántas cuentas ejecuta un
  deployment. El tope de tres cuentas activas anterior al 2026-07-27 se eliminó por
  completo; el seam de asientos permanece en el código como un no-op de compatibilidad
  que no rechaza nada, y que una licencia caduque nunca limita, desactiva ni borra una
  cuenta.

Consulta [Honestidad y límites](/es/start/honesty-and-limits/) para el panorama
completo de qué es abierto y qué es enterprise.

## La licencia nunca limita el producto abierto

Esto es importante y deliberado: en el binario abierto (AGPL), la validación de
licencia es **solo atestación**. El motor registra quién posee una licencia y su
estado; **nunca deshabilita, degrada ni bloquea** ninguna petición, ningún módulo ni
el arranque por una comprobación de licencia, y funciona **offline** (una firma
Ed25519, sin servidor de licencias), motivo por el cual el producto abierto funciona
air-gapped. El único punto donde la licencia se *consume* en lugar de mostrarse es el
binario enterprise cerrado, y solo para dar derecho a los add-ons que cubre el
acuerdo comercial, evaluados add-on por add-on — una
decisión local de la edición comercial, nunca una comprobación en el binario abierto.
Nunca limita usuarios: las cuentas son ilimitadas en todas las ediciones. Así que
el binario abierto es genuinamente íntegro y sin tope por licencia; lo que difiere en
la edición comercial son los add-ons aditivos de `enterprise/`, no una clave de
licencia que active funciones dentro del mismo binario.

## Por qué este modelo

Se rechazó mutilar el núcleo: la edición abierta hace el trabajo completo — todo el
ciclo de gobierno en un solo nodo —, así que recortarla la convertiría en un producto
peor y erosionaría la confianza. Lo permisivo en todo (MIT/Apache en el núcleo)
regalaría el núcleo sin base comercial. Las licencias source-available, no OSS (BSL,
SSPL y similares) matarían la adopción open source que es justamente el propósito de
un ecosistema de conectores extensible. Por eso el modelo es **open core**: un
producto copyleft que es completo y creíble por sí mismo, un SDK permisivo que
mantiene el ecosistema de conectores sin fricción, y una pequeña línea comercial
**aditiva** de código nuevo que nunca estuvo en el binario abierto — más una
excepción comercial limpia — *sin degradar jamás lo que puedes alojar tú mismo*.

## Contribuir

Las contribuciones se aceptan bajo los términos de contribución del proyecto (el
repositorio incluye tanto un DCO como un CLA, además de una política de marca). Consulta
la guía `CONTRIBUTING` del repositorio para conocer el proceso actual.

## Relacionado

- [Instalar una licencia y pasar a enterprise](/es/how-to/install-a-license/) — dónde se
  guarda una licencia adquirida y cómo hacer el cambio in-place de Community → enterprise.
  Esta página explica el modelo; la otra detalla los pasos.
- [Modelo de seguridad](/es/explanation/security/security-model/) — por qué las licencias
  solo de atestación importan para un producto de seguridad air-gapped.
