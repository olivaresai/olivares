> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0029: Regiones de la nube gestionada — una región primaria, la residencia se resuelve mediante self-hosting

- **Status:** accepted (managed cloud; this record creates no infrastructure)
- **Date:** 2026-08-02
- **Deciders:** Fran Olivares
- **References:** ADR-0027 (managed-cloud ingress), ADR-0028 (managed-cloud database),
  ADR-0020 (enterprise private-repo distribution), ADR-0024 (DDIL offline semantics and
  signed bundles); the platform decision record for the managed cloud.

## Contexto y planteamiento del problema

Hay que responder conjuntamente a dos preguntas, porque responder mal a una obliga a dar una
mala respuesta a la otra: **dónde se ejecuta el plano gestionado** y **qué se le dice a un
cliente que pregunta dónde residen sus datos**.

La tentación es elegir la región que facilita la respuesta a la segunda pregunta —una región
cuya jurisdicción quede bien en un apartado de cumplimiento— y aceptar la latencia que eso
implique para los clientes reales. Es el orden equivocado. También se apoya en una idea errónea
que conviene dejar escrita una vez en un lugar duradero, para que nadie vuelva a deducirla:
**el lugar donde se almacenan los bytes no determina qué legislación de protección de datos es
aplicable.** Prestar servicio a interesados de una jurisdicción conlleva la aplicación de la
legislación de esa jurisdicción, con independencia de la ubicación del hosting.

## Motivadores de la decisión

- La latencia para los clientes a los que realmente se vende el producto.
- Las evidencias de cumplimiento que solicita un comprador enterprise, que en gran medida son
  evidencias sobre el **proveedor de infraestructura**, no sobre la región.
- No pagar el coste fijo de una segunda región —ni la complejidad permanente del tratamiento
  de datos entre regiones— antes de que un cliente la exija.
- Disponer de una respuesta veraz y directa para un cliente con un requisito estricto de
  residencia.

## Opciones consideradas

- **A — una única región primaria en el mercado objetivo**, con una segunda región como
  proyecto condicionado por la demanda.
- **B — dos regiones desde el lanzamiento**, una por cada mercado principal.
- **C — una región primaria elegida por el relato normativo** en lugar de por la latencia para
  los clientes.

## Resultado de la decisión

Opción elegida: **A — una única región primaria, situada en el mercado objetivo (Este de
Estados Unidos)**. Una segunda región es un proyecto que se abrirá cuando haya un requisito
financiado, no un elemento del lanzamiento. La fijación de región por tenant y la replicación
entre regiones quedan deliberadamente fuera del ámbito de la primera versión gestionada.

Los clientes con un **requisito contractual o normativo de residencia que la región primaria
no satisfaga** recibirán servicio mediante la **edición self-hosted**, que es la forma principal
del producto, se ejecuta en la propia infraestructura del cliente y responde por completo a la
cuestión de la residencia, no de forma parcial. No es un apaño; es la respuesta más sólida y
está disponible desde el primer día.

### Consecuencias

- **Bueno:** el despliegue consta de una región, una base de datos y un dominio de fallo sobre
  el que razonar, y el presupuesto de latencia se destina a donde están los clientes.
- **Bueno:** la respuesta sobre la residencia es honesta e inmediata —self-hosting—, en lugar
  de una promesa en el roadmap.
- **Malo / compromisos:** no se puede atender a un cliente que quiera un servicio *gestionado*
  **y** residencia fuera de Estados Unidos hasta que exista una segunda región. Es una carencia
  conocida y aceptada, y debe exponerse claramente en el material comercial en lugar de
  disimularse.
- **Malo:** una única región constituye un único dominio de fallo regional. Multi-AZ
  (ADR-0028) cubre la pérdida de una zona de disponibilidad, **no** la pérdida de una región.
  La estrategia de recuperación ante una caída regional consiste en restaurar en otro lugar
  desde los backups, con un tiempo de recuperación medido en horas, y debe **ensayarse** antes
  de citar ese tiempo ante nadie.
- **Neutral, y el motivo de dejar esto por escrito:** elegir una región primaria en Estados
  Unidos implica que los datos personales de interesados no estadounidenses se
  **transfieren**, lo que exige un mecanismo de transferencia válido y un acuerdo de
  tratamiento que identifique al proveedor de infraestructura como subencargado. Este registro
  no crea ninguno de los dos. Deja constancia de que **la elección de región no elimina la
  obligación**, para que ningún lector futuro confunda «alojamos en la región X» con una
  respuesta de cumplimiento. Este es un registro de ingeniería, no asesoramiento jurídico;
  los instrumentos corresponden al ámbito de cumplimiento.

## Por qué se rechazaron las alternativas

- **B (dos regiones en el lanzamiento)** — rechazada porque supone pagar el doble, de forma
  permanente, por un cliente que aún no existe. Una segunda región duplica el coste fijo mínimo
  de la infraestructura y añade una clase de problema que nunca desaparece: qué región posee
  un tenant, qué cruza entre ellas y cómo se demuestra una afirmación de residencia por tenant
  en lugar de por plataforma. Merece la pena hacerlo cuando un requisito firmado lo financie.
- **C (región elegida por el relato normativo)** — rechazada porque compra un párrafo y lo paga
  con cada petición. Tampoco ofrece lo que aparenta: como se ha indicado, la ubicación del
  hosting no decide la legislación aplicable, por lo que el relato sería más débil de lo que
  parece, mientras que el coste de latencia sería exactamente tan grande como parece.
