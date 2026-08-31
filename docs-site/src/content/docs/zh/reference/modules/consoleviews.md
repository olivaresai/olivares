---
title: "已保存的控制台视图"
description: >-
  为控制台视图状态（过滤器、范围、scope）创建有名称、可共享的快照，并按
  tenant 存储在服务端。保存一次调查，再与团队共享。本文说明模块存储的内容、
  所有权与共享规则，以及如实声明的限制。
---

`consoleviews` 模块为控制台提供**已保存视图**：它是视图状态的命名快照，包含
控制台编码到 URL 中的同一组过滤器、时间范围和 scope，并**按 tenant 存储在
服务端**。因此，诸如*“过去 24 小时内失败的准入”*这样的调查不会随浏览器关闭而
消失，运营者可以在不同计算机上继续使用；共享后，整个团队只需一次点击即可打开。

## 它存储什么，以及绝不存储什么

已保存视图**只含参数**：一个有大小上限的 JSON 对象（最大 4 KB），用于保存视图的
URL 状态，另加名称、可选描述、所有者 principal 和 `shared` 标志。模块**绝不存储
查询结果、台账行或参数会选中的任何数据**。加载已保存视图时，会以调用者自己的
权限重新运行底层查询。控制台严格把存储的参数当作数据处理。

## 所有权、共享与可执行的操作

- **创建/更新** — 任何具有 `consoleviews:view:write`（editor tier）的成员。视图
  归创建它的 principal 所有；只有所有者可以编辑。
- **可见性** — 所有者始终能看到自己的视图。标有 `shared` 的视图对所有具有
  `consoleviews:view:read`（viewer tier）的 tenant 成员可见。你无权查看的视图
  返回 `404`，绝不返回 `403`，以免泄漏它的存在。
- **删除** — 所有者，或者 tenant 的 **admin/owner role**，可删除任意视图（用于
  清理离职用户留下的视图）。
- **上限** — 每位所有者 200 个视图，每个 tenant 2000 个；达到上限时以明确消息
  拒绝。`(feature, owner, name)` 是自然键：在同一 feature 下保存重复名称会返回
  `409`。

每次创建、更新和删除都会记入 tenant 的审计台账，并归于真实 principal。所记录的
元数据只标识视图（feature、名称、共享标志），绝不包含其参数。

## 路由

| 方法 | 路由 | 权限 |
|---|---|---|
| `GET` | `/v1/m/consoleviews/views?feature_id=` | `consoleviews:view:read` |
| `GET` | `/v1/m/consoleviews/views/{id}` | `consoleviews:view:read` |
| `POST` | `/v1/m/consoleviews/views` | `consoleviews:view:write` |
| `PUT` | `/v1/m/consoleviews/views/{id}` | `consoleviews:view:write` |
| `DELETE` | `/v1/m/consoleviews/views/{id}` | `consoleviews:view:write` |

模块路由属于 **beta** surface，参见
[模块路由参考](/reference/api-beta/)。

## 如实声明的限制

- 服务端会验证视图的 `feature_id` 是 slug，但**不会固定**控制台的 feature 列表。
  控制台 registry 才是权威来源，并会随版本变化；对于控制台中已不再存在的 feature，
  其已保存视图会被忽略。
- 共享视图共享的是**参数**，不是结果。若权限不同，两名运营者加载同一已保存视图时
  可能看到不同数据。这是有意设计的：共享绝不扩大访问权。
- 已保存视图是控制台的便利功能，不是证据。视图本身位于台账链之外，只有它们的
  生命周期事件会被证据化。
- **受工作区限制的**运营者可以读取已保存视图，但不能创建、编辑或删除。带 scope
  的 grant 引擎以 deny-closed 方式禁止受限 principal 的 collection 级写入，并且
  tenant 范围的 admin 删除 override 明确排除受限管理员。
- 在 Postgres 上有并发 writer 时，每位所有者/每个 tenant 的上限是软性的（额外
  超出的数量有界）；重复名称始终会被硬拒绝。
