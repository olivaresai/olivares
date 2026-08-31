---
title: Справочник gRPC — службы, методы и типы сообщений
description: >-
  Каждый RPC, регистрируемый движком Olivares AI и хостом плагинов, с типом потока,
  сообщениями запроса и ответа и полным именем метода, по которому он передаётся.
  Сгенерировано из собственных регистрационных таблиц серверов.
---

Olivares AI использует gRPC в двух местах, направленных в противоположные стороны:

- **API control plane движка** (`olivares.api.v1.ControlPlane`) — небольшое отражение
  REST-поверхности для клиентов, предпочитающих типизированную заглушку. Более широким из
  этих двух остаётся REST-контракт в [справочнике API](/reference/api/).
- **Протокольный контракт плагинов** (`olivares.sdk.v1.*`) — версионированный контракт,
  на котором говорит каждый внепроцессный коннектор и модуль. Именно его вы реализуете,
  когда [создаёте коннектор](/ru/how-to/build-a-connector/) на языке, отличном от Go.

Эта страница **сгенерирована из регистрационных таблиц, которые серверы передают gRPC**,
а не из файлов `.proto`. Различие существенно: файл `.proto`, изменённый без регенерации,
описывает службу, которой бинарный файл не предоставляет. Проверка этой страницы сообщает
о расхождении, а не публикует более привлекательную из двух версий. Указанный здесь метод
доступен для вызова клиентом.

:::note[Стабильность]
Контракт плагинов `olivares.sdk.v1` версионирован и защищён детектором несовместимых изменений
buf: несовместимое изменение требует нового major-пакета. Объём и срок этого обязательства
описаны в [стабильности API](/ru/reference/api-stability/).
:::

## Транспорт и аутентификация

Каждый метод перечисленных ниже служб, кроме `GetServerInfo`, требует аутентифицированного и
авторизованного субъекта. Два исключения сделаны намеренно и перечислены явно:
`GetServerInfo` отвечает анонимно, а стандартная служба `grpc.health.v1.Health` (`Check`,
`List`, `Watch`) обслуживается тем же listener без субъекта, потому что probe или service
mesh должен обращаться к ней в каждом pod так же, как kubelet обращается к `/livez`.
Отсутствующий bearer-токен оставляет запрос анонимным, а присутствующий, но недействительный
токен отклоняется. Служба control plane доступна на gRPC-listener движка; службы плагинов
вызываются через брокер go-plugin (коннекторы на хосте) либо через gRPC с взаимным TLS
(удалённый сборщик). Настройте listener переменными `OLIVARES_*` из
[справочника конфигурации](/ru/reference/configuration/).

<!-- BEGIN GENERATED olivares-grpc-reference — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

Движок и хост плагинов регистрируют **28 RPC** в **7 службах**. Таблицы ниже считываются из
сгенерированных регистрационных таблиц, которые серверы передают gRPC; указанный здесь метод
доступен для вызова клиентом.

### `olivares.api.v1.ControlPlane`

Определено в `apiv1/api.proto`; 5 RPC.

| Метод | Полное имя метода | Вид | Запрос | Ответ | Назначение |
|---|---|---|---|---|---|
| `CreateAgent` | `/olivares.api.v1.ControlPlane/CreateAgent` | unary | `CreateAgentRequest` | `Agent` | Регистрирует нового агента в инвентаре и возвращает сохранённую запись, включая идентификатор, используемый остальным API. |
| `GetAgent` | `/olivares.api.v1.ControlPlane/GetAgent` | unary | `GetAgentRequest` | `Agent` | Возвращает одного агента по идентификатору с теми же полями, что и REST-endpoint инвентаря. |
| `GetServerInfo` | `/olivares.api.v1.ControlPlane/GetServerInfo` | unary | `Empty` | `ServerInfo` | Сообщает версию, редакцию и готовность. Это единственный метод службы, не требующий аутентифицированного субъекта. |
| `ListAgents` | `/olivares.api.v1.ControlPlane/ListAgents` | unary | `ListAgentsRequest` | `ListAgentsResponse` | Постранично перечисляет агентов, видимых вызывающему субъекту. |
| `VerifyAudit` | `/olivares.api.v1.ControlPlane/VerifyAudit` | unary | `VerifyAuditRequest` | `VerifyAuditResponse` | Повторно проверяет цепочку аудита на диапазоне и сообщает, продолжают ли хеши связываться, включая состояние контрольной точки. |

### `olivares.sdk.v1.ContentSourceService`

Определено в `olivaresv1/v1.proto`; 7 RPC.

| Метод | Полное имя метода | Вид | Запрос | Ответ | Назначение |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.ContentSourceService/Close` | unary | `Empty` | `Empty` | Завершает сессию, открытую `Open`, и освобождает всё, что коннектор удерживал для неё. |
| `DeltaList` | `/olivares.sdk.v1.ContentSourceService/DeltaList` | server-streaming | `ContentDeltaRequest` | `ContentChange` (stream) | Передаёт поток изменений после курсора. Вызывается только тогда, когда коннектор объявляет возможность `content.delta`. |
| `Describe` | `/olivares.sdk.v1.ContentSourceService/Describe` | unary | `Empty` | `DescribeResponse` | Возвращает дескриптор коннектора: идентичность, поля конфигурации и объявленные возможности. |
| `Fetch` | `/olivares.sdk.v1.ContentSourceService/Fetch` | unary | `ContentFetchRequest` | `ContentDocument` | Возвращает тело и метаданные одного документа по ссылке, выбранной хостом из потока `List`. |
| `FetchACL` | `/olivares.sdk.v1.ContentSourceService/FetchACL` | unary | `ContentFetchRequest` | `ContentACLResult` | Возвращает ссылки на разрешения, управляющие одним документом. Пустой результат означает применение значения базы знаний по умолчанию. |
| `List` | `/olivares.sdk.v1.ContentSourceService/List` | server-streaming | `ContentListRequest` | `ContentDocRef` (stream) | Передаёт ссылки на документы по одной странице, в пределах ограничений хоста, чтобы корпус нельзя было загрузить в память хоста за один вызов. |
| `Open` | `/olivares.sdk.v1.ContentSourceService/Open` | unary | `OpenRequest` | `Empty` | Начинает сессию с предоставленной хостом конфигурацией до любого вызова содержимого. |

### `olivares.sdk.v1.HostService`

Определено в `olivaresv1/v1.proto`; 3 RPC.

| Метод | Полное имя метода | Вид | Запрос | Ответ | Назначение |
|---|---|---|---|---|---|
| `Log` | `/olivares.sdk.v1.HostService/Log` | unary | `LogRecord` | `Empty` | Записывает одну структурированную запись журнала через движок, чтобы внепроцессный модуль писал туда же, куда и внутрипроцессный. |
| `Publish` | `/olivares.sdk.v1.HostService/Publish` | unary | `Event` | `Empty` | Публикует одно событие в шину движка от имени внепроцессного модуля. |
| `Subscribe` | `/olivares.sdk.v1.HostService/Subscribe` | server-streaming | `SubscribeRequest` | `Event` (stream) | Передаёт модулю поток событий шины, фильтруя по запрошенным типам. Пустой фильтр означает все типы. |

### `olivares.sdk.v1.IngestService`

Определено в `olivaresv1/v1.proto`; 1 RPC.

| Метод | Полное имя метода | Вид | Запрос | Ответ | Назначение |
|---|---|---|---|---|---|
| `Push` | `/olivares.sdk.v1.IngestService/Push` | client-streaming | `IngestEnvelope` (stream) | `IngestSummary` | Принимает поток наблюдений от демона-сборщика, поднимает каждое в шину событий и возвращает сводку после завершения потока. |

### `olivares.sdk.v1.ModuleService`

Определено в `olivaresv1/v1.proto`; 4 RPC.

| Метод | Полное имя метода | Вид | Запрос | Ответ | Назначение |
|---|---|---|---|---|---|
| `Describe` | `/olivares.sdk.v1.ModuleService/Describe` | unary | `Empty` | `DescribeResponse` | Возвращает дескриптор модуля: его идентичность и принимаемую конфигурацию. |
| `Init` | `/olivares.sdk.v1.ModuleService/Init` | unary | `InitRequest` | `Empty` | Передаёт модулю конфигурацию и позволяет подготовиться до запуска чего-либо. |
| `Start` | `/olivares.sdk.v1.ModuleService/Start` | unary | `Empty` | `Empty` | Запускает работу модуля после успешного `Init`. |
| `Stop` | `/olivares.sdk.v1.ModuleService/Stop` | unary | `Empty` | `Empty` | Останавливает модуль и позволяет ему освободить удерживаемые ресурсы. |

### `olivares.sdk.v1.OutputService`

Определено в `olivaresv1/v1.proto`; 4 RPC.

| Метод | Полное имя метода | Вид | Запрос | Ответ | Назначение |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.OutputService/Close` | unary | `Empty` | `Empty` | Завершает сессию, открытую `Open`, и освобождает всё, что коннектор удерживал для неё. |
| `Describe` | `/olivares.sdk.v1.OutputService/Describe` | unary | `Empty` | `DescribeResponse` | Возвращает дескриптор коннектора: идентичность, поля конфигурации и объявленные возможности. |
| `Notify` | `/olivares.sdk.v1.OutputService/Notify` | unary | `NotifyRequest` | `NotifyResponse` | Доставляет одно уведомление в назначение и сообщает, как оно обработано; от этого зависит повтор хостом. |
| `Open` | `/olivares.sdk.v1.OutputService/Open` | unary | `OpenRequest` | `Empty` | Начинает сессию с предоставленной хостом конфигурацией до любой доставки. |

### `olivares.sdk.v1.SourceService`

Определено в `olivaresv1/v1.proto`; 4 RPC.

| Метод | Полное имя метода | Вид | Запрос | Ответ | Назначение |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.SourceService/Close` | unary | `Empty` | `Empty` | Завершает сессию, открытую `Open`, и освобождает всё, что коннектор удерживал для неё. |
| `Describe` | `/olivares.sdk.v1.SourceService/Describe` | unary | `Empty` | `DescribeResponse` | Возвращает дескриптор коннектора: идентичность, поля конфигурации и объявленные возможности. |
| `Gather` | `/olivares.sdk.v1.SourceService/Gather` | server-streaming | `Empty` | `Observation` (stream) | Передаёт наблюдения хосту, который поднимает каждое в шину событий. Поток завершается с пакетным запуском или отменой хостом. |
| `Open` | `/olivares.sdk.v1.SourceService/Open` | unary | `OpenRequest` | `Empty` | Начинает сессию с предоставленной хостом конфигурацией до сбора наблюдений. |

<!-- END GENERATED olivares-grpc-reference -->

## Формы сообщений

В таблицах названы сообщения каждого запроса и ответа; их поля объявлены в файлах `.proto`,
указанных рядом со службами. Эти файлы входят в репозиторий и служат источником генерации
заглушек. Перед чтением полезно знать два соглашения:

- **Поля словаря — строки, а не закрытые enum**: режим доступа, источник сигнала,
  уверенность, серьёзность и тип события. Сторонний коннектор может ввести собственный
  источник сигнала, не ожидая релиза SDK.
- **Формы payload закрыты.** Payload `Observation` или `Event` — это `oneof` известных
  типов сообщений плюс запасной JSON для payload событий, определённых модулем.
  Нераспознанный payload является ошибкой контракта и не отбрасывается молча.

## Создание клиента

Файлы `.proto` — контракт. Для контракта плагинов направьте protobuf-инструменты вашего
языка на `sdk/plugin/proto/olivaresv1/v1.proto`, а для зеркала control plane — на
`core/api/proto/apiv1/api.proto`. Готовые клиенты для Go и TypeScript описаны в руководстве
[Использование клиентских SDK](/ru/how-to/use-the-client-sdks/).
