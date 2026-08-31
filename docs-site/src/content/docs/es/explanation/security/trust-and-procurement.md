---
title: Confianza y compras
description: >-
  Lo que el equipo de seguridad de un comprador puede verificar hoy: preparación
  para certificaciones (no afirmaciones), el programa de pruebas de penetración,
  el modelo de objetivos de respuesta de soporte, la conformidad de accesibilidad y la evidencia de
  cumplimiento legible por máquina — y lo que, con honestidad, todavía no existe.
---

Esta página es el punto de entrada para los equipos de seguridad, cumplimiento y
compras que evalúan Olivares AI. La postura de cumplimiento del producto sigue una
regla, aplicada tanto en el código como en la prosa: **declara lo que está
construido y es verificable; nunca afirmes una certificación que no existe.** El
módulo de cumplimiento informa de un control respaldado solo por evidencia de
diseño como `by_design` — nunca `satisfied` — y cada entrada de framework en el
catálogo lleva su propia advertencia de "no es una certificación".

:::note[Estado actual, sin sorpresas]
Olivares AI **no posee ningún informe SOC 2, ningún certificado ISO/IEC 27001 ni
42001**, **no ha sido sometido todavía a una prueba de penetración de terceros** y
**no** figura en el CSA STAR Registry. Lo que existe en su lugar — y que es
posiblemente más útil antes de contratar — es un paquete de preparación
verificable: correspondencias control por control con evidencia que tú mismo puedes
extraer de un despliegue en ejecución, además de la lista explícita de decisiones
(contratación de auditorías de certificación, contratación de pruebas de
penetración, activación de soporte comercial) que siguen abiertas. FedRAMP/ATO
queda explícitamente fuera del alcance del producto autoalojado.
:::

## El paquete de confianza

El paquete completo orientado al comprador vive en el repositorio bajo `docs/trust/`:

- **Preparación para certificaciones** — correspondencias SOC 2 Type II, ISO/IEC
  27001:2022 e ISO/IEC 42001:2023 desde cada control hacia la capacidad del producto
  y el endpoint de evidencia en vivo que lo respalda, incluida la evidencia
  específica de IA que un auditor de 2026 pregunta (registro de
  prompts/interacciones, versionado de modelos, linaje, inventario de
  subprocesadores LLM).
- **Banco de respuestas a cuestionarios** — respuestas de proveedor preverificadas
  alineadas con los dominios del Shared Assessments SIG 2026 y listas para
  transcribir a un CSA AI-CAIQ para STAR for AI Level 1.
- **Programa de pruebas de penetración** — cadencia comprometida (prueba de terceros
  con alcance definido en la primera GA comercial, anual a partir de entonces,
  reverificaciones según eventos), alcance y un flujo de remediación conectado a los
  objetivos de remediación de CVE publicados en `SECURITY.md`.
- **Arquitectura de referencia** — topologías de despliegue (nodo único, HA
  activo-pasivo, multirregión, air-gapped), zonas de confianza, líneas base de
  dimensionamiento medidas, niveles de RPO/RTO y la superficie de integración con
  IdP/SIEM/ITSM/KMS.
- **Artefactos de compra para la UE** — una plantilla de documentación técnica del
  Anexo IV del Reglamento de IA de la UE poblada a partir de evidencia en vivo, y una
  correspondencia cláusula por cláusula con las cláusulas contractuales modelo MCC-AI
  de la Comisión (variantes High-Risk y Light).
- **Caso de seguridad del agente** — una plantilla de argumento estructurado de estilo
  CAE, prospectiva, con columnas honestas de riesgo residual.
- **Riesgo de proveedor único** — la objeción de viabilidad respondida de forma
  estructural: el núcleo AGPL es la plataforma de gobierno completa, sin nada
  limitado internamente para vender una ampliación (una pequeña línea comercial
  aditiva se compila por separado y se distribuye de forma privada, está ausente
  del binario abierto y añade capacidades, nunca las resta del núcleo abierto); en
  ese binario abierto la clave de licencia solo sirve como atestación y funciona
  offline — no habilita nada —, y las compilaciones son reproducibles y están
  atestadas por procedencia, de modo que la continuidad no depende de la
  existencia del proveedor.

## Lo que puedes verificar sin confiar en nosotros

El autoalojamiento invierte la relación de atestación habitual: la mayoría de los
controles que un informe SOC 2 atestaría puedes verificarlos directamente en tu
propio despliegue.

- **Releases:** firmas cosign, SBOM, procedencia SLSA Build L3 (SLSA v1.2), OpenVEX — consulta
  [Verificar una release](/es/how-to/verify-a-release/).
- **Contacto de seguridad y divulgación:** el canal de reporte, el plazo de divulgación
  coordinada y los objetivos de remediación de CVE se publican en `SECURITY.md` y se anuncian de
  forma legible por máquinas en [`/.well-known/security.txt`](https://olivares.ai/.well-known/security.txt)
  (RFC 9116), de modo que un escáner o un investigador encuentra el canal sin preguntar.
- **Evidencia de manipulación:** el audit ledger append-only, hash-chained y firmado
  por evento se verifica offline — consulta el
  [modelo de seguridad](/es/explanation/security/security-model/).
- **Evidencia de cumplimiento en vivo:** el estado de los frameworks, el análisis de
  brechas, los paquetes de evidencia sellados (JSON/CSV/OSCAL), los AIBOM de modelos
  (CycloneDX 1.6 / SPDX 3.0.1 AI profile), las model cards y el calendario regulatorio
  son todos respuestas de API, no PDF — el producto trata las fechas y
  correspondencias de cumplimiento como datos con versión fijada.
- **Afirmaciones operativas:** los números de SLO, dimensionamiento y RPO/RTO de la
  arquitectura de referencia se remontan a líneas base medidas registradas en el
  repositorio.

## Soporte y accesibilidad

- El modelo de soporte (niveles, objetivos de respuesta según severidad, escalado)
  está publicado en `SUPPORT.md` — incluida la divulgación honesta de que el soporte
  comercial está definido pero todavía no es adquirible, y de que la cadena de
  escalado tiene hoy una sola persona de profundidad.
- El informe de conformidad de accesibilidad es un ACR de edición **VPAT 2.5Rev INT**
  completado (WCAG 2.1/2.2 AA + Revised Section 508 + EN 301 549 V3.2.1) en
  `docs/accessibility/VPAT-olivares-admin.md`, con la verificación formal de
  tecnología de asistencia aún pendiente y divulgada como tal. La consola se entrega
  en inglés y español; la hoja de ruta de i18n más allá de EN/ES está condicionada a
  la demanda y documentada en el paquete de confianza.

## Centro de confianza público

El [Centro de confianza](https://olivares.ai/trust) en el sitio web del producto
presenta los mismos artefactos de cadena de suministro descritos anteriormente en
una página pública independiente: atestaciones SLSA Build L3, firmas cosign,
descargas de SBOM, avisos OpenVEX y el script de verificación. Los titulares de
licencias comerciales pueden acceder a artefactos de cumplimiento por versión a
través del [portal de cliente](https://licenses.olivares.ai/portal).

## Adónde ir a continuación

- [Modelo de seguridad](/es/explanation/security/security-model/) — cómo se defiende la
  plataforma a sí misma.
- [Modelo de amenazas](/es/explanation/security/threat-model/) — adversarios y fronteras
  de confianza.
- [Honestidad y límites](/es/start/honesty-and-limits/) — qué funciona hoy frente a lo
  que está planificado, en todo el producto.
- [Centro de confianza](https://olivares.ai/trust) — verificación pública de la
  cadena de suministro y estado de cumplimiento.
