---
title: "Tu primera hora con Olivares AI"
description: >-
  Instala Olivares AI, inicia sesión con una herramienta de IA o conecta un
  proveedor, abre una conversación en la carpeta de tu proyecto, aprueba una
  acción, detén y reanuda.
---

Necesitas Docker Compose, una carpeta de proyecto sobre la que quieras que la
herramienta trabaje y, además, una suscripción a una herramienta, una clave de
API de un proveedor o un modelo local.

## 1. Instala y abre la consola

Sigue la [instalación del README](https://github.com/olivaresai/olivares#install).
Selecciona la imagen de la versión de forma explícita, monta la carpeta de tu
proyecto del host en `/project` e imprime una dirección de consola y un token de
configuración de un solo uso de sustitución. Usa allí las instrucciones de
AppArmor cuando se apliquen a tu host Docker.

Abre la dirección impresa. La primera instalación usa un certificado
autofirmado; comprueba que estás abriendo tu propio servidor antes de aceptar la
advertencia del navegador. Introduce el token de configuración, tu correo y una
contraseña para crear el primer administrador. Mantén el token y la contraseña
en privado. La consola inicia tu sesión y abre el asistente de configuración.

El contenedor ejecuta las herramientas en el servidor. Una herramienta con
sesión iniciada en tu portátil es otra instalación; su inicio de sesión no se
copia al contenedor.

## 2. Elige una herramienta y su inicio de sesión

Usa el asistente o abre **Herramientas de IA** (`/agent-tools`). Cada herramienta
muestra si está instalada y si tiene la sesión iniciada. Si falta, revisa y
aprueba su instalación. Usa el control de inicio de sesión de la herramienta y
completa su propio flujo de inicio de sesión. En Claude Code o Codex, aprueba el
inicio de sesión con tu cuenta de Claude o de ChatGPT.

En una instalación nativa, la instancia predeterminada usa el inicio de sesión
del usuario que ejecuta el motor. Los inicios de sesión hechos a través de
Olivares siguen siendo instancias separadas. Las herramientas informan de la
cuenta, el plan, los modelos y las ventanas de uso que exponen. Un valor no
disponible no es un saldo cero; si una actualización falla, la última
instantánea aparece como obsoleta, junto con el comando que falló.

Para usar una clave de API, abre **Herramientas**, añade el proveedor en
**Claves de API** y prueba la conexión. Para un modelo local, añade un endpoint
de Ollama accesible desde el motor; no hace falta clave de API. Consulta
[Añadir un proveedor](/how-to/add-a-provider/). Para una herramienta que requiere
un modelo, como Codex, selecciona uno de los que devuelve la prueba de conexión
si el proveedor no tiene un modelo predeterminado guardado. Claude Code puede
usar su modelo predeterminado nativo.

## 3. Inicia una conversación en tu proyecto

1. Abre **Sesiones** (`/sessions`) y elige **Nueva sesión**.
2. Elige una herramienta lista. El formulario muestra una carpeta y la rellena
   por ti. Déjala así para una sesión en su propia carpeta, o elige **Cambiar
   carpeta** e introduce `/project` para usar la carpeta del host montada por la
   instalación del README.
3. Deja **Primer mensaje** vacío y pulsa **Iniciar**. Puedes escribir tu mensaje
   en la conversación una vez abierta. El motor rellena un perfil y las rutas
   de inicio; no necesitas registrarlos para esta primera sesión.

El usuario del contenedor, UID 65532, necesita acceso de escritura a la carpeta
del proyecto del host. Las [instrucciones de carpeta de proyecto](https://github.com/olivaresai/olivares/blob/main/deploy/compose/README.md#work-on-a-host-project-folder)
explican los permisos. Una sesión puede cambiar el contenido de la carpeta
montada; usa el directorio de un proyecto, no tu directorio personal completo.

Pide a la herramienta que explique el proyecto. Lee su respuesta en la
conversación. Si **Iniciar** no puede continuar, el formulario indica qué falta.
Sigue esa indicación en **Herramientas** y vuelve después a la sesión.

## 4. Aprueba una acción

Para probar una aprobación, elige **Más opciones → Pedir confirmación antes de
cada acción** al iniciar la sesión. Pide a la herramienta que ejecute en el
proyecto un comando inofensivo. Cuando solicite permiso, abre **Reglas**. La
solicitud que espera tu respuesta aparece marcada en naranja y abierta junto a
la cola, con el comando, a qué afecta y la regla que la pidió. Elige **Aprobar
una vez** (o pulsa `A`), o **Rechazar** (`D`) y añade una nota. Vuelve a la
conversación para leer el resultado. Una política de la organización también
puede exigir aprobación antes de que se inicie una sesión; un aprobador debe
aceptar primero esa solicitud.

## 5. Detén y reanuda

Elige **Detener** en la sesión y confirma si se te pide. El proceso de la
herramienta termina; la conversación permanece. Elige **Reanudar** en esa misma
sesión y envía otro mensaje. Reanudar mantiene el perfil, la cuenta y la
carpeta seleccionados, y comprueba que todavía se pueden usar.

Para la operación continua, la CLI y los ajustes opcionales de perfil, consulta
[Operar una sesión de proveedor](/how-to/operate-provider-sessions/).

## Mensajes de inicio nativo

Otros métodos de instalación tienen límites de cualificación propios; consulta
[Instalación](https://github.com/olivaresai/olivares/blob/main/INSTALL.md#installation-qualification).
Un inicio rápido nativo imprime la dirección de la consola y el siguiente paso
según el estado de su configuración. Estos son los mensajes para una instalación
nueva, una que vuelve y una con la configuración pendiente:

```text
Next: Open the console; it guides setup, sign-in and your first session.
Next: Open the console and sign in to continue your work.
Next: Open the console to finish setup with the one-time token issued earlier.
```

Cuando no hay configurada una credencial de inferencia para todo el host, el
registro de inicio del motor incluye:

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

Esto limita los perfiles de Claude que usan inyección gestionada sin un
proveedor vinculado. Una clave de proveedor vinculada o el inicio de sesión
autorizado de la herramienta es una fuente de credencial distinta; consulta
[Operar una sesión de proveedor](/how-to/operate-provider-sessions/#choose-how-the-tool-connects).
