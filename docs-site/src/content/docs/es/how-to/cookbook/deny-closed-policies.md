---
title: "Receta: políticas deny-closed (Cedar / OPA)"
description: >-
  Cablea el punto de decisión de políticas en modo solo-restringir: un overlay
  forbid de Cedar o una política OPA permit-by-default, validada y en dry-run
  antes de publicar — políticas que solo pueden quitar acceso, nunca ampliarlo.
sidebar:
  order: 1
---

**Objetivo:** añadir restricciones basadas en atributos sobre un RBAC
deny-by-default — por ejemplo, "nadie toca los recursos etiquetados como
`secret`, diga lo que diga su rol".

El invariante que debes tener siempre en mente: el PDP **solo restringe**. La
decisión se compone como RBAC ∩ ABAC nativo ∩ PDP externo — una política nunca
puede conceder lo que el modelo de roles deniega
([el modelo](/es/how-to/govern-and-approve/#el-seam-de-política-abacpdp-solo-restringe)).

## Cedar (embebido, principal)

Selecciona el motor y apúntalo a tu fichero de políticas, luego reinicia:

```bash
OLIVARES_PDP_ENGINE=cedar
OLIVARES_PDP_CEDAR_FILE=/etc/olivares/policy.cedar
```

Una política Cedar es un **overlay forbid** — el permit base hace de "RBAC ya
ha decidido", y tus reglas `forbid` restan:

```cedar
permit(principal, action, resource);

forbid(principal, action, resource)
  when { resource.kind == "credential" && resource.sensitivity == "secret" };
```

Dos hechos de autoría, verificados contra el adaptador: `resource.kind` y
`resource.sensitivity` están siempre presentes en la entrada de decisión
(referenciables sin condición); cualquier otro atributo debe protegerse con
`has()` o la regla no podrá hacer match. Un `permit` que escribas nunca puede
ampliar la decisión.

## OPA (sobre HTTP)

```bash
OLIVARES_PDP_ENGINE=opa
OLIVARES_PDP_OPA_URL=http://opa.internal:8181
OLIVARES_PDP_OPA_PATH=/v1/data/olivares/decision
OLIVARES_PDP_OPA_TOKEN=<bearer-reference>     # optional
```

Escribe el Rego **permit-by-default**:

```rego
package olivares

default allow := true

allow := false if {
  input.resource.sensitivity == "secret"
  input.action == "read"
}
```

`true` = sin restricción. `false`, un resultado ausente, o **cualquier error de
transporte o respuesta no-2xx falla cerrado** — la petición se deniega, nunca
queda silenciosamente sin gobernar.

## Validar, dry-run, publicar

El módulo de governance expone un ciclo de vida de políticas para que una
política defectuosa nunca aterrice a ciegas:

```bash
# Compile-check the source:
curl -ks -X POST "$BASE/v1/m/governance/pdp/validate" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d @policy.json

# Pre-flight a decision WITHOUT audit side effects:
curl -ks -X POST "$BASE/v1/m/governance/pdp/dry-run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"principal":"…","action":"…","resource":{"kind":"credential","sensitivity":"secret"}}'

# Then publish (policy-admin permission):
curl -ks -X POST "$BASE/v1/m/governance/pdp/publish" …
```

`GET /v1/m/governance/pdp/versions` lista lo que está desplegado;
`POST /v1/m/governance/pdp/explain` explica una decisión.

## Verifica las propiedades de seguridad

- Reinicia con un fichero de políticas **inválido**: el motor deshabilita solo
  el PDP externo y lo registra — el RBAC y el ABAC nativo siguen gobernando; el
  control plane no se cae.
- Cada restricción que aplica el PDP queda **auditada** — comprueba el ledger
  tras una petición denegada.

## Notas

- Las políticas se versionan y publican, no son ficheros editados en caliente
  en producción — trata la publicación como un cambio revisado.
- Para acciones gobernadas por aprobación (en lugar de denegadas), consulta
  [aprobaciones HITL](/es/how-to/cookbook/hitl-approvals/).
