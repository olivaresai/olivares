---
title: "Módulo XXI — cuadros de mando ejecutivos e informes"
description: >-
  La vista de dirección sobre el control plane: coste, uso, riesgo, cumplimiento y
  fiabilidad agregados desde los módulos que poseen los cálculos, sujetos al mismo RBAC
  que las vistas técnicas, con exportación a PDF bajo demanda. Qué presenta, qué nunca
  calcula y sus límites honestos.
---

El módulo XXI es la superficie de dirección de la **capa Web** (capa 4): una lectura de
alto nivel del estate —gasto, uso, postura de riesgo, cobertura de cumplimiento y
fiabilidad— situada junto a la UI técnica por módulo. **Agrega y presenta; nunca
recalcula** (los módulos poseen cada cifra), y hereda el mismo acotado por tenant y el
mismo RBAC que las vistas que resume.

## Qué es

Dos superficies de solo lectura componen este módulo:

- el **cuadro de mando ejecutivo** (`/dashboards`): la agregación completa entre módulos,
  con un rango de coste seleccionable (7d / 30d / 90d / desde inicio de mes), un desglose
  de gasto por equipo, proyecto, agente, modelo o proveedor, y una portada de informe
  imprimible;
- la **vista general de inicio** (`/`): una puerta de entrada deliberadamente más ligera:
  una sola rejilla de pilares del estate (inventario, sesiones en vivo, seguridad,
  cumplimiento, ritmo de gasto, salud/SLA), cada uno un enlace de profundización hacia su
  módulo.

La vista general de inicio reutiliza los hooks de lectura, las agregaciones puras y las
primitivas de tile del cuadro de mando en lugar de duplicarlas, y comparte la misma
caché de consultas acotada por tenant, de modo que la puerta de entrada se mantiene
ligera (menos consultas) a la vez que coherente con la vista profunda.

## Qué presenta (y qué nunca calcula)

El cuadro de mando arranca con KPI repartidos en cinco pilares: **coste** (FinOps XI +
Modelos X), **uso** (Inventario I + Sesiones II), **riesgo** (Seguridad IX + Red-teaming
XVIII + Access map III), **cumplimiento** (XIII) y **fiabilidad** (Salud y SLA XXII). La
capa de agregación es un conjunto de **funciones puras** que solo cuentan, suman y
ordenan lo que los módulos ya decidieron: el coste se mantiene en las unidades enteras de
los módulos, y la severidad de los hallazgos, la puntuación de red-team, el estado de los
controles y el estado de salud se pasan sin cambios.

Como no posee ningún cálculo, no puede lavar la veta de honestidad de una fuente, y no lo
hace: un agregado `truncated` sigue marcado como un mínimo; una ejecución de red-team que
no pudo completar sus sondeos **nunca** se cuenta como aprobada; el acceso observado con
cobertura aproximada u opaca se expone como una cota inferior; el cumplimiento se lee como
**cobertura de controles**, nunca como una afirmación de "cumplidor", y conserva su
descargo de responsabilidad permanente; un sujeto de salud sin ninguna comprobación se lee
`unknown`, no sano.

## Exportación y el cable

La exportación es **bajo demanda, en el lado del cliente**: el cuadro de mando imprime lo
que hay en pantalla mediante el Guardar como PDF del navegador (`window.print()`), con una
portada de informe solo para impresión (organización, rango, hora de generación) y un pie
con descargo de responsabilidad permanente. Esto es fiel al RBAC y al acotado por tenant
**por construcción**: el informe solo puede contener las secciones que el rol realmente
renderizó. El documento exportado, igual que el propio cuadro de mando, lleva **solo KPI
agregados; ningún payload, ningún secreto**: el dato mínimo es una propiedad de lo que
cruza el cable, no una promesa sobre el buen comportamiento de quien lo ve.

## Actuación

El módulo XXI **no tiene superficie de actuación** (`—` en el [catálogo de
módulos](/es/reference/modules/overview/)). Es una capa de presentación sobre endpoints de
lectura que los módulos ya sirven; no emite ninguna escritura, no dispara nada y no
despacha nada.

:::caution[Límites honestos]
- **No hay informes programados ni entregados.** La intención de diseño del catálogo
  incluye informes programados y exportables; lo que se entrega hoy es **solo impresión a
  PDF bajo demanda en el lado del cliente**. No hay endpoint de informes en el servidor, ni
  programación recurrente, ni entrega por correo: no esperes que un informe llegue por sí
  solo.
- **Es tan honesto como sus fuentes.** Cada hueco de cobertura, truncamiento, atribución
  pendiente y descargo viene de los módulos subyacentes y se muestra, no se suaviza; un
  número bajo puede significar bajo riesgo *o* cobertura limitada. Lee cada pilar con los
  límites de su módulo (p. ej. los niveles de cobertura del access map).
- **El RBAC controla cada pilar.** Un rol que no puede leer una fuente nunca ve su KPI y no
  puede imprimirlo. Un lector sin ninguna fuente permitida ve un estado vacío honesto, no un
  cuadro de mando fabricado.
- **Punto en el tiempo, fuente única.** Riesgo, cumplimiento y fiabilidad son instantáneas
  del estado actual; solo el coste abarca el rango seleccionado. La vista es una agregación
  de los datos de este propio control plane, no una herramienta de BI externa.
:::

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/) — capas, y la separación Gobernar/Actuar.
- [Módulo XI — Coste y AI FinOps](/es/reference/modules/xi-finops/) — las cifras de gasto que agrega.
- [Módulo XIII — Cumplimiento](/es/reference/modules/xiii-compliance/) — cobertura de controles, nunca una afirmación de cumplimiento.
- [Módulo III — access & resource map](/es/reference/modules/iii-access-map/) — el drift detrás del pilar de riesgo.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — dónde se sitúa la capa Web.
- [Honestidad y límites](/es/start/honesty-and-limits/) — cómo el producto declara lo que hace y lo que no hace.
