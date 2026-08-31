---
title: "TAK Server のポスチャとガバナンス対象 Cursor-on-Target ingest"
description: >-
  TAK deployment をガバナンスします。CoreConfig.xml から TAK Server の
  ポスチャをオフラインで読み取り（任意で live の version probe を追加）、
  Cursor-on-Target event を UDP/TCP 経由で最小データのガバナンス対象 signal
  として ingest します。座標と detail は決して connector の外に出ず、すべての
  edge を事実どおり approximate と評価します。
sidebar:
  order: 9
---

`tak` source は、**TAK**（Team Awareness Kit）deployment をもう 1 つの surface
としてガバナンスします。互いに独立した次の 2 つの機能があり、片方だけでも有効に
できます。

- **TAK Server のポスチャ** — server の設定（input とその protocol/port、
  TLS/keystore 設定、certificate-signing backend）を、最小データの finding として
  報告します。根拠となる source は server 自身の `CoreConfig.xml` であり、disk
  から**オフライン**で読み取ります。network 経由で読むのは、任意の live
  **version probe** だけです。TAK federation は読み取りません。
- **ガバナンス対象 CoT ingest** — connector 自身の **UDP** および **TCP** listener
  で **Cursor-on-Target** event を受信し、各 event をガバナンス対象 access edge
  に変換します。

connector は**読み取り優先**です。TAK Server に書き込まず、federation に参加せず、
payload を再送信することも決してありません。credential も listener も設定されて
いない場合は、事実どおり**何もしません**。接触していない deployment のポスチャを
捏造せず、何も出力しません。

## 出力する内容

| フィールド | 値 |
|---|---|
| Signal source | `cot` |
| Mode | `write` — CoT emitter は situational-awareness state を feed に*提供*します |
| Origin | emitter の `uid`。**デフォルトで hash 化**（`cot_uid_mode`） |
| Confidence | 常に **`approximate`** — base CoT は認証されていません（後述） |
| Findings | drop-track cancellation、誤差が無制限の event、listener の拒否（rate-limit / oversize / malformed / conn-limit）を集約したもの |

## 1. ポスチャ: server を最初にオフラインで読む

根拠となるポスチャ source は server 自身の設定 file です。package install では
`/opt/tak/CoreConfig.xml` にあります。この file を connector に指定すると、network
を一切使用せず、設定済みの input、TLS/keystore 設定、certificate-signing backend
を読み取ります。`<federation>` element は意図的にモデル化していないため、
federation のポスチャは生成されません。

live の **version probe** は任意で、追加するのは実行中の version だけです。
TAK Server は運用者を **mTLS** で認証するため、この probe はデニークローズドです。
`posture` を有効にして `server_url` を設定しながら client certificate を**省略**
すると、匿名で probe して認証していないポスチャを報告する代わりに、connector は
**起動を拒否**します。`server_url` は `https` でなければなりません。

```jsonc
// OLIVARES_SOURCES_CONFIG — posture only
{
  "sources": [{
    "name": "tak-server",
    "kind": "tak",
    "tenant": "<tenant-id>",
    "config": {
      "core_config_path": "/opt/tak/CoreConfig.xml",
      "server_url": "https://takserver.example.mil:8443",
      "client_cert": "${TAK_CLIENT_CERT_PEM}",
      "client_key":  "${TAK_CLIENT_KEY_PEM}"
    }
  }]
}
```

## 2. Ingest: UDP と TCP で CoT を受信する

listener を有効にすると、connector は UDP datagram ごとに 1 message、TCP connection
ごとに 1 message（「open-squirt-close」）の CoT を受信します。TAK feed または CoT
client の送信先を connector の listen address に設定します。connector は consumer
であり、server に接続して pull することはありません。

```jsonc
// OLIVARES_SOURCES_CONFIG — ingest
{
  "sources": [{
    "name": "tak-edge",
    "kind": "tak",
    "tenant": "<tenant-id>",
    "config": {
      "cot_udp_listen": "0.0.0.0:6969",
      "cot_multicast_group": "239.2.3.1",
      "cot_tcp_listen": "0.0.0.0:8087",
      "allow_public_bind": true,
      "feed_ref": "tak"
    }
  }]
}
```

### 設定 key（connector に同梱された descriptor から）

| Key | Type | Default | Secret | 意味 |
|---|---|---|:--:|---|
| `core_config_path` | string | — | no | `CoreConfig.xml` への path（package install: `/opt/tak/CoreConfig.xml`）。根拠となるオフラインのポスチャ source |
| `server_url` | string | — | no | TAK Server の base URL（例: `https://takserver.example.mil:8443`）。任意で、live version probe だけを有効にします |
| `version_path` | string | `/Marti/api/version` | no | `server_url` 上の Marti version endpoint。tak.gov の API reference は account が必要なため設定可能です |
| `client_cert` | string | — | **yes** | TAK Server mTLS 用の PEM client certificate。参照で指定 |
| `client_key` | string | — | **yes** | client certificate の PEM private key。参照で指定 |
| `ca_cert` | string | — | no | TAK Server certificate 用の PEM CA bundle。空なら host の trust store を使用します |
| `posture` | bool | `true` | no | TAK Server のポスチャ finding を出力します |
| `request_timeout` | duration | `15s` | no | TAK Server API に対する request ごとの timeout |
| `feed_ref` | string | `tak` | no | この CoT feed の安定した参照。sourcescope binding が `source_type=data` で scope を設定する `source_ref` |
| `cot_udp_listen` | string | — | no | CoT の UDP listen address（例: `127.0.0.1:6969`）。空なら UDP ingest を無効にします |
| `cot_tcp_listen` | string | — | no | CoT open-squirt-close の TCP listen address（例: `127.0.0.1:8087`）。空なら TCP ingest を無効にします |
| `cot_multicast_group` | string | — | no | UDP listener で参加する任意の multicast group（TAK の SA default は `239.2.3.1`） |
| `cot_max_event_bytes` | int | `65536` | no | 1 件の CoT event の最大 byte 数 |
| `cot_max_detail_bytes` | int | `32768` | no | 1 件の CoT event に含まれる opaque な `<detail>` 範囲の最大 byte 数 |
| `cot_rate_limit_eps` | int | `500` | no | すべての listener を通じて 1 秒あたりに受け入れる CoT event の最大数。超過分は drop して数を記録します |
| `cot_max_tcp_conns` | int | `128` | no | 同時に接続できる TCP CoT connection の最大数 |
| `cot_uid_mode` | string | `hash` | no | connector の外へ出す `uid` の形式: `hash`（デフォルト、一方向）または `raw`。uid は device を識別し、device はその所持者を識別します |

## Port（TAK Server Configuration Guide v5.2）

以下は連携先を理解するための情報です。connector 自身の listener は、設定した任意の
`host:port` に bind します。例でこれらの番号を再利用しているのは、分かりやすさの
ためだけです。

| Port / group | 慣例 |
|---|---|
| **8089** | TLS CoT streaming input — 認証済み client↔server channel |
| **6969** + multicast **239.2.3.1** | situational-awareness（SA）multicast group |
| **8087** | 慣例上の input port。guide の標準例では **UDP** として bind します。protocol は設定可能であり、8087 は本質的に TCP なのでは**ありません** |
| **8088** | `stcp` — 暗号化されていない TCP input。**test 専用** |
| **8443** | 管理用 web UI |
| **8446** | certificate enrollment |

## Privacy: 座標と detail は決して connector の外に出ない

CoT は位置報告 protocol であり、この製品が ingest する signal の中で PII が最も
集中しています。そのため、データ最小化を厳格に強制します。

- `<point>` の `lat` / `lon` / `hae` は**決して connector の外に出ません**。座標は
  人の位置です。製品が記録するのは event の受信、emitter、CoT type であって、
  誰かがいた場所ではありません。
- opaque な `<detail>` 範囲は決して connector の外に出ません。保持するのは
  **サイズ**と **SHA-256 digest** だけなので、payload を保存せずに同一 payload を
  correlate できます。
- emitter の `uid` は**デフォルトで hash 化**されます（`cot_uid_mode=hash`、domain
  separation 付きの一方向変換）。`raw` は運用者が明示的に opt-in した場合だけです。

## Confidence: CoT uid は認証済み ID ではない

base CoT には**認証がありません**。listener に到達できる host は、どのような `uid`
でも主張できます。TAK Server の TLS が保護するのは client↔**server** channel
（port 8089）であり、この connector が自身の平文 UDP/TCP listener で受信する event
については何も保証しません。そのため、base CoT listener から生じる**すべての**
edge は、設計上 **`approximate`** と評価されます。`attributed` を返す code path は
存在しません。

:::caution[`uid` は主張であって、証明ではありません]
CoT の `uid` は、認証済み ID ではなく、*「この ID を名乗る emitter が feed に
publish した」*と解釈してください。listener が mTLS を terminate し、uid を peer
certificate に binding した場合にだけ認証済みになります。
:::

## Scope: sourcescope binding で feed をガバナンスする

feed は第一級のガバナンス対象 source です。**sourcescope** binding は、
`source_type=data` と `source_ref=<feed_ref>` を使って、任意の subject 軸
（**session / agent / user / user_group / role**）で利用者の scope を定めます。
effect は `allow`（デフォルト）または `forbid` で、**`forbid` は絶対的**です
（`forbid` が `allow` に優先します）。

```http
POST /v1/m/sourcescope/bindings
Content-Type: application/json

{
  "source_type": "data",
  "source_ref":  "tak",
  "scope_tree":  "agent",
  "scope_ref":   "agent:recon-planner",
  "effect":      "allow",
  "enabled":     true
}
```

`"effect": "forbid"`（たとえば `"scope_tree": "user_group"`）を設定すると、`allow`
が存在する場合でも group 全体の access を除外できます。

## License と clean-room の来歴

CoT wire format は、**公開された MITRE specification だけ**を使用して記述した
**clean-room** implementation です。TAK または ATAK の source code を読み、コピー
し、派生させたことはありません。

- *The Developer's Guide to Cursor on Target*, Butler, MITRE, Aug 2005 — DTIC
  **ADA637348**、MITRE **Case #06-0249**。
- `Event-PUBLIC.xsd`、CoT base-event schema（Version 2.0）— MITRE
  **Case #11-3895**。
- *TAK Server Configuration Guide* **v5.2** — port/protocol の慣例。

ATAK-CIV と TAK Server は **GPLv3** であり、connector（Apache-2.0）からの利用は禁止
されています。この境界は license boundary check によって強制されます。両者には
米国連邦政府の **「Distribution A」**表示がありますが、これは**政府による公開の
声明であって、software license ではありません**。code tree の license は GPLv3
です。clean-room implementation を正当なものにしているのは、MITRE が公開した
schema と guide です。

## 明示されている制限

- **mesh/radio bearer はありません** — UDP と TCP だけで、serial、TAK mesh、
  radio には対応しません。
- **ATAK/WinTAK plugin はありません** — connector は end-user 向け TAK client を
  実装しません。
- **TAK federation はありません** — federation が設定されていることを*観測する*
  だけで、federate は決してしません。
- **Link-16 / MIL-STD** や認証を要する tactical protocol はなく、**Iron Bank /
  DoD accreditation もありません**。これらは独立した任意の customer path です。
- **CoT の `<detail>` sub-schema はモデル化していません** — 解析するのは base
  event だけです。detail は opaque で、サイズ上限を設け、digest だけを取る byte
  列です。
- **UDP loss は数えられません** — backpressure は listener を遅らせます。UDP では
  この process が datagram を見る前に**kernel**が drop するため、その drop 数は
  記録できません。connector が実際に拒否した event だけが rejection finding に
  集約されます。

## 関連項目

- [source を接続する](/ja/how-to/connect-a-source/) — connector model と正確な tier
  taxonomy。
- [ガバナンスと承認](/ja/how-to/govern-and-approve/) — sourcescope binding が接続する
  authorization model。
- [Connector と coverage tier](/ja/reference/connectors/) — catalog 全体。
