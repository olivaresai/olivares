---
title: "Endurecer un despliegue"
description: >-
  Pasos de operador para ejecutar Olivares AI de forma segura: conserva los valores
  por defecto seguros, gobierna las acciones destructivas con aprobaciones
  human-in-the-loop, verifica una release antes de ejecutarla y mantén tu evidencia
  fuera de la máquina. Postura defensiva, por diseño.
---

Esta es la **guía de endurecimiento del operador**: los pasos concretos para ejecutar el control
plane de forma segura. Se sitúa *por encima* de las páginas explicativas —el
[modelo de seguridad](/es/explanation/security/security-model/) y el
[modelo de amenazas](/es/explanation/security/threat-model/) explican los activos, las fronteras de
confianza y por qué la postura es la que es—. Esta página es el *cómo*.

:::note[Defensivo por diseño]
Olivares AI es un producto defensivo. Te ayuda a **gobernar tu propio estate**; no es
un framework de mando y control y no escanea credenciales de nadie más. Leer el
access map es una acción privilegiada, con ámbito por tenant y **auditada** (rol editor en
adelante, nunca el viewer más bajo). Esta guía endurece el despliegue —no te enseña a mapear un
estate que no posees.
:::

## 1. Conserva los valores por defecto seguros

Una instalación nueva es segura por defecto. El trabajo aquí es sobre todo *no debilitarla*.

| Por defecto | Consérvalo porque | Acción del operador |
|---|---|---|
| **Sin credenciales por defecto** | El footgun nº 1 del self-hosted. El primer arranque emite un **token de configuración de un solo uso**; creas con él al primer administrador. | Lee el token de la salida de arranque (o los logs del contenedor), crea el admin, y entonces se consume. Nunca incrustes una credencial en una imagen. |
| **TLS activo por defecto** | Los canales colector→core y usuario→panel transportan metadatos sensibles. | Deja el TLS activo. `--insecure` (texto plano) es **solo para desarrollo en localhost** —nunca en un bind expuesto. |
| **Bind en loopback** | El motor se liga a loopback por defecto para que nunca quede expuesto accidentalmente. | Exponlo **deliberadamente**, detrás de tu propio ingress/TLS. En contenedores el proceso se liga dentro del contenedor y el stack de Compose mapea el puerto del host a loopback —ver [self-hosting](/es/how-to/self-hosting/). |
| **Sin telemetry-home** | Una herramienta de seguridad que llama a casa es un pasivo. | Sin acción —el motor no hace llamadas salientes obligatorias en el arranque. En modo air-gapped el egress es cero. |

Cada desviación peligrosa de los valores por defecto es un **opt-in nombrado y explícito** (por
ejemplo el flag de texto plano para desarrollo, o permitir un rol de base de datos privilegiado). Si
no configuraste ninguno, está desactivado. La postura completa de valores seguros por defecto y las
garantías criptográficas del audit ledger están en el [modelo de seguridad](/es/explanation/security/security-model/).

### TLS mutuo para colectores remotos

En la topología distribuida, los colectores de borde envían observaciones al core mediante
**TLS mutuo** con certificado de cliente verificado. Actívalo dando al core una CA de cliente para que
**exija y verifique** un certificado de cliente:

```bash
./bin/olivares serve \
  --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444 \
  --grpc-client-ca /path/to/collector-ca.pem \
  --data-dir /var/lib/olivares
```

Los colectores se ejecutan en **tu** infraestructura **sin listener entrante** (un modelo de push
puro), así que no añaden puertos abiertos a tus hosts de producción. Protege y respalda el
directorio de datos (permisos restrictivos) —contiene la clave de firma de auditoría y el material
TLS— y mantén una copia fuera de la máquina de la clave pública de auditoría.

## 2. Gobierna las acciones destructivas con aprobaciones human-in-the-loop

El control plane está gobernado por un core de autorización **deny-by-default** (RBAC, con un
policy decision point Cedar/OPA opcional de solo-restricción que únicamente puede *quitar acceso*,
nunca ampliarlo). Para el modelo —roles, el seam de política y la garantía de decisiones registradas—
consulta [gobernar y aprobar](/es/how-to/govern-and-approve/). Los pasos operativos:

1. **Cablea el gate de aprobación.** Cualquier acción de módulo que mutaría tu infraestructura
   (un apply de despliegue, un disparo de orquestación, una apertura de voz) pasa por un
   gate de aprobación human-in-the-loop que abre una aprobación gobernada ligada al plan
   exacto, deny-closed y con límite de tiempo. Se habilita proporcionando la configuración del
   puente; sin ella, esas acciones permanecen deny-closed.
2. **Usa una cuenta de servicio aprobadora dedicada —nunca la de un humano.** El componente que
   *abre* aprobaciones debe ejecutarse como su **propia cuenta de servicio, que nunca está en el
   pool de aprobadores**. La separación de funciones se aplica del lado del motor: la identidad que abrió
   una petición no puede decidirla, y un token de sistema no puede aprobar en absoluto. Si la cuenta del
   que abre es también aprobadora, creas un interbloqueo de liveness —así que mantenlas separadas.
3. **Los aprobadores deciden, el ledger recuerda.** Un humano autorizado aprueba o rechaza;
   la decisión se anexa al ledger con alteraciones detectables y con el actor real en la misma
   transacción. Una petición caducada nunca puede recibir una decisión vinculante. No puedes hacer
   un cambio gobernado que el ledger olvide silenciosamente.

Las rutas de aprobación viven bajo el namespace del módulo de gobierno y están sujetas al
mismo RBAC deny-by-default y a la misma auditoría por lectura que todo lo demás.

## 3. Verifica una release antes de ejecutarla

Un control plane es un producto de seguridad —demuestra que una release es la que el proyecto publicó
antes de ejecutarla—. La cadena completa (firma sobre los checksums, procedencia SLSA, SBOM
y atestaciones OpenVEX, keyless online o totalmente offline) está en
[verifica lo que descargaste](/es/how-to/verify-a-release/). La única regla que no tiene
excepciones:

:::danger[Nunca `curl | bash`]
No canalices un instalador hacia una shell. Descarga los artefactos, **verifícalos**, y solo
entonces ejecútalos. Despliega imágenes de contenedor y el chart de Helm **por digest**, nunca por una
etiqueta mutable.
:::

## 4. Mantén tu evidencia —y tus datos— en tu perímetro

- **Exporta el ledger fuera de la máquina.** El audit ledger append-only, hash-chained y firmado con
  Ed25519 se expone como una exportación **pull** autenticada en varios formatos SIEM, de modo que tu
  SIEM o store WORM conserve una copia inmutable que re-verifica la cadena offline. La
  copia fuera de la máquina es el control real frente a un host completamente comprometido —ver
  [reenviar la auditoría a Splunk](/es/how-to/forward-audit-to-splunk/).
- **Sin telemetría obligatoria ni egreso del plano de control de forma predeterminada.** El
  data plane (los colectores) siempre se ejecuta en tu infraestructura, y el access map
  almacena **relaciones, nunca payloads, secretos o PII** —datos mínimos es una propiedad
  del cable, no un ajuste—. Solo cruza tu perímetro lo que **tú** configuras para que lo
  cruce: llamadas a tus API de modelos, las salidas SIEM/webhook que conectas (incluida la
  exportación fuera de la máquina descrita arriba) y un proveedor externo de embeddings si
  aprovisionas uno. Este es el argumento estructural para la residencia de datos, el RGPD y
  la operación air-gapped; describe la arquitectura y tu configuración, **no es una garantía**.

## Relacionado

- [Modelo de seguridad](/es/explanation/security/security-model/) — privilegio, ámbito por tenant, auto-auditoría, datos mínimos.
- [Modelo de amenazas](/es/explanation/security/threat-model/) — activos, fronteras de confianza y qué puede atestar cada nivel de cobertura.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — el modelo RBAC/PDP y el flujo de aprobación en profundidad.
- [Verifica lo que descargaste](/es/how-to/verify-a-release/) — la cadena completa de verificación de release.
- [Self-hosting](/es/how-to/self-hosting/) y [instalación air-gap](/es/how-to/air-gap-install/) — las topologías de despliegue.
