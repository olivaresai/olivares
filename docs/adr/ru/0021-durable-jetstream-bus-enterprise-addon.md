> Машинный перевод. Авторитетным источником является английская версия.

# ADR-0021: Долговечный backend шины событий JetStream (at-least-once + дедупликация на границе шины) как закрытый enterprise-add-on

- **Status:** accepted (extends ADR-0017's "JetStream remains the upgrade path")
- **Date:** 2026-06-24
- **Deciders:** Fran Olivares (scale/reliability lever); design re-anchored against HEAD + a subscriber-idempotency re-census
- **References:** ADR-0017 (the at-most-once Core-NATS bridge), ADR-0020 (enterprise private-repo distribution),
  `LICENSING.md`, `enterprise/durablebus`, `core/eventbus/natsbus`

## Контекст и постановка проблемы

ADR-0017 реализовал распределённую шину как локальный in-proc fan-out + мост **Core-NATS с
at-most-once** и явно **отклонил JetStream для v1** (вариант C): перепись подписчиков от 2026-06-12
показала, что большинство не защищено от дубликатов, поэтому at-least-once доставлял бы дубликаты
обработчикам, которые неверно их обрабатывают. JetStream остался «путём обновления до at-least-once,
**при условии проверки идемпотентности подписчиков**».

Плоскость управления governance не может молча потерять событие, вызывающее DECISION. В открытом
мосте finding.reported / cost.sampled, потерянное между HA-узлами (перезапуск сервера, переполнение
буфера reconnect, отбрасывание медленного потребителя), означает незаметно пропущенный сигнал
enforcement. Enterprise-уровень scale/reliability (lever #4) должен устранить это для класса
enforcement-event без проверки каждого подписчика, предусмотренной ADR-0017. Повторная перепись
подтвердила, что подписчики всё ещё лишь «**достаточно** идемпотентны»: например,
`modules/security` дедуплицирует findings посредством **ограниченного best-effort scan**, а не
жёсткой гарантии (`observed.go`, `anomaly.go`).

## Движущие факторы решения

- **Решить неидемпотентность в BUS, не доверяя обработчикам.** Условие ADR-0017 — сделать каждого
  подписчика идемпотентным — хрупкий распределённый инвариант примерно для 17 обработчиков, который
  любое изменение может снова нарушить; он так и не был выполнен. Одна принадлежащая шине
  дедупликация на её границе даёт долговечное решение: подписчики получают durability без требования
  оставаться всегда корректными.
- **Без rug-pull и регрессии hot path.** Критическое ограничение ADR-0017 сохраняется: локальный
  in-proc hot path и открытый мост Core-NATS должны остаться побитно неизменными в community-бинарнике.
  Обновление должно быть ADDITIVE.
- **Срок монетизации (ADR-0020).** Durability/HA — enterprise lever. Он поставляется как закрытый код
  за build tag `enterprise` после того, как разделение закрытого репозитория сделало тег реальной границей.

## Рассмотренные варианты

- **A. Заменить мост на JetStream для ВСЕХ типов.** Отклонено: терпимые к потерям массовые observations
  (edge/metric) пошли бы через RAFT storage, а поведение открытого моста изменилось бы (rug-pull).
- **B. Долговечный JetStream только для класса ENFORCEMENT со встроенным открытым мостом для остальных
  (ВЫБРАНО).**
- **C. Постоянная таблица дедупликации для каждого подписчика в store.** Отклонено для Fase 1:
  enterprise-only table ломает проверку open≡enterprise schema parity, а открытая таблица тяжелее,
  чем требует гарантия. Состояние дедупликации размещается в JetStream KV (без store и изменения schema).

## Результат решения

Выбран **B**: закрытый add-on `enterprise/durablebus` (`//go:build enterprise`,
`LicenseRef-Olivares-Commercial`), который **встраивает** открытый `*natsbus.Bus` и добавляет путь
JetStream для **enforcement set** (`finding.reported`, `cost.sampled`, `guardrail.observed`,
`approval.requested`, `policy.changed` — оператор может переопределить). Механика:

- **Соседние пространства subject.** Долговечные события публикуются в `<durable_prefix>.<type>`
  (JetStream stream, RAFT, replicas ≥ 3), НЕ ПЕРЕСЕКАЮЩЕЕСЯ с `<subject_prefix>.>` моста Core; каждый
  тип доставляется ровно одним транспортом, а не обоими. Встроенному мосту предписано ИСКЛЮЧИТЬ
  долговечный набор из Core bridging (`natsbus.Options.BridgeExclude`, неактивен в открытом бинарнике).
  Остальные типы сохраняют at-most-once reach открытого моста без регрессии.
- **Publish подтверждает PubAck** (`Nats-Msg-Id = event.ID`): событие либо долговечно сохранено, либо
  ошибка видима — оно не теряется молча. Duplicate window потока сводит двойную публикацию при retry /
  failover к одной сохранённой копии.
- **Leader-gated durable consumer** (ack-explicit), привязанный при promotion и остановленный при
  demotion через watcher `Active()` (elector не предоставляет OnDemote); его server-side position
  переживает failover. Enforcement выполняется один раз на весь кластер.
- **Дедупликация по event.ID на границе inject**, два уровня: in-memory time window (быстро, same-node) и
  bucket **JetStream KV** (RAFT-replicated, TTL-bounded, переживает crash/restart и дедуплицирует между
  узлами). READ-before-inject подавляет дубликат, RECORD-after-inject гарантирует, что crash приведёт к
  повторному inject, а не потере.

**Честная семантика: at-least-once, НИКОГДА не exactly-once.** При нормальной и умеренно деградировавшей
работе LOSS не происходит: record-after-inject, подтверждённый publish долговечен, consumer возобновляет
работу с подтверждённой позиции. Единственный остаточный путь потери ограничен retention: stream хранит
message не дольше `MaxAge` (по умолчанию 72h, `LimitsPolicy`), поэтому stored event отбрасывается, если
НИ ОДИН leader не опустошает его дольше `MaxAge` — total-quorum-loss / многодневный leaderless или
partitioned outage. Окно наблюдаемо через SLI `olivares_durablebus_stream_pending`: backlog, приближающийся
к `MaxAge`, позволяет alert; это не silent drop. Оператор увеличивает `MaxAge` или восстанавливает leader,
чтобы сохранить ноль. DUPLICATE возможен лишь в двух ограниченных окнах — overlap лидерства ≤2s и hard
crash между inject и записью дедупликации — и оба поглощаются downstream (`(tenant_id, event_id)` index
eventing capture и bounded-scan dedup security). Открытый мост остаётся at-most-once и неизменным.

### Последствия

- **Плюсы:** enforcement events переживают доставку между узлами (at-least-once) с одной принадлежащей
  шине гарантией dedup; community-бинарник byte-identical (add-on отсутствует, единственный открытый seam
  `BridgeExclude` неактивен); schema store не меняется (dedup в JetStream KV), schema parity не затронут;
  fail-boot-closed (невозможность установить заявленный durable backend прерывает boot; нелицензированный
  enterprise-бинарник ВИДИМО деградирует до открытого Core-NATS, но не молча до single-node).
- **Минусы / компромиссы:** durable delivery требует round-trip JetStream при publish (PubAck) и KV read
  при inject — приемлемо для умеренного объёма enforcement; оператор может сузить durable set. События
  достигают подписчиков только на leader через consumer, поэтому собственные durable publishes узла не
  получают локальный fan-out (соответствует «enforcement только на leader»). Лицензионный gate шины
  применяется при boot, поэтому установка лицензии для durability требует restart, в отличие от
  hot-applied add-on entitlements.
- **Нейтрально:** Fase 2+ lever (DR ladder, multi-region, per-tenant silo/CMEK) — документированный roadmap
  (`enterprise/durablebus/doc.go`), НЕ реализован.

## Почему альтернативы были отклонены

A делает rug-pull открытого моста и нагружает hot path; C заменяет небольшой KV изменением core schema,
ломающим parity gate. B ограничивает изменение закрытым дополнительным кодом и решает проблему
duplicate-safety ADR-0017 на границе шины, а не через невыполненную проверку каждого подписчика.
