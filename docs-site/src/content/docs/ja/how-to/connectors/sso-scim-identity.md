---
title: "SSO、SCIM、アイデンティティソース（確かな帰属）"
description: >-
  エンタープライズアイデンティティをエンドツーエンドで配線する: フェデレーション
  シームを通じたコンソールのフェデレーテッドログイン（OIDC/SAML）、コントロール
  プレーンへの SCIM プロビジョニング、そしてアクセスマップの帰属を approximate
  から firm へ格上げする LDAP / Okta / Entra のロスターソース。
sidebar:
  order: 8
---

アイデンティティはアクセスマップ全体の根底にある厳しい依存関係だ: ネイティブ
監査はアクセスを**クレデンシャル**に帰属させるが、そのクレデンシャルを
**エージェントまたは人**に結びつけられるのはアイデンティティロスターだけだ。
このページでは 3 つのアイデンティティの面を配線する: コンソールの
**SSO ログイン**、コントロールプレーンへの **SCIM プロビジョニング**、そして
帰属を `approximate` ではなく `attributed` にする**ロスターソース**
（LDAP、Okta、Entra ID）だ。

## 1. コンソール SSO（OIDC / SAML）

フェデレーテッドログインはエンジンの**フェデレーションシーム**を通じて提供される。
態勢は構成上、正直だ:

- ログインフローのエンドポイントはすべてのビルドに存在し、エンジンは秘密を
  含むすべてのフロー値をサーバー側に保持する——CSRF state、OIDC nonce、
  PKCE verifier（プロバイダーに送られるのは S256 *challenge* のみ）。
  Authorization Code + **PKCE は常時オン**だ。
- デフォルトビルドは `NoFederation` プロバイダーを同梱する: 両エンドポイントは
  `501 sso_not_configured` を返す——IdP が配線されていない状態で、面が正直に
  公示される。プロトコルを完遂するフェデレーションプロバイダーは
  エンタープライズビルドの一部であり、**起動時に環境によって構成される**
  （`OLIVARES_SSO_PROTOCOL`、OIDC 用の `OLIVARES_OIDC_*` 一式、SAML 用の
  `OLIVARES_SAML_*` 一式）。
- IdP が持つべきリダイレクト/ACS URI は**厳密**だ
  （コンソールオリジン上の `…/v1/auth/federation/callback` ——RFC 9700 の
  厳密一致、プレフィックスの小細工は不可）。

コンソールの **Identity & NHI → SSO & SCIM** タブはライブ構成を文書化し、
IdP のリダイレクト URI を厳密に期待される値と照合し、接続状態を表示する——
そして、あるパネルのバックエンドが宣言された契約でまだ稼働していない場合、
捏造データをレンダリングするのではなく「backend pending」と表示する:

<img class="light:sl-hidden" src="/console/identity-dark.png" alt="Identity & NHI ビュー: 厳密なリダイレクト URI 照合付きの SSO 構成、NHI ロスター、主要な態勢タブ。" />
<img class="dark:sl-hidden" src="/console/identity-light.png" alt="Identity & NHI ビュー: 厳密なリダイレクト URI 照合付きの SSO 構成、NHI ロスター、主要な態勢タブ。" />

## 2. SCIM プロビジョニング（インバウンド）

コントロールプレーンは、次のエンドポイントで標準的な SCIM 2.0（RFC 7644）
サービスプロバイダーとして機能する:

```
/v1/scim/v2/Users
/v1/scim/v2/Groups
```

- **認証:** SCIM 連携に対するテナント束縛の**管理者/オーナー API トークン**
  ——API の他の部分と同じ不透明トークンモデルであり、別個の SCIM シークレット
  型はない。エンドポイントは常に存在する（フィーチャーゲートされない）。
- **Users** はプリンシパルをプロビジョニングおよびデプロビジョニングする。
  IdP によるデプロビジョニングは、HR がそう告げた瞬間にアクセスを失効させる。
- **Groups** はアイデンティティ対グループの参照データを運ぶ。各グループは
  `mapped_role` を介してコントロールプレーンのロールにマッピングできる——
  そしてそのマッピングは**オペレーター所有**だ: コントロールプレーン側で
  設定され、監査される（`scim.group.role.map`）。IdP のプッシュがロールを
  無言で昇格させることはない。プッシュされたグループ内の未知のメンバーは
  スキップされ、**かつ監査される**——捏造されることはない。

## 3. ロスターソース: LDAP、Okta、Entra ID

ロスターソースはモジュール VI のアイデンティティインベントリに供給し——
これが要点だが——モジュール III に帰属を格上げするバインディングを与える:

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

主要な LDAP オプション（同梱ディスクリプタより）: `user_filter` /
`group_filter`、`privileged_group_dns`（そのメンバーシップ自体が
特権アクセスのシグナルとなるグループ）、`nhi_dn_suffix`（非人間
アイデンティティを保持するサブツリー）、`start_tls`、`page_size`。`idp` kind は
`provider: okta`（`api_token` 付き）または `provider: entra`（`tenant_id` /
`client_id` / `client_secret` 付き）を取る。`okta` と `entra` は `kind` として
直接機能させることもできる。

### これがどのように帰属を格上げするか——正確に

ロスターソースはアイデンティティを（external id で）登録し、ディレクトリが
宣言している場合は**許可されたグラント**も登録する。観測されたエッジの起点が
**非共有**のロスターアイデンティティに一致すると、モジュール III はアクセスを
そのアイデンティティに結びつけ、エッジの信頼度は `attributed` に格上げされる。
複数のワークロードが共有するアイデンティティは正直に `approximate` のまま残る
——ロスターはクレデンシャルの共有を解消できない。それができるのはエージェント
ごとのアイデンティティを発行することだけだ
（[ガバナンスへの橋渡し](/ja/how-to/govern-and-approve/)）。

専用の**エージェントアイデンティティおよびワークロードアイデンティティの kind**
（エージェントフェデレーションソース——Entra Agent ID、AgentCore、SPIFFE および
同種のもの）は、確かなエージェントごとのシグナルだ。グループ/ディレクトリの
ロスターは人とサービスアカウントを鋭くする。

## 正直な限界

- **SSO はエンタープライズビルドで完遂する。** シーム、フローのセキュリティ、
  501 態勢はすべてのビルドにある。プロトコルプロバイダーはそうではない。
- **ロスターは共有クレデンシャルを修正できない。** クレデンシャルが共有されて
  いることを正直に告げることだけができる。
- **SCIM はインバウンドプロビジョニングだ** ——コントロールプレーンは
  アイデンティティを IdP に押し返さず、Security-Event-Token レシーバーは
  アウトバウンド webhook ではなくインバウンドの面だ。

## 関連

- [ソースを接続する](/ja/how-to/connect-a-source/#厳しい依存関係-エージェント単位のアイデンティティ)
  — なぜアイデンティティが厳しい依存関係なのか。
- [統治と承認](/ja/how-to/govern-and-approve/) — ロール、RBAC、そして
  `mapped_role` が付与するもの。
- [コネクタとカバレッジティア](/ja/reference/connectors/) — 完全な
  アイデンティティソース一覧（Vault、Infisical、Keycloak、SPIFFE、
  エージェントアイデンティティのフェデレーション kind）。
