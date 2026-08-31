> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0027: Entrada de la nube gestionada — passthrough L4 para el mTLS de los colectores, L7 para la API del plano de control

- **Status:** accepted (managed cloud; this record creates no infrastructure)
- **Date:** 2026-08-02
- **Deciders:** Fran Olivares
- **References:** ADR-0012 (collectors push to the core over gRPC + mTLS), ADR-0028
  (managed-cloud database), ADR-0029 (managed-cloud regions), ADR-0009 (append-only
  hash-chained audit); the platform decision record for the managed cloud; AWS Elastic
  Load Balancing documentation, consulted 2026-08-02:
  `https://docs.aws.amazon.com/elasticloadbalancing/latest/network/network-load-balancers.html`,
  `https://docs.aws.amazon.com/elasticloadbalancing/latest/network/edit-target-group-attributes.html`,
  `https://docs.aws.amazon.com/elasticloadbalancing/latest/application/configuring-mtls-with-elb.html`.

## Contexto y planteamiento del problema

El ADR-0012 fijó la topología de ingesta: los colectores se ejecutan en la infraestructura
del cliente y hacen **push** de las observaciones sobre gRPC con TLS mutuo, y el propio núcleo
**termina ese mTLS**.

Conviene precisar qué aporta esto, porque la versión imprecisa de esta frase es errónea y
sería determinante si se diese por cierta. La admisión en el plano de colectores se apoya en
**dos factores independientes**:

1. **Una puerta de transporte.** El servidor exige y verifica un certificado de cliente cuya
   cadena llega a la CA de colectores configurada. Esto demuestra la posesión de una clave
   cuyo certificado hemos emitido; no se analiza para obtener un sujeto ni nombra a ningún
   principal.
2. **Un principal bearer.** La identidad autenticada sobre la que actúan la autorización y
   la cadena de auditoría (ADR-0009) procede del bearer token de la petición, no del
   certificado.

Ambos se aplican **dentro del propio proceso del producto**. Nada interpuesto avala ninguno
de los dos. Esa es la propiedad que aborda este registro: no «el certificado es la
identidad», sino «ningún intermediario avala ninguno de los dos factores».

La nube gestionada es el primer despliegue que sitúa un balanceador de carga delante de ese
binario. El mismo despliegue también expone una superficie HTTPS pública ordinaria —API REST,
consola, administración— que requiere el tratamiento opuesto: un certificado público
gestionado, un firewall de aplicaciones web y enrutamiento por host/ruta. Un único ingress no
puede servir a ambos sin renunciar a algo en uno de los dos lados.

## Motivadores de la decisión

- Ambos factores de admisión deben seguir aplicándose mediante **una sesión TLS que el propio
  producto termina**. Una nube gestionada que degradase silenciosamente cualquiera de ellos a
  «un intermediario nos dijo que era válido» debilitaría la afirmación central del producto.
- La superficie HTTP pública debería poder usar las protecciones de borde que ofrece L7, sin
  que el producto tenga que reimplementarlas.
- Los streams de larga duración de los colectores deben sobrevivir al comportamiento de idle
  del ingress.
- Ninguna regresión respecto al despliegue self-hosted: una sola ruta de código, no dos.

## Opciones consideradas

- **A — un balanceador de carga L4 para todo.** Passthrough TCP para ambos planos; el binario
  termina todas las sesiones TLS, incluida la de la API pública.
- **B — entrada dividida.** Un **balanceador de carga de red (L4) con un listener TCP** para el
  plano de colectores en passthrough, más un **balanceador de carga de aplicaciones (L7)** para
  la superficie HTTP del plano de control.
- **C — un balanceador de carga L7 con TLS mutuo gestionado.** El balanceador de carga de
  aplicaciones autentica por sí mismo los certificados de cliente (modo verify contra un
  trust store, con listas de revocación) o reenvía la cadena al target como una cabecera HTTP.

## Resultado de la decisión

Opción elegida: **B — entrada dividida**.

### Consecuencias

- **Bueno:** el plano de colectores sigue byte por byte la ruta self-hosted. Un listener TCP
  no termina TLS, por lo que el binario realiza el handshake e impone por sí mismo el requisito
  del certificado, exactamente igual que on-premises. No hay ninguna rama específica de la
  nube en el autorizador ni ningún caso específico de la nube en la cadena de auditoría.
- **Bueno:** la superficie pública puede usar un certificado gestionado, enrutamiento por
  host/ruta y un firewall de aplicaciones web sin que el producto tenga que reimplementar nada
  de ello. El firewall es un servicio que se **factura aparte**, no una propiedad gratuita del
  balanceador de carga L7; se enumera aquí como disponible, no como incluido.
- **Bueno, con su alcance expresado con precisión:** el idle timeout del listener TCP es
  **configurable entre 60 y 6000 segundos** (`tcp.idle_timeout.seconds`, valor por defecto
  **350**); el de un listener TLS está **fijado en 350 segundos y no se puede modificar**. Es un
  timeout de **inactividad** —ausencia de bytes—, **no un techo para la duración del stream**:
  un stream que sigue enviando datos o frames de keepalive no se corta a los 350 segundos. Por
  tanto, el passthrough no «hace posibles los streams largos»; nos permite fijar el presupuesto
  de inactividad. Dicho al revés, porque esta es la parte importante: **un stream sin tráfico
  muere en cualquiera de estos ingress**, y el cliente debe sobrevivir a ello.
- **Malo, y el motivo por el que el punto anterior se presenta como advertencia:** el cliente
  del colector no configura **ningún keepalive de gRPC** (la biblioteca lo desactiva por
  defecto) y, tras un envío fallido, mantiene en caché el stream muerto en vez de
  reconstruirlo. Por tanto, un periodo de inactividad superior al timeout configurado, un
  cambio de liderazgo o un despliegue termina un stream de colector que nada vuelve a conectar.
  Esto **no lo crea la división** —es preexistente—, pero la división es el primer despliegue en
  el que un intermediario cerrará activamente las conexiones inactivas, por lo que es donde la
  carencia empieza a costar datos. Un bucle de reconexión con backoff en el colector es una
  **precondición** para considerar este ingress listo para producción.
- **Malo / compromisos:** dos balanceadores de carga implican dos cargos por hora y dos
  contadores independientes de unidades de capacidad, que en conjunto dominan el coste fijo
  mensual mínimo de un despliegue pequeño. Es un coste real y recurrente que se paga por
  mantener ambos factores de admisión dentro del proceso.
- **Malo, y un requisito de construcción, no una nota al pie:** para los **target groups de
  tipo IP con protocolo TCP o TLS, la conservación de la IP del cliente está desactivada por
  defecto**; y las tareas del runtime de contenedores gestionado son targets IP. Si se mantiene
  el valor por defecto, todas las conexiones de los colectores llegan al binario con la
  dirección privada del balanceador de carga como origen. Todo lo derivado de la dirección
  —registros de auditoría, límites de tasa, allow-lists de direcciones— sería silenciosamente
  incorrecto desde el primer día. La entrada no está completa hasta que se active
  `preserve_client_ip.enabled` o el binario analice Proxy Protocol v2 antes del handshake.
  Activar la conservación también implica que el grupo de seguridad del target se enfrenta a
  las direcciones de los clientes en vez de a la del balanceador de carga, algo que el diseño
  de red debe tener en cuenta.
- **Neutral / seguimientos:** la decisión sobre cuál de los dos mecanismos restablece la
  dirección de origen se deja para la fase de implementación, pero **la elección debe hacerse
  y probarse, no heredarse de un valor por defecto**. Una prueba que confirme que la dirección
  de origen registrada coincide con la del colector es el criterio de aceptación.

## Por qué se rechazaron las alternativas

- **A (un balanceador de carga L4)** — rechazada para el plano *público*, no para el plano de
  colectores. Es más barata y es lo más próximo a la topología self-hosted, pero la API del
  plano de control perdería los certificados gestionados, el WAF y el enrutamiento por
  host/ruta, y el producto acabaría reimplementando en L7 lo que el borde ya proporciona. La
  mitad de la opción A correspondiente a los colectores es exactamente lo que conserva la
  opción B.
- **C (TLS mutuo gestionado en L7)** — rechazada porque **desplaza la frontera de confianza**.
  En modo verify, el borde realiza la comprobación del certificado y la aplicación recibe una
  petición que ya ha sido avalada; en modo passthrough, la cadena de certificados llega como
  una cabecera `X-Amzn-Mtls-Clientcert`. En ambos casos, la puerta de transporte deja de ser
  algo aplicado por el producto y se convierte en una afirmación hecha por otro: precisamente
  la sustitución que este producto existe para hacer verificable, y cuyo modo de fallo
  (cualquier cosa que pueda alcanzar directamente el target puede falsificar la cabecera) está
  a un solo error de configuración de red. El trust store gestionado con listas de revocación
  es una ventaja operativa genuina que el producto no tiene hoy en absoluto para los
  certificados de colectores: carga una CA y realiza una validación X.509 ordinaria, sin
  comprobar CRL ni OCSP. Si alguna vez la revocación gestionada pesa más que la terminación
  directa, será objeto de un **registro nuevo**, no de una modificación de este.
