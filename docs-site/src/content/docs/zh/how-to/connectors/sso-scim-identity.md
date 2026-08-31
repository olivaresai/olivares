---
title: "SSO、SCIM 与身份来源（firm 归属）"
description: >-
  端到端接入企业身份：联合化的控制台登录（通过 federation seam 的 OIDC/SAML）、
  向 control plane 的 SCIM 预配，以及把 access-map 归属从 approximate 升级为 firm 的
  LDAP / Okta / Entra 名册来源。
sidebar:
  order: 8
---

身份是整个 access map 之下的硬依赖：原生审计把一次访问归属到一个**凭据
（credential）**，而只有身份名册（roster）才能把该凭据绑定到一个**agent 或人**。本页
接入三个身份表面：控制台 **SSO 登录**、向 control plane 的 **SCIM 预配**，以及让归属变为
`attributed`（而非 `approximate`）的**名册来源**（LDAP、Okta、Entra ID）。

## 1. 控制台 SSO（OIDC / SAML）

联合化登录通过引擎的 **federation seam** 提供。其态势在构造上即是诚实的：

- 登录流的端点存在于每一个构建中，且引擎会把每一个携带机密的流值都保留在服务器端 ——
  CSRF state、OIDC nonce、PKCE verifier（只有 S256 *challenge* 会发往提供方）。
  Authorization Code + **PKCE 始终开启**。
- 默认构建随附 `NoFederation` provider：两个端点都返回 `501 sso_not_configured` ——
  在没有接入任何 IdP 的情况下，该表面被诚实地公示出来。完成该协议的 federation provider
  是企业构建的一部分，并在引导时**通过环境变量配置**（`OLIVARES_SSO_PROTOCOL`，OIDC 用
  `OLIVARES_OIDC_*` 一组，SAML 用 `OLIVARES_SAML_*` 一组）。
- 你的 IdP 必须携带的 redirect/ACS URI 是**精确匹配**的（你控制台 origin 上的
  `…/v1/auth/federation/callback` —— 遵循 RFC 9700 的精确匹配，不存在前缀技巧）。

控制台的 **Identity & NHI → SSO & SCIM** 选项卡会记录在用配置、对照精确的预期值检查你
IdP 的 redirect URI，并显示连接状态 —— 在某个面板的后端只是已声明的契约而尚未上线时，它会
显示 “backend pending”，而不是渲染编造的数据：

<img class="light:sl-hidden" src="/console/identity-dark.png" alt="Identity & NHI 视图：带精确 redirect-URI 检查的 SSO 配置、NHI 名册以及关键的密钥态势选项卡。" />
<img class="dark:sl-hidden" src="/console/identity-light.png" alt="Identity & NHI 视图：带精确 redirect-URI 检查的 SSO 配置、NHI 名册以及关键的密钥态势选项卡。" />

## 2. SCIM 预配（入站）

control plane 是一个标准的 SCIM 2.0（RFC 7644）服务提供方，位于：

```
/v1/scim/v2/Users
/v1/scim/v2/Groups
```

- **认证：** 在 SCIM 集成上使用一个绑定租户的 **admin/owner API token** —— 与 API 其余
  部分相同的不透明 token 模型，不存在单独的 SCIM 密钥类型。该端点始终存在（不受特性门控）。
- **Users** 预配与撤销预配主体；由你的 IdP 撤销预配会在 HR 一发话的那一刻就吊销访问权限。
- **Groups** 承载身份到组的引用数据。每个组都可以通过 `mapped_role` 映射到一个 control-plane
  角色 —— 而该映射是**由操作员拥有的**：它在 control-plane 一侧设置并被审计
  （`scim.group.role.map`）；IdP 的推送绝不会悄无声息地提升某个角色。被推送的组中的未知
  成员会被跳过**并被审计**，而非凭空编造。

## 3. 名册来源：LDAP、Okta、Entra ID

名册来源馈送模块 VI 的身份清单，并且 —— 这才是重点 —— 为模块 III 提供升级归属所需的绑定：

```json
{
  "sources": [
    {
      "name": "corp-ldap",
      "kind": "ldap",
      "tenant": "<tenant-id>",
      "config": {
        "url": "ldaps://ldap.corp.example:636",
        "bind_dn": "cn=olivares-ro,ou=svc,dc=corp,dc=example",
        "bind_password": "<reference>",
        "base_dn": "dc=corp,dc=example"
      }
    },
    {
      "name": "okta",
      "kind": "idp",
      "tenant": "<tenant-id>",
      "config": { "provider": "okta", "base_url": "https://corp.okta.com", "api_token": "<reference>" }
    }
  ]
}
```

关键的 LDAP 选项（来自随附描述符）：`user_filter` / `group_filter`、
`privileged_group_dns`（其成员身份本身即为一种特权访问信号的组）、`nhi_dn_suffix`
（哪个子树承载非人类身份）、`start_tls`、`page_size`。`idp` kind 接受 `provider: okta`
（配 `api_token`）或 `provider: entra`（配 `tenant_id` / `client_id` / `client_secret`）；
`okta` 与 `entra` 也可直接作为 `kind` 使用。

### 它究竟如何升级归属

一个名册来源会注册身份（按 external id），并在目录有声明的情况下注册**许可的 grant**。当一条
被观测到的边，其发起方匹配一个**非共享**的名册身份时，模块 III 会把该访问绑定到那个身份，
并将该边的 confidence 升级为 `attributed`。被多个工作负载共享的身份会诚实地保持
`approximate` —— 名册无法把一个凭据“去共享化”；只有签发逐 agent 的身份才能做到
（[通往治理的桥梁](/zh/how-to/govern-and-approve/)）。

专用的 **agent-identity 与 workload-identity kind**（agent 联合来源 —— Entra Agent ID、
AgentCore、SPIFFE 及同类）才是 firm 的逐 agent 信号；组/目录名册则锐化人员与服务账户。

## 诚实的局限

- **SSO 在企业构建中才完成。** seam、流安全以及 501 态势存在于每一个构建中；协议
  provider 则不然。
- **名册无法修复一个共享凭据。** 它只能诚实地告诉你该凭据是共享的。
- **SCIM 是入站预配** —— control plane 不会把身份反向推送回你的 IdP，且
  Security-Event-Token 接收端是一个入站表面，而非出站 webhook。

## 相关

- [接入一个来源](/zh/how-to/connect-a-source/#硬性依赖每代理身份)
  —— 为什么身份是硬依赖。
- [治理与审批](/zh/how-to/govern-and-approve/) —— 角色、RBAC，以及一个 `mapped_role`
  授予什么。
- [Connector 与覆盖层](/zh/reference/connectors/) —— 完整的身份来源列表（Vault、
  Infisical、Keycloak、SPIFFE，以及 agent-identity 联合 kind）。
