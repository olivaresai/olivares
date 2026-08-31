---
title: Dónde encaja Olivares AI con tu IdP
description: >-
  Olivares AI no es un proveedor de identidad. Federa la identidad de agentes
  desde los registros que ya ejecutas — Entra Agent ID, AWS AgentCore Identity,
  Google Agent Identity — en modo solo lectura, y la usa para atribuir el access
  map. Cómo se compone con tu IdP, SSO/SCIM, SPIFFE/WIF y los estándares ID-JAG / XAA.
sidebar:
  order: 3
---

Una primera pregunta frecuente de un arquitecto de seguridad es: *"¿Es esto otro
sistema de identidad que tengo que ejecutar?"* No. **Olivares AI no es un proveedor de identidad y
no posee identidades.** **Consume** las identidades que ya emites — para
humanos, desde tu IdP vía SSO/SCIM; para agentes, desde los registros de identidad de agentes
que los hyperscalers han puesto en disponibilidad general — y las usa para atribuir *quién
o qué* hay detrás de cada edge en el [access map](/es/explanation/). Esta nota
explica exactamente dónde se sitúa la juntura.

## La estratificación

```
   Your IdP (Entra ID / Okta / Google)         ← humans: SSO + SCIM (unchanged)
   Agent-identity registries                    ← agents: Entra Agent ID,
     (Entra Agent ID / AgentCore / Google)        AgentCore Identity, Google Agent Identity
            │  read-only roster sync
            ▼
   Olivares AI  ── SPIFFE/WIF roster ──► R/RW access map (attributed edges)
            │                            └─ Permitted-vs-Observed drift
            └─ deny-closed gates (approvals, hooks PEP, MCP gating) — never an IdP
```

- **Los humanos** se autentican a través de **tu** IdP. Olivares AI se integra con
  **SSO y SCIM** estándar para las cuentas de operador y el mapeo de grupo a rol; no
  almacena credenciales ni se convierte en un segundo directorio.
  → [Identidad SSO y SCIM](/es/how-to/connectors/sso-scim-identity/)
- **Los agentes** obtienen su identidad de los registros que ya adoptaste. Olivares
  AI federa esos rosters **en modo solo lectura** sobre un roster interno **SPIFFE/WIF**,
  de modo que cada acceso observado pueda ligarse a una identidad gobernada y con nombre, en lugar de un
  proceso anónimo.

## Qué hace en realidad la federación de identidad de agentes

El control plane incluye conectores de roster de solo lectura para los registros de identidad de agentes en
disponibilidad general, cada uno verificado contra su fuente primaria y **deny-closed** (sin
credencial → roster vacío, nunca un error fantasma):

- **Microsoft Entra Agent ID** — importa identidades de agentes, blueprints y
  relaciones de propietario/patrocinador vía Microsoft Graph; aflora los huérfanos
  declarados por el registro. Los blueprints que llevan credenciales de contraseña de larga vida elevan un
  finding de **long-lived-credential drift**.
- **AWS AgentCore Identity** — importa el roster de agentes; los agentes con una identidad
  de servicio se mapean a un tipo de identidad de cuenta de servicio.
- **Google Agent Identity** — importa identidades de reasoning-engine; la referencia
  es un **SPIFFE ID** completo, de modo que converge con el roster SPIFFE por external id.

Estos mapeos alimentan el eje de [atribución del access map](/es/reference/glossary/#atribución-confianza)
(`firm` / `approximate` / `unknown`) — no lo reimplementan. La federación
es estrictamente de solo lectura: Olivares AI **nunca** muta un registro remoto. Las señales de propiedad
y de huérfanos se reenvían al ciclo de vida de non-human-identity para que un
huérfano declarado por el registro aparezca a través de la maquinaria de gobernanza existente.

:::note[Experimental y orientado al diseño, etiquetado como tal]
Los descriptores cross-ecosystem (**OASF**) y los **AGNTCY Agent Badges** se tratan como
**experimentales** hasta que cumplan la conformidad de verifiable-credential. Los rosters que
siguen en preview (p. ej. la Gemini Enterprise Agent Platform de Google) se cablean como
**junturas**, no se declaran como activos. Marcamos qué está en disponibilidad general, qué está en preview y qué está
orientado al diseño — no los difuminamos.
:::

## ID-JAG, XAA y autenticación de cliente basada en SPIFFE

Los estándares enterprise para el acceso de agentes *delegado y atribuible* están
convergiendo, y el control plane está construido para cabalgarlos en lugar de inventar el
suyo propio:

- **ID-JAG** (Identity Assertion JWT Authorization Grant) y **XAA** (Cross-App
  Access) son el patrón emergente para que un IdP emita autorización **acotada y atribuible**
  para un agente que actúa a través de aplicaciones — la extensión de autorización gestionada por la empresa
  en el trabajo de autorización de MCP. A medida que aterricen, el
  token atribuible se convierte en otra señal de alta fidelidad que el access map puede ligar
  a una identidad gobernada.
- **Autenticación de cliente OAuth basada en SPIFFE**
  (`draft-ietf-oauth-spiffe-client-auth`) permite que los propios flujos OAuth del plano se
  autentiquen con un **SVID** en el momento en que un servidor de autorización publique
  soporte — sobre el mTLS deny-by-default existente. Esto está **orientado al diseño**, sin
  reclamación de conformidad, hasta que el draft y el soporte del servidor se estabilicen.
- **De vida corta por defecto.** Las credenciales estáticas de larga vida descubiertas en el
  estate se marcan como una clase de drift, en línea con la guía de **Five Eyes** (2026)
  de que las credenciales de agentes deberían ser de vida corta.

## Qué significa esto para ti

- Conservas tu IdP, tu SSO, tu SCIM y cualquiera que sea el registro de identidad de agentes en el que
  te estandarizaste. Nada migra.
- Olivares AI se convierte en el lugar donde **todas** esas identidades se encuentran con el
  **comportamiento observado** de tu estate — la única capa que puede decir "este agente,
  de este registro, propiedad de este humano, está usando un acceso que la política nunca
  concedió."
- Como la federación es de solo lectura y self-hosted, esa correlación no requiere una
  transferencia impuesta: no hay telemetría obligatoria ni egreso del plano de control de
  forma predeterminada. Solo cruza tu perímetro lo que **tú** configuras para que lo cruce:
  llamadas a tus API de modelos, las salidas SIEM/webhook que conectas y un proveedor
  externo de embeddings si aprovisionas uno.

## Relacionado

- [Agente / Identidad / NHI](/es/reference/glossary/#identidad--nhi) — las definiciones
  del glosario.
- [vs torres de control de IA](/es/explanation/positioning/vs-control-towers/) — la
  integración bidireccional con los planos de administración del ecosistema.
