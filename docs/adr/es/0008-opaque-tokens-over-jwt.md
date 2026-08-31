> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0008: Tokens opacos en servidor, no JWT, para la autenticación propia

- **Status:** accepted
- **Fecha:** 2026-06
- **Decisores:** Olivares AI (confirmado mediante revisión adversarial)
- **Referencias:** contrato API/authz/auditoría (§2, decisión §13.2)

## Contexto y planteamiento del problema

Había que elegir el mecanismo de autenticación propio. El alcance inicial mencionaba
"sessions/JWT". Para un producto de seguridad, los modos de fallo de las credenciales de tipo bearer
—revocación, claims que portan secretos, riesgo de la librería de parsing— importan muchísimo.

## Factores de decisión

- Revocación inmediata.
- Ningún secreto transportado dentro del token.
- Superficie de ataque de parsing criptográfico mínima; seguro por defecto.

## Opciones consideradas

- **Tokens opacos en servidor** (un secreto aleatorio, almacenado hasheado, resuelto en el servidor).
- **JWT** para las sesiones propias.

## Resultado de la decisión

Opción elegida: **tokens opacos en servidor** para la autenticación propia. Los tokens llevan un prefijo
por propósito (`olvs_` sesión, `olvk_` clave de API); el servidor almacena solo un selector público
y un SHA-256 del secreto, comparando en tiempo constante. JWT queda confinado a la
costura (seam) de SSO/federación, no a las sesiones propias.

### Consecuencias

- **Bueno:** los tokens son revocables, no portan secretos y no requieren parsing criptográfico de
  claims suministrados por el atacante; seguro por defecto.
- **Malo / compromisos:** la validación requiere una consulta en servidor (aceptable para un
  control plane).
- **Neutral:** la federación sigue usando JWT donde el protocolo lo exige.

## Por qué se rechazaron las alternativas

- **JWT para las sesiones propias** — difícil de revocar antes del vencimiento, tiende a portar
  claims y añade superficie de ataque de parsing/validación sin beneficio aquí.
