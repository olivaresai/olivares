---
title: "以代码方式管理 Olivares AI（Terraform）"
description: >-
  使用 Olivares AI 的 Terraform/OpenTofu provider，通过不透明 API 令牌对引擎的
  REST API 进行认证，声明并协调控制平面对象——智能体、策略、身份绑定与部署。
---

Olivares AI 提供一个 **Terraform provider**，让你能够*以代码方式*管理控制平面——
在 HCL 中声明智能体、治理策略、智能体↔身份绑定以及部署定义，并通过引擎的 REST API
与运行中的引擎进行协调。这是模块 XIX（自有 API + 以代码管理）；该 provider 是一个轻量客户端，
封装在 [API 参考](/reference/api/) 所记录的同一套 REST 接口之上，因此凡是你能在 HCL 中完成的操作，
都能通过 REST 完成。

provider 与 CLI 采用 Apache-2.0 许可，且从不导入引擎内部实现；HCL 只是受治理 API 的
另一种前端。

## 配置 provider

```hcl
terraform {
  required_providers {
    olivares = {
      source = "olivaresai/olivares"
    }
  }
}

provider "olivares" {
  endpoint = "https://olivares.internal:8443" # or OLIVARES_ENDPOINT
  api_token = var.olivares_token                  # or OLIVARES_API_TOKEN (sensitive)
  # tenant   = "…"                                # optional; or OLIVARES_TENANT (sent as X-Olivares-Tenant)
  # insecure_skip_verify = true                   # dev self-signed cert only
}
```

| 设置项 | 是否必填 | 环境变量回退 | 说明 |
|---|---|---|---|
| `endpoint` | 是 | `OLIVARES_ENDPOINT` | 控制平面 API 的基础 URL |
| `api_token` | 是 | `OLIVARES_API_TOKEN` | **不透明 bearer 令牌**（产品使用不透明、可撤销的令牌，而非 JWT） |
| `tenant` | 否 | `OLIVARES_TENANT` | 租户 UUID；当令牌已绑定租户时可省略 |
| `insecure_skip_verify` | 否 | — | 跳过 TLS 校验，仅用于开发环境的自签名证书；生产环境绝不可用 |

认证方式是在每个请求上发送 bearer 令牌，并通过 `X-Olivares-Tenant` 头携带租户——
与 API 的其余部分一样采用相同的默认拒绝（deny-by-default）RBAC、租户作用域划分以及逐操作审计。
为一个遵循最小权限的服务身份铸造令牌，并将其排除在 state 之外（使用变量与密钥后端）。

## 资源

| 资源 | 管理对象 | 关键属性 |
|---|---|---|
| `olivares_agent` | 库存中的一个智能体实体 | `name`（必填）、`kind`（必填）、`external_id`（可选）；计算属性 `id`、`status`、`version` |
| `olivares_policy` | 一项治理策略 | `name`（必填）、`kind`（`abac` 或 `approval`，必填，不可变更）、`enabled`、`spec`（必填，JSON）；计算属性 `spec_canonical` |
| `olivares_agent_identity_binding` | 将智能体绑定到一个非人类身份（用于强化 R/RW 归因的桥梁） | `agent_id`、`identity_id`/`identity_ref`、`mint`、`allow_unknown`；计算属性 `minted`、`shared`、`agent_count` |
| `olivares_deployment` | 一份部署**定义**（声明式期望状态） | `subject_kind`、`subject_ref`、`name`、`environment`、`runtime`、`target`、`source_ref`、`spec`、`desired_status`；计算属性 `current_version`、`applied_version`、`spec_hash` |

## 数据源

只读视图，使模块能够引用受治理的状态而无需重新实现 REST 调用：`olivares_policies`、
`olivares_identities`、`olivares_deployment`、`olivares_server_info` 以及 `olivares_access_edges`——
后者暴露 R/RW 边，并在设置 `include_drift = true` 时暴露 Permitted-vs-Observed 漂移
（包括对尚无法稳固归因的访问所标注的诚实 `reconciliation_pending` 标志）。

## 一个最小示例

```hcl
resource "olivares_agent" "billing_bot" {
  name = "billing-reconciler"
  kind = "service"
}

resource "olivares_policy" "require_approval_for_prod" {
  name    = "prod-deploys-need-approval"
  kind    = "approval"
  enabled = true
  spec    = jsonencode({
    # policy body — see the API reference for the schema of each kind
  })
}

# Read the current Permitted-vs-Observed drift as data:
data "olivares_access_edges" "estate" {
  include_drift = true
}
```

`terraform plan` 会将你的 HCL 与引擎进行协调；`terraform apply` 则通过受治理的 API
创建或更新这些对象。由于策略与绑定会改变授权面，应将该 plan 视为一项需评审的变更——
引擎会以真实操作者审计每一次变更。

:::caution[`olivares_deployment` 声明期望状态；实时 apply 受关卡控制]
`olivares_deployment` 管理的是一份部署**定义**——声明式、带版本的期望状态。它映射到模块 VII（部署），
其实时执行是一个**默认关闭（deny-closed）的接缝**：在执行器被预置之前，引擎会*规划并治理*一次部署，
但 **`apply`/`retire` 会返回 `503`**，而不会对基础设施采取行动。因此 `olivares_deployment` 资源
如今记录并治理意图；它本身并不协调真实的基础设施。参见 [模块 VII](/zh/reference/modules/vii-deploy/) 与
[诚实与限制](/zh/start/honesty-and-limits/)。
:::

:::note[provider 有意只覆盖 API 的一个子集]
该 provider 覆盖上述以代码管理的对象。完整的受治理接口——以及每个 `spec` 的字段级 schema——
是 REST API；部分模块路由虽可访问，但有意排除在所提供的 OpenAPI 文档之外。在依赖某资源的属性之前，
请对照 `terraform providers schema -json` 与 [API 参考](/reference/api/) 进行核实；本页不复述无法与代码
保持同步的 schema。
:::

## 相关内容

- [API 参考](/reference/api/) — provider 所驱动的 REST 接口。
- [API 稳定性策略](/zh/reference/api-stability/) — provider 所依赖的版本化/弃用承诺（当某个响应携带弃用信号时，它会在每次运行时发出一次警告）。
- [模块 XIX — 自有 API + 以代码管理](/zh/reference/modules/xix-api-manage-as-code/)。
- [模块 VII — 部署与集成](/zh/reference/modules/vii-deploy/) — 上文提到的 503 接缝注意事项。
- [治理与审批](/zh/how-to/govern-and-approve/) — 策略与审批如何治理你所声明的内容。
