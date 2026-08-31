---
title: "Copia de seguridad y restauración (DR que se demuestra a sí misma)"
description: >-
  Copias de seguridad cifradas y seguras para la continuidad del ledger con
  olivares dr: bundles programados para SQLite y Postgres, la restauración que
  verifica la cadena, el simulacro que puedes ejecutar sin tocar producción — y
  las dos claves que deciden si tu evidencia sobrevive.
---

La copia de seguridad de un control plane tiene un trabajo más difícil que la mayoría:
debe volver con su **ledger con alteraciones detectables demostrablemente intacto**. `olivares dr`
está construido en torno a ese requisito — cada bundle registra las puntas de cadena
por tenant, la restauración **falla con código distinto de cero si el ledger restaurado
no es seguro para la continuidad**, y el subcomando de simulacro demuestra que un bundle
es restaurable sin tocar producción.

El bundle se cifra bajo una **KEK que tú proporcionas** — una passphrase derivada con
Argon2id (`--passphrase-file`) o una clave en bruto de 32 bytes de tu KMS
(`--kek-key-file`); se requiere exactamente una. Las claves de firma de auditoría y de
catálogo viajan **selladas** dentro del bundle.

## Hacer la copia de seguridad

**SQLite** (nodo único) — segura mientras `serve` está en ejecución (el snapshot usa
`VACUUM INTO`; el WAL permite la lectura concurrente):

```bash
olivares dr backup \
  --data-dir /var/lib/olivares --engine sqlite \
  --out /backups/olivares-dr-$(date -u +%Y%m%dT%H%M%SZ).drbundle \
  --passphrase-file <your-dr-passphrase-file>
```

**Postgres** — un `pg_dump --format=custom` consistente impulsado por el mismo comando
(`--engine postgres --dsn … --admin-dsn …`), o entrégale un dump prefabricado con
`--snapshot-file`. Ejecutar el dump directamente **requiere `--admin-dsn`**: `pg_dump`
mantiene `row_security=off` y **aborta** como rol de aplicación contra las tablas
`FORCE ROW LEVEL SECURITY`, así que el comando rechaza de entrada en vez de no producir
nada. Para un RPO casi nulo, `--pitr-ref` produce un bundle compañero de claves+manifiesto que
se empareja con tu configuración PITR de archivado de WAL
(`deploy/postgres/backup/pitr-setup.md`); los scripts envoltorio
`deploy/postgres/backup/pg-dump.sh` / `pg-restore.sh` empaquetan el mismo flujo.

Dos interruptores de honestidad que conviene conocer:

- La copia de seguridad **se niega a capturar un ledger que no verifica** en el momento
  del backup — `--allow-unverified` existe, queda registrado y no se recomienda.
- Con un snapshot **prefabricado** (`--snapshot-file`/`--pitr-ref`) y sin `--admin-dsn`
  (un rol dedicado `NOSUPERUSER BYPASSRLS`), la copia advierte de que el conjunto de
  tenants capturado puede estar limitado por RLS e **incompleto** — el dump en sí es
  correcto; lo que necesita el rol de administrador es el inventario cross-tenant del
  manifiesto. (Ejecutar `pg_dump` *directamente* es un caso distinto: se rechaza de
  plano, ver arriba.)

**Programación:** el stack de Compose incluye un
[perfil de backup](/es/tutorials/getting-started/docker-compose/#3-copias-de-seguridad-dr-cifradas-el-perfil-backup),
el chart de Helm un
[CronJob](/es/tutorials/getting-started/kubernetes/#4-copias-de-seguridad-cifradas-programadas);
en bare metal, programa con cron el comando de arriba. Tu calendario **es** tu RPO:

| Nivel | Mecanismo | RPO | RTO |
|---|---|---|---|
| SQLite | `dr backup` con cron | el intervalo de cron | < 15 min |
| Postgres lógico | `pg-dump.sh` con cron | el intervalo de cron | < 30 min |
| Postgres PITR | base backup + archivado de WAL | ≈ segundos | < 30 min |

Refleja los bundles **fuera del sitio** y mantén la KEK **separada de los bundles**
(3-2-1): una copia en el mismo host no es recuperación ante desastres, y un bundle que
viaja con su passphrase no está cifrado en ningún sentido que importe.

## Simulacro — antes de necesitarlo

`dr verify` demuestra que un bundle es restaurable **sin tocar tu directorio de datos**
(SQLite: verificación completa de la cadena en un directorio temporal; sale con código
distinto de cero si no es seguro):

```bash
olivares dr verify --in /backups/olivares-dr-<ts>.drbundle \
  --passphrase-file <your-dr-passphrase-file>
```

`dr inspect --in <bundle>` imprime el manifiesto (sin necesidad de KEK, sin mostrar
secretos) — qué motor, qué tenants, qué puntas de cadena. Ejecuta el simulacro con la
misma cadencia que la copia de seguridad; un backup no verificado es una esperanza, no
un control.

## Restaurar

```bash
olivares dr restore --in /backups/olivares-dr-<ts>.drbundle \
  --data-dir /var/lib/olivares --engine sqlite \
  --passphrase-file <your-dr-passphrase-file>
```

La secuencia de restauración es deliberada: primero las claves de firma (fail-closed al
sobrescribir — `--force` es la anulación explícita), luego el snapshot del store, y
después **arranca el store restaurado y demuestra la continuidad del ledger**, saliendo
con código distinto de cero si la cadena no es segura. Tras cualquier restauración,
vuelve a verificar contra tu pin de checkpoint **fuera de banda** — un snapshot más
antiguo restaurado puede pasar un recorrido ingenuo y aun así fallar la comparación
fuera de banda
([troubleshooting § ledger](/es/how-to/troubleshooting/#el-ledger-falla-la-verificación)).

## Las dos claves que lo deciden todo

| Clave | Regla |
|---|---|
| **La DR KEK** (passphrase o clave en bruto) | sin ella todo bundle es ruido. Guárdala en un sistema distinto al de los bundles; perder ambas a la vez es el modo de fallo |
| **`audit-signing.key`** (en el directorio de datos) | haz una copia fuera de banda en el aprovisionamiento — el motor solo **advierte** en el primer arranque, no hay escrow obligatorio, y una clave perdida deja el ledger permanentemente inverificable. Fija también la clave pública fuera de banda (`GET /v1/audit/pubkey`) |

Para la custodia basada en KMS de las propias claves de firma (envelopes BYOK, ceremonias
de rotación, `olivares keys`), consulta la
[referencia de la CLI](/es/reference/cli/); para los recorridos de los modos de fallo, la
[página de troubleshooting](/es/how-to/troubleshooting/).
