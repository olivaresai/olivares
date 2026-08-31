---
title: "Módulo XX — multi-tenancy y gestión de organizaciones"
description: >-
  El cimiento del aislamiento: cada entidad del núcleo lleva un tenant_id, y el
  almacén se niega a abrirse salvo que esa frontera se imponga en la capa de consultas.
  Lo que el modelo de datos garantiza hoy, y qué siguen siendo la jerarquía de
  organizaciones y la administración delegada.
---

El módulo XX no es un servicio que cuelga del motor: es una **propiedad del motor
mismo**. No hay un módulo de tenancy separado que enchufar; en su lugar, el modelo de
datos del núcleo lleva una frontera de tenant en cada entidad y el almacén la impone
por debajo de cada consulta. Esta página es la referencia de lo que esa frontera
garantiza hoy, y de las partes de la gestión de organizaciones que aún están en fase
de diseño.

## Qué es

La multi-tenancy vive en la capa de motor (capa 0), junto a la propia API de la
plataforma (módulo XIX), porque añadir el aislamiento a un modelo de datos ya en marcha
es la clase de cambio que no se puede hacer con seguridad más tarde. Cada entidad del
núcleo lleva un **`tenant_id`**, y un llamante nunca lo pasa como parámetro libre:
**fija el tenant una sola vez** y recibe un ámbito cuyos repositorios ya están ligados a
él. **No hay vocabulario en la API para cruzar tenants**: esa ausencia es la primera
barrera de aislamiento, antes que cualquier mecanismo de base de datos. El ámbito
privilegiado entre tenants (crear una org, listar orgs, eliminar un tenant) solo es
alcanzable **por el propio arranque del motor**, nunca por un módulo.

## El contrato y las entidades

El modelo de tenant lo posee el contrato del modelo de datos, no un esquema por módulo.
La entidad raíz es la **`Org`**, que *es* el tenant: cuando el motor siembra una org, su
identificador se convierte en el identificador de tenant y la propia cadena de auditoría
de la org se establece en ese mismo momento. Cualquier otra entidad del núcleo —agentes,
sesiones, recursos, identidades, políticas, registros de coste, hallazgos, despliegues,
el access map y el audit ledger— se crea **dentro** de un ámbito de tenant y se sella con
ese tenant en la escritura; el llamante no puede sobreescribirlo.

El aislamiento se impone en la capa de consultas, según el despliegue:

- En **PostgreSQL**, cada tabla que lleva `tenant_id` corre bajo `FORCE ROW LEVEL
  SECURITY` con una política `tenant_isolation` ligada por transacción. Una transacción
  que no consigue ligar un tenant **lanza un error** en lugar de devolver silenciosamente
  cero filas (fail-closed). El rol de aplicación no es superusuario y nunca tiene
  `BYPASSRLS`, y `FORCE` liga la política incluso para el propietario de la tabla. La
  propiedad, en cambio, es una elección de despliegue: la instalación single-role por
  defecto deja al rol de aplicación como **dueño de la base de datos** —RLS le sigue
  aplicando, pero un dueño puede alterar sus propias tablas, así que esa postura es
  capaz de *detectar alteraciones*, no *owner-proof*. La frontera dura de privilegio —un rol de
  aplicación que además no es propietario— viene de la topología split owner/app, donde
  un rol owner separado hace el provisioning y el rol de aplicación recibe solo el DML
  que necesita.
- En **SQLite** (el despliegue de nodo único) no hay seguridad a nivel de fila; la
  equivalencia viene de dos hechos: la *única* vía hacia la base de datos es el SQL
  generado por el descriptor, que siempre añade el predicado de tenant, y los **triggers
  trampa (tripwire)** abortan cualquier escritura cuyo tenant no coincida con el ámbito
  fijado.

Una **autocomprobación de arranque** consulta las guardas de aislamiento vivas tras
migrar y **se niega a abrir** el almacén si alguna tabla que lleva `tenant_id` queda
desprotegida, de modo que una guarda olvidada en una tabla nueva se convierte en un
fallo de arranque, no en una fuga silenciosa.

## Qué consume y qué produce

El módulo XX no tiene superficie en el bus de eventos ni actuación. No consume
`edge.observed`, no emite hallazgos ni llama a ningún proveedor: es el sustrato a
*través* del cual escriben los demás módulos. Su único efecto observable es estructural:
cada entidad que cualquier módulo persiste ya está acotada por tenant, y cada mutación
sobre una entidad auditada se añade al [audit ledger encadenado por hash
(hash-chained)](/es/reference/events/) de ese tenant dentro de la misma transacción.

:::caution[Límites honestos]
- **Lo que el modelo de datos modela de verdad es `Org`-como-tenant + la frontera de
  aislamiento**, no la jerarquía completa de organizaciones. **Equipos, proyectos,
  administración delegada, roles por nivel y uso/facturación por org están en fase de
  diseño**, no son entidades entregadas. Trata la garantía de tenancy del producto hoy
  como: *una org = un tenant aislado, impuesto en la capa de consultas.*
- **El aislamiento de lectura en SQLite lo da la capa de consultas, no el motor.** SQLite
  no tiene seguridad a nivel de fila: el acotado de lectura es una propiedad del SQL
  generado (las escrituras además están cubiertas por los triggers trampa). El
  multi-tenant **a escala es PostgreSQL con RLS** como respaldo a nivel de kernel; SQLite
  es el despliegue de nodo único / air-gapped.
- **El ámbito de administración entre tenants depende del despliegue en PostgreSQL.**
  Listar orgs a través de tenants necesita un rol de administración dedicado en
  PostgreSQL y concierne al despliegue, no al código de aplicación. Funciona
  directamente en SQLite (escritor único).
- **La tenancy no es administración delegada.** Quién puede actuar *dentro* de un tenant
  —roles, aprobaciones, segregación de funciones— lo gobierna el [módulo
  VI](/es/reference/modules/vi-governance/), no este. El módulo XX garantiza el muro entre
  tenants; el módulo VI custodia la puerta de dentro de uno.
:::

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/) — dónde encaja el módulo XX y su estado honesto de actuación.
- [Identidad, permisos y gobernanza](/es/reference/modules/vi-governance/) — roles y autoridad delegada dentro de un tenant.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — la capa de motor y el modelo de datos general.
- [Referencia del bus de eventos](/es/reference/events/) — el audit ledger por tenant al que se añade cada mutación.
- [Honestidad y límites](/es/start/honesty-and-limits/) — qué está construido hoy frente a lo que está en fase de diseño.
