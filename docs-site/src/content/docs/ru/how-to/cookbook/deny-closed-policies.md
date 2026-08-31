---
title: "Рецепт: политики deny-closed (Cedar / OPA)"
description: >-
  Подключите точку принятия решений (PDP), работающую только на ограничение:
  forbid-наложение Cedar или OPA-политику с permit-by-default, проверенную и
  прогнанную в dry-run перед публикацией — политики, которые могут только
  отнимать доступ, но никогда не расширять его.
sidebar:
  order: 1
---

**Цель:** добавить ограничения на основе атрибутов поверх RBAC с
deny-by-default — например, «никто не трогает ресурсы с меткой `secret`, что бы
ни говорила его роль».

Один инвариант, который нужно держать в голове: PDP **только ограничивает**.
Решение составляется как RBAC ∩ нативный ABAC ∩ внешний PDP — политика никогда
не может выдать то, что отклоняет ролевая модель
([модель](/ru/how-to/govern-and-approve/#шов-политики-abacpdp-только-ограничивает)).

## Cedar (встроенный, основной)

Выберите движок и укажите ему путь к файлу политики, затем перезапустите:

```bash
OLIVARES_PDP_ENGINE=cedar
OLIVARES_PDP_CEDAR_FILE=/etc/olivares/policy.cedar
```

Политика Cedar — это **forbid-наложение**: базовый permit означает «RBAC уже
принял решение», а ваши правила `forbid` вычитают:

```cedar
permit(principal, action, resource);

forbid(principal, action, resource)
  when { resource.kind == "credential" && resource.sensitivity == "secret" };
```

Два факта об авторинге, проверенные по адаптеру: `resource.kind` и
`resource.sensitivity` всегда присутствуют во входных данных решения (можно
ссылаться без условий); любой другой атрибут необходимо защищать через `has()`,
иначе правило не сможет сработать. Написанный вами `permit` никогда не сможет
расширить решение.

## OPA (по HTTP)

```bash
OLIVARES_PDP_ENGINE=opa
OLIVARES_PDP_OPA_URL=http://opa.internal:8181
OLIVARES_PDP_OPA_PATH=/v1/data/olivares/decision
OLIVARES_PDP_OPA_TOKEN=<bearer-reference>     # optional
```

Пишите Rego в режиме **permit-by-default**:

```rego
package olivares

default allow := true

allow := false if {
  input.resource.sensitivity == "secret"
  input.action == "read"
}
```

`true` = нет ограничения. `false`, отсутствующий результат или **любая ошибка
транспорта либо ответ не-2xx срабатывают на закрытие (fail closed)** — запрос
отклоняется, а не остаётся молча неуправляемым.

## Проверка, dry-run, публикация

Модуль governance предоставляет жизненный цикл политики, чтобы плохая политика
никогда не попадала вслепую:

```bash
# Compile-check the source:
curl -ks -X POST "$BASE/v1/m/governance/pdp/validate" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d @policy.json

# Pre-flight a decision WITHOUT audit side effects:
curl -ks -X POST "$BASE/v1/m/governance/pdp/dry-run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"principal":"…","action":"…","resource":{"kind":"credential","sensitivity":"secret"}}'

# Then publish (policy-admin permission):
curl -ks -X POST "$BASE/v1/m/governance/pdp/publish" …
```

`GET /v1/m/governance/pdp/versions` перечисляет то, что развёрнуто;
`POST /v1/m/governance/pdp/explain` объясняет решение.

## Проверьте свойства безопасности

- Перезапуститесь с **некорректным** файлом политики: движок отключает только
  внешний PDP и журналирует это — RBAC и нативный ABAC продолжают управлять;
  control plane не падает.
- Каждое ограничение, которое применяет PDP, **аудируется** — проверьте ledger
  после отклонённого запроса.

## Заметки

- Политики версионируются и публикуются, а не редактируются «на лету» в
  продакшене — относитесь к публикации как к изменению, прошедшему ревью.
- Для действий, требующих одобрения (а не отклонения), смотрите
  [одобрения HITL](/ru/how-to/cookbook/hitl-approvals/).
