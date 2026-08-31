> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0018: Backend de voz en tiempo real — postura latente documentada en la v1, integración post-v1

- **Status:** accepted
- **Date:** 2026-06-12
- **Deciders:** Fran Olivares
- **References:** `modules/liveingest/voice.go:28`
  (`PublishVoiceTelemetry`), `modules/voice` (módulo XVI)

## Contexto y planteamiento del problema

La sonda de telemetría de voz está construida de extremo a extremo y validada: `liveingest.PublishVoiceTelemetry`
publica una `voice.Telemetry` incluida en allow-list como `voice.telemetry.observed`, y el módulo XVI la
incorpora a los metadatos de sesión mediante un consumidor estricto que la revalida. Nada invoca al productor en
ninguna ruta de producción — no hay backend de voz en tiempo real en el build —, así que la mitad de observación
está vacía. Es una costura pura. La pregunta: ¿integrar ahora un backend (p. ej. LiveKit) o declarar la postura?

## Resultado de la decisión

**La v1 entrega la sonda latente y LO DICE.** La postura honesta ya está aplicada en el código: el productor
rechaza muestras descartables y no fabrica nada; el `Start` de liveingest registra "voice telemetry probe wired
but dormant — no realtime voice backend in this build emits turn metadata"; la mitad de observación permanece
visiblemente vacía en lugar de falsamente llena (nunca un hueco silencioso — y, por igual, nunca una plenitud
fabricada). Integrar un backend concreto de tiempo real (LiveKit o equivalente) es una **sesión post-v1, si y
cuando haya demanda**.

El trabajo de escalado horizontal dejó la costura honesta a nivel multinodo por el camino: un futuro dispatcher
que alimente la sonda en CUALQUIER nodo alcanza ahora el módulo de voz del líder a través del puente NATS (la
raíz de composición registra el decodificador de payloads `voice.Telemetry`), de modo que la costura latente no
se convirtió en silencio en una costura exclusiva de nodo único bajo HA.

### Consecuencias

- **Bueno:** ninguna dependencia especulativa; la costura está probada (productor + consumidor + decodificador del
  puente NATS), de modo que una integración futura es cableado, no diseño.
- **Malo / compromisos:** el panel de observación de voz permanece vacío en la v1 — documentado en el contrato de
  la UI como una costura declarada, que es el estado veraz.
- **Neutro:** la decisión está condicionada por la demanda, no por la arquitectura.
