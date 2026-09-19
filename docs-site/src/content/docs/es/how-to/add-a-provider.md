---
title: Añadir un proveedor y lanzar Claude Code, Codex o Grok
description: >-
  Registra una clave de API en el plano de control, prueba la conexión sin
  gastar nada, vincúlala a un perfil de proveedor y lanza la primera sesión
  — desde la consola y desde el CLI.
---

Esta página es la primera hora del plano de **proveedores**: dónde va tu clave de
API, cómo sabes que funciona y cómo arranca una sesión con ella.

Antes de v26.10 la respuesta a la primera pregunta era una variable de entorno en el
servidor. Esas variables siguen funcionando. Ya no son el único camino, y ya no son
como empieza una operadora nueva.

## Qué significan las tres palabras

Una frase para cada una, porque el producto las mezclaba y las negativas que te da
las nombran por separado.

| Palabra | Qué es |
|---|---|
| **Proveedor** | Una credencial: una clave de API, un endpoint opcional y el tipo al que pertenece (`anthropic`, `openai`, `xai`, `openai_compatible`). |
| **Perfil de proveedor** | Una identidad en esta máquina: qué CLI oficial corre, y bajo qué home de configuración y qué home de usuario. |
| **Sesión** | Un proceso hijo lanzado, bajo un perfil, usando la credencial de un proveedor. |

Un proveedor por sí solo no lanza nada. Un perfil sin proveedor solo lanza si las
variables del host resultan estar puestas. Lo que hace que arranque una sesión es el
vínculo entre los dos.

## 1. Añade el proveedor

### Desde la consola

1. Abre **Proveedores** (IA → Entornos → Proveedores).
2. Elige **Añadir proveedor**.
3. Elige el proveedor, ponle un nombre que reconozcas en un selector y pega la clave.
   Deja el endpoint vacío salvo que apuntes a tu propio gateway; un proveedor
   `openai_compatible` exige uno, porque no hay ningún endpoint oficial que suponer.
4. Confirma. La escritura necesita una sesión AAL3, como cualquier otra credencial de
   este producto; la consola muestra la ceremonia en vez de una negativa.

El motor sella la clave en reposo y devuelve una pista de cuatro caracteres. **La
clave no se devuelve nunca más**, ni siquiera justo después de escribirla. Si la
pierdes, rótala: no hay ninguna lectura que la recupere.

### Desde el CLI

```sh
# La clave se lee de stdin. Nunca es el valor de un flag: un flag deja la credencial
# en el historial del shell y en la tabla de procesos.
olivares provider add --kind anthropic --name "Anthropic (prod)" < key.txt

# O desde una variable de entorno de tu propio shell:
ANTHROPIC_KEY=sk-ant-... olivares provider add \
  --kind openai --name "Codex" --key-env ANTHROPIC_KEY
```

## 2. Prueba la conexión

```sh
olivares provider test prv_01J8ABCDEF
```

La prueba le pregunta al proveedor qué modelos sirve. **No envía ninguna compleción y
no gasta nada.**

Contesta una de tres cosas, y son tres preguntas distintas:

| Resultado | Qué significa | Qué hacer |
|---|---|---|
| `ok` | El proveedor contestó y aceptó la credencial. | Nada. Vincúlala. |
| `refused` | El proveedor contestó y rechazó la credencial. | Rota la clave. |
| `unreachable` | No se obtuvo respuesta. | Revisa el endpoint, la red y cualquier proxy. **Esto no dice nada sobre la clave**: no la regeneres. |

Un proveedor que no has probado se muestra como **sin probar**, nunca como
funcionando. Registrar una credencial es una intención; una prueba es un hecho.

## 3. Registra un perfil y vincula el proveedor

Los homes del perfil tienen que existir ya en la máquina que corre el plano de
control. El servidor los valida allí y nunca crea uno que falte: un home vacío de
repuesto le daría a una sesión una identidad de proveedor que nadie configuró.

```sh
olivares agent profile create \
  --driver claude \
  --config-home /home/ops/.claude \
  --user-home /home/ops \
  --name "Claude (trabajo)" \
  --auth-source managed_injection \
  --provider prv_01J8ABCDEF
```

`--auth-source` decide de dónde sale la identidad de proveedor del hijo, y los dos
valores no son una cadena de respaldo:

- `provider_account_home`: el login ya guardado dentro de los homes del propio perfil.
  Olivares no inyecta nada y nunca lee ese fichero.
- `managed_injection`: una credencial que suministra el motor. Con un proveedor
  vinculado, es la de ese proveedor.

Hay un atajo de un solo verbo que hace la detección, el registro y el vínculo a la
vez, tomando por defecto los homes propios de ese driver bajo tu `$HOME`:

```sh
olivares agent deploy claude --provider prv_01J8ABCDEF
```

Informa de cuatro estados y no son el mismo problema: **sin instalar** (y nombra el
comando de instalación en vez de ejecutarlo), **instalado**, **perfil listo**,
**lanzable**. Nunca ejecuta un login del proveedor ni crea un home que falte.

Para vincular (o revincular) después:

```sh
olivares provider bind prv_01J8ABCDEF --profile ppf_01J8ZZZZZZ
```

El motor rechaza una credencial que el driver del perfil no puede leer. Una clave de
OpenAI en un perfil de Claude es una negativa que nombra a los dos, al vincular y otra
vez al lanzar; no una sesión que falla a mitad de un handshake.

| Tipo de proveedor | Drivers que lo leen |
|---|---|
| `anthropic` | `claude`, `opencode` |
| `openai` | `codex`, `opencode` |
| `xai` | `grok`, `opencode` |
| `openai_compatible` | todos, con su endpoint |

## 4. Lanza la primera sesión

```sh
olivares agent workspace add /srv/projects/acme --name acme --mode ro --dlp deny
olivares agent session create \
  --name acme-1 \
  --workspace ws-123 \
  --provider-profile ppf_01J8ZZZZZZ
olivares agent session attach run-123
```

`--provider-profile` es **obligatorio**: el servidor no selecciona implícitamente un
perfil, un home ni un entorno.

Desde la consola, el mismo camino es **Onboarding → Agentes y primera sesión**, o
**Sesiones → Nueva sesión**.

## Rotación y revocación

```sh
olivares provider rotate prv_01J8ABCDEF < nueva-clave.txt   # resella en el sitio
olivares provider rm prv_01J8ABCDEF --yes                   # irreversible
```

La rotación sustituye el valor bajo la misma referencia, así que todos los perfiles
vinculados siguen funcionando y el **siguiente** lanzamiento usa la clave nueva. Una
sesión que ya está corriendo conserva la credencial con la que arrancó. Se borra la
prueba de conexión anterior: un veredicto medido sobre una credencial que ya no existe
no es prueba sobre la que la sustituye.

La revocación destruye el valor sellado y rechaza **por nombre** todos los
lanzamientos futuros. El registro y los vínculos se conservan a propósito: un perfil
que dejara de nombrar nada en silencio se leería como un perfil que nadie configuró.
Revocar aquí no revoca la clave en el proveedor; eso se hace en su propia consola.

## Qué rechaza el motor, y por qué

| Ves | Significa |
|---|---|
| `provider credentials cannot be stored on this deployment` | No hay ningún almacén sellado conectado. El motor se niega a guardar una clave en vez de guardar una que no puede proteger. |
| `the provider connection test is not available` | No hay probe conectado. **Lanzar no se ve afectado.** |
| `a … credential is not readable by driver …` | El tipo y el driver no casan. Mira la tabla de arriba. |
| `the provider this profile is bound to is revoked` | Vincula un proveedor activo. |
| `this launch has two endpoints` | Aplican a la vez el gateway de inferencia del despliegue y el `base_url` propio del proveedor. Quita uno; el motor no elige. |
| `the registered provider credential could not be opened` | El lanzamiento se deniega. **No recae en una credencial del host**: eso correría tu sesión con una cuenta que no elegiste. |

## Las variables de entorno, y dónde siguen aplicando

`OLIVARES_SESSION_RUNTIME_WIF` y `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` no cambian y
siguen soportadas. Son la credencial de todo el host y aplican a cualquier perfil que
**no** nombre un proveedor.

Un perfil que sí nombra uno usa ese. Gana la selección más específica, y no hay
respaldo desde ella: una credencial vinculada que no se puede producir deniega el
lanzamiento en vez de usar calladamente la del despliegue.

## Relacionado

- [Operar una sesión de proveedor](/es/how-to/operate-provider-sessions/)
- [Tu primera hora](/es/how-to/first-hour/)
- [Referencia del CLI](/es/reference/cli/)
