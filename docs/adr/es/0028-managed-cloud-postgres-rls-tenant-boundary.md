> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0028: Base de datos de la nube gestionada — PostgreSQL gestionado, con row-level security como frontera entre tenants

- **Status:** accepted (managed cloud; this record creates no infrastructure)
- **Date:** 2026-08-02
- **Deciders:** Fran Olivares
- **References:** ADR-0005 (SQLite by default, PostgreSQL at scale), ADR-0027
  (managed-cloud ingress), ADR-0029 (managed-cloud regions), ADR-0022 (source-scoping
  subject axes); the platform decision record for the managed cloud; PostgreSQL
  documentation on row security policies and the AWS database guidance on multi-tenant
  isolation with row-level security, consulted 2026-08-02:
  `https://aws.amazon.com/blogs/database/multi-tenant-data-isolation-with-postgresql-row-level-security/`.

## Contexto y planteamiento del problema

El ADR-0005 ya estableció PostgreSQL como base del producto a escala, y el producto ya dispone
de la maquinaria de row-level security para delimitar por tenant. La nube gestionada no necesita un
modelo de datos nuevo; necesita una decisión sobre **quién opera la base de datos** y sobre
**en qué se confía exactamente para mantener las filas de un tenant fuera del alcance de
otro**.

La segunda parte importa más que la primera. «Usamos row-level security» no es una propiedad
real hasta que los roles se organizan de forma que las políticas se apliquen de verdad.
PostgreSQL excluye de las políticas de tabla a dos categorías de llamantes: los superusuarios
y los roles con el atributo `BYPASSRLS`; además, por defecto, **el propietario de una tabla
elude por completo las políticas RLS de esa tabla a menos que esta se modifique con
`FORCE ROW LEVEL SECURITY`**. Por tanto, una aplicación que se conecta con el rol que creó el
esquema carece de aislamiento entre tenants aunque parezca tenerlo. Este es el error más
costoso que permite este diseño, y es silencioso.

## Motivadores de la decisión

- El aislamiento entre tenants debe aplicarlo **la base de datos**, no la diligencia de cada
  consulta futura.
- El único operador no debería tener que operar PostgreSQL: aplicar parches, gestionar la
  conmutación por error y realizar la recuperación a un instante concreto son precisamente el
  trabajo que la oferta gestionada pretende eliminar.
- La recuperación debe ser una propiedad de la plataforma, no de un runbook que alguien tenga
  que acordarse de ejecutar.
- Todo lo que se afirme sobre el aislamiento debe poder **probarse desde fuera de la
  aplicación**.

## Opciones consideradas

- **A — PostgreSQL autogestionado en máquinas virtuales.** Control total, el menor coste
  unitario y todas las actualizaciones, simulaciones de conmutación por error y verificaciones
  de backups pasan a ser responsabilidad nuestra.
- **B — el servicio PostgreSQL gestionado del proveedor de nube, multi-AZ**, con backups
  automatizados y recuperación a un instante concreto.
- **C — el servicio de clúster compatible con PostgreSQL del proveedor** (arquitectura de
  almacenamiento compartido, facturación de E/S por petición en la configuración estándar).
- **D — una plataforma PostgreSQL de terceros** accesible desde la misma región.

## Resultado de la decisión

Opción elegida: **B — PostgreSQL gestionado, multi-AZ**, con row-level security como frontera
entre tenants y con la siguiente disposición de roles considerada parte de la decisión, no un
detalle de implementación.

La disposición de roles es normativa:

1. La aplicación se conecta con un rol que **no es propietario** de las tablas con ámbito de
   tenant y **no tiene `BYPASSRLS`**.
2. Todas las tablas con ámbito de tenant llevan **`FORCE ROW LEVEL SECURITY`**, de modo que la
   mera propiedad no pueda eludir una política; esto protege frente a una migración futura que
   cambie el propietario de una tabla.
3. El rol administrativo utilizado para las migraciones no es el rol incluido en la cadena de
   conexión de la aplicación.
4. **Alcance, expresado para que nunca se dé por supuesto:** este registro rige el **plano
   de datos del tenant**: el esquema que contiene filas propiedad del tenant, donde el motor
   ya emite `ENABLE ROW LEVEL SECURITY`, `FORCE ROW LEVEL SECURITY` y una política por tenant
   vinculada a una configuración de sesión. Los **metadatos de control propios** del plano
   gestionado (registro de tenants, libro mayor de facturación, instantáneas de uso) están en
   un **esquema separado con una postura separada**: hoy se apoyan en el scoping en la capa
   de aplicación, con un único rol de aplicación y sin SQL expuesto a tenants. Esa puede ser
   perfectamente la respuesta correcta para los metadatos de control, pero actualmente es
   una postura **heredada en vez de decidida**, y no es lo que «usamos row-level security»
   da a entender al lector. Quien construya el plano gestionado debe **dejar por escrito qué
   postura tiene ese esquema y por qué** antes de que almacene los registros de un cliente
   de pago.

### Consecuencias

- **Bueno:** la aplicación de parches, la conmutación por error multi-AZ, los backups
  automatizados y la recuperación a un instante concreto se convierten en propiedades de la
  plataforma. El runbook de recuperación ante desastres que distribuye el producto sigue
  siendo el artefacto para los despliegues self-hosted; deja de ser una tarea operativa diaria
  del plano gestionado.
- **Bueno:** el aislamiento pasa a ser comprobable desde el exterior. El criterio de
  aceptación es una consulta ejecutada **como el rol de la aplicación** que intenta leer las
  filas de otro tenant y no obtiene ninguna, no una afirmación en un documento de diseño.
- **Malo / compromisos:** un coste fijo mensual mínimo superior al de una máquina virtual
  sencilla, y las actualizaciones de versión del motor llegan según el calendario del
  proveedor, no el nuestro.
- **Neutral:** el rol administrativo del servicio gestionado es un rol privilegiado de la base
  de datos, **no** un superusuario de PostgreSQL: no tiene acceso al sistema operativo ni puede
  reescribir la configuración de autenticación del host. Es una reducción útil del radio de
  impacto, pero no es lo que hace que se cumpla la row-level security; lo consigue la
  disposición de roles anterior.
- **Explícitamente NO verificado, y no debe darse por supuesto:** si ese rol administrativo
  tiene `BYPASSRLS` en el motor en ejecución. Se comprueba con una única consulta
  (`SELECT rolbypassrls FROM pg_roles WHERE rolname = current_user;`) contra una instancia real,
  y corresponde a la fase que cree la primera. Hasta que se ejecute, ningún documento debe
  afirmar que el rol administrativo está sujeto a las políticas de tenant.

## Por qué se rechazaron las alternativas

- **A (PostgreSQL autogestionado)** — rechazada porque devuelve exactamente la carga operativa
  que el plano gestionado existe para absorber, concentrada en un único operador:
  actualizaciones de versión, simulacros de conmutación por error y una verificación de backups
  que solo es real si alguien restaura desde ellos periódicamente. Su ventaja de coste es real
  y pequeña en términos absolutos; la exposición operativa, en cambio, no es pequeña.
- **C (servicio de clúster compatible con PostgreSQL)** — rechazada por prematura. La carga de
  trabajo es un esquema transaccional pequeño con una tasa de escritura moderada; la
  arquitectura de almacenamiento compartido resuelve problemas de escalado que esta carga no
  tiene, con un coste mínimo superior y facturación de E/S por petición en la configuración
  estándar. Sigue siendo la vía de actualización natural si la tasa de escritura llega a
  justificarla.
- **D (plataforma PostgreSQL de terceros)** — rechazada para el store primario. El
  comportamiento de row-level security, el modelo de superusuario y los atributos de rol
  disponibles varían según el proveedor, y cada uno tendría que volver a verificarse contra la
  propiedad de aislamiento anterior. No hay ningún motivo para asumir un riesgo específico de
  proveedor en la única frontera que no debe fallar.
