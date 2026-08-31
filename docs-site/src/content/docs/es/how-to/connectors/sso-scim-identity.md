---
title: "SSO, SCIM y fuentes de identidad (atribución firme)"
description: >-
  Conecta la identidad empresarial de extremo a extremo: login federado en la
  consola (OIDC/SAML a través del seam de federación), aprovisionamiento SCIM
  hacia el control plane y las fuentes de roster LDAP / Okta / Entra que elevan
  la atribución del mapa de accesos de aproximada a firme.
sidebar:
  order: 8
---

La identidad es la dependencia dura bajo todo el mapa de accesos: la auditoría
nativa atribuye un acceso a una **credencial**, y solo un roster de identidad
puede ligar esa credencial a un **agente o persona**. Esta página conecta las
tres superficies de identidad: el **login SSO** de la consola, el
**aprovisionamiento SCIM** hacia el control plane y las **fuentes de roster**
(LDAP, Okta, Entra ID) que hacen la atribución `attributed` en lugar de
`approximate`.

## 1. SSO de la consola (OIDC / SAML)

El login federado se sirve a través del **seam de federación** del motor. La
postura es honesta por construcción:

- Los endpoints del flujo de login existen en todas las builds, y el motor
  mantiene del lado del servidor cada valor del flujo que porta secretos — el
  estado CSRF, el nonce OIDC, el verifier PKCE (solo el *challenge* S256 va al
  proveedor). Authorization Code + **PKCE está siempre activo**.
- La build por defecto incluye el proveedor `NoFederation`: ambos endpoints
  devuelven `501 sso_not_configured` — la superficie se anuncia honestamente sin
  ningún IdP conectado. El proveedor de federación que completa el protocolo
  forma parte de la build empresarial y se **configura por entorno en el
  arranque** (`OLIVARES_SSO_PROTOCOL`, el conjunto `OLIVARES_OIDC_*` para OIDC,
  el conjunto `OLIVARES_SAML_*` para SAML).
- La URI de redirect/ACS que tu IdP debe portar es **exacta**
  (`…/v1/auth/federation/callback` en el origen de tu consola — coincidencia
  exacta según RFC 9700, sin trucos de prefijo).

La pestaña **Identity & NHI → SSO & SCIM** de la consola documenta la
configuración en vivo, comprueba la URI de redirect de tu IdP contra el valor
exacto esperado y muestra el estado de la conexión — y allí donde el backend de
un panel es un contrato declarado pero aún no operativo, dice "backend pending"
en lugar de renderizar datos fabricados:

<img class="light:sl-hidden" src="/console/identity-dark.png" alt="La vista Identity & NHI: configuración SSO con comprobación de redirect-URI exacta, el roster de NHI y pestañas clave de postura." />
<img class="dark:sl-hidden" src="/console/identity-light.png" alt="La vista Identity & NHI: configuración SSO con comprobación de redirect-URI exacta, el roster de NHI y pestañas clave de postura." />

## 2. Aprovisionamiento SCIM (entrante)

El control plane es un proveedor de servicio SCIM 2.0 (RFC 7644) estándar en:

```
/v1/scim/v2/Users
/v1/scim/v2/Groups
```

- **Auth:** un **token de API de admin/owner** ligado al tenant en la
  integración SCIM — el mismo modelo de token opaco que el resto de la API, sin
  un tipo de secreto SCIM aparte. El endpoint está siempre presente (no está
  detrás de un feature gate).
- **Users** aprovisiona y desaprovisiona principales; el desaprovisionamiento por
  parte de tu IdP revoca el acceso en el momento en que RRHH lo dice.
- **Groups** lleva los datos de referencia de identidad-a-grupo. Cada grupo puede
  mapearse a un rol del control plane mediante `mapped_role` — y ese mapeo es
  **propiedad del operador**: se establece en el lado del control plane y se
  audita (`scim.group.role.map`); un push del IdP nunca escala un rol de forma
  silenciosa. Los miembros desconocidos de un grupo enviado se omiten **y se
  auditan**, no se inventan.

## 3. Fuentes de roster: LDAP, Okta, Entra ID

Las fuentes de roster alimentan el inventario de identidad del módulo VI y —este
es el punto— dan al módulo III los bindings que elevan la atribución:

```json
{
  "sources": [
    {
      "name": "corp-ldap",
      "kind": "ldap",
      "tenant": "<tenant-id>",
      "config": {
        "url": "ldaps://ldap.corp.example:636",
        "bind_dn": "cn=olivares-ro,ou=svc,dc=corp,dc=example",
        "bind_password": "<reference>",
        "base_dn": "dc=corp,dc=example"
      }
    },
    {
      "name": "okta",
      "kind": "idp",
      "tenant": "<tenant-id>",
      "config": { "provider": "okta", "base_url": "https://corp.okta.com", "api_token": "<reference>" }
    }
  ]
}
```

Opciones clave de LDAP (del descriptor que se envía): `user_filter` /
`group_filter`, `privileged_group_dns` (grupos cuya pertenencia es en sí misma
una señal de acceso privilegiado), `nhi_dn_suffix` (qué subárbol contiene las
identidades no humanas), `start_tls`, `page_size`. El kind `idp` admite
`provider: okta` (con `api_token`) o `provider: entra` (con `tenant_id` /
`client_id` / `client_secret`); `okta` y `entra` también funcionan directamente
como el `kind`.

### Cómo eleva esto la atribución — con precisión

Una fuente de roster registra identidades (por external id) y, allí donde el
directorio las declara, **concesiones permitidas**. Cuando el origen de una
arista observada coincide con una identidad de roster **no compartida**, el
módulo III liga el acceso a esa identidad y la confianza de la arista se eleva a
`attributed`. Las identidades que varios workloads comparten permanecen
honestamente como `approximate` — el roster no puede des-compartir una
credencial; solo emitir identidad por agente puede hacerlo
([el puente hacia la gobernanza](/es/how-to/govern-and-approve/)).

Los **kinds dedicados de identidad-de-agente e identidad-de-workload** (las
fuentes de federación de agentes — Entra Agent ID, AgentCore, SPIFFE y
similares) son la señal firme por agente; los rosters de grupo/directorio afinan
las personas y las cuentas de servicio.

## Límites honestos

- **El SSO se completa en la build empresarial.** El seam, la seguridad del flujo
  y la postura 501 están en todas las builds; el proveedor de protocolo no.
- **Un roster no puede arreglar una credencial compartida.** Solo puede decirte,
  honestamente, que la credencial es compartida.
- **SCIM es aprovisionamiento entrante** — el control plane no empuja identidades
  de vuelta a tu IdP, y el receptor de Security-Event-Token es una superficie
  entrante, no un webhook saliente.

## Relacionado

- [Conectar una fuente](/es/how-to/connect-a-source/#la-dependencia-dura-identidad-por-agente)
  — por qué la identidad es la dependencia dura.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — roles, RBAC y qué concede un
  `mapped_role`.
- [Conectores y niveles de cobertura](/es/reference/connectors/) — la lista completa
  de fuentes de identidad (Vault, Infisical, Keycloak, SPIFFE, los kinds de
  federación de identidad de agentes).
