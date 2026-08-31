---
title: "PostgreSQL pgAudit（clean 层 R/RW）"
description: >-
  从 PostgreSQL 原生的 pgAudit 审计轨迹捕获对它的读/写访问 —— clean 层信号：
  READ/WRITE 逐字取自审计 CLASS 字段，绝不从 SQL 推断，且 connector 只读取日志文件。
sidebar:
  order: 1
---

`pgaudit` 来源把 PostgreSQL 自己的审计轨迹转化为 access-map 的边：每一次被审计的数据
访问对应一条边，其读/写模式**逐字取自 pgAudit 的 CLASS 字段** —— 绝不从 SQL 文本推断。
它是规范化的 **clean 层（clean tier）** 来源：一个在其原生轨迹中对访问进行分类的
对象型/关系型存储。

该 connector 是**对日志文件只读**的。它从不连接数据库，从不看到查询结果，也从不捕获
SQL 主体 —— 身份、对象与分类都是 pgAudit 自己的输出。

## 它发出什么

| 字段 | 取值 |
|---|---|
| Signal source | `pg_audit` |
| Mode | 逐字来自 CLASS：READ → `read`，WRITE → `write`，DDL → `write`（一次 schema 写入），FUNCTION → `unknown`（pgAudit 不说明）；ROLE/MISC 被跳过，而非臆测 |
| 发起方 | 存在时取 `application_name`（→ `attributed`），否则取会话角色 |
| Confidence | `attributed`，对你声明为共享的角色/应用则为 `approximate` |
| Coverage tier | clean |

## 1. 打开 pgAudit、结构化日志、UTC

在 PostgreSQL 一侧（标准的 pgAudit 配置 —— 针对你的主版本参见 pgAudit 文档）：

```ini
# postgresql.conf
shared_preload_libraries = 'pgaudit'
pgaudit.log = 'read, write'        # the classes this source consumes
logging_collector = on
log_destination = 'csvlog'         # or 'jsonlog' (PostgreSQL 15+)
log_timezone = 'UTC'               # REQUIRED — see below
```

有两条约束来自 connector 的解析方式，两者都已对照其实现验证：

- **服务器必须以 UTC 记录日志。** PostgreSQL 写入时间戳时带的是时区*缩写*，而非 UTC
  的缩写无法可靠地解析为偏移量 —— 因此 connector 会**跳过**这类记录，而不是臆测出一个
  错误的时间戳。`log_timezone = 'UTC'` 是受支持的配置。
- **`csvlog` 为批处理；`jsonlog` 可跟随。** csvlog 记录可能跨越换行，因此该格式在每一遍
  都按批读取；`jsonlog` 以行分隔，支持持续 tail（`follow`，默认值）。

要让归属更精准，让各应用按 agent 设置 `application_name` —— 这正是把一条边从共享角色
升级为已归属发起方的关键
（参见[身份依赖](/zh/how-to/connect-a-source/#硬性依赖每代理身份)）。

## 2. 声明该来源

在你的[来源配置](/zh/how-to/connect-a-source/#连接一个真实的源)
（`OLIVARES_SOURCES_CONFIG`）中：

```json
{
  "sources": [{
    "name": "salesdb-pgaudit",
    "kind": "pgaudit",
    "tenant": "<tenant-id>",
    "config": {
      "log_path": "/var/log/postgresql/postgresql.csv",
      "format": "csvlog",
      "shared_accounts": "etl_role,app_pool"
    }
  }]
}
```

配置键（来自 connector 随附的描述符）：

| 键 | 必填 | 默认值 | 含义 |
|---|---|---|---|
| `log_path` | 是 | — | 引擎主机可读取的 PostgreSQL 日志文件路径 |
| `format` | 否 | `csvlog` | `csvlog` 或 `jsonlog` |
| `follow` | 否 | `true` | 持续 tail（**仅 jsonlog** —— csvlog 为批处理） |
| `shared_accounts` | 否 | — | 逗号分隔的共享角色 / application_names；它们的边会被诚实地标记为 `approximate` |

重启引擎，并确认引导行 `ingest: wired source … kind=pgaudit`。

## 3. 你将在控制台看到什么

打开 **Access map**。每一次被审计的访问都会渲染为一条从角色或应用指向表的边，按读或写
着色，并在 Postgres 资源上带有 `CLEAN` 覆盖徽标。**Permitted vs observed** 面板会浮现
任何没有匹配 grant 的访问 —— 当 pgAudit 已接入且尚未声明任何 grant 时，*每一次*被观测到
的访问都是诚实的漂移（drift），这正是符合预期的初始状态。

## 诚实的局限

- **它只能看到 pgAudit 记录的内容。** 你未启用的类（`pgaudit.log`）不会被观测到；当某个
  类被关闭时，边的缺失并不能证明不存在访问。
- **归属属于数据库。** 一个没有 `application_name` 的共享角色会把多个调用方坍缩到一个
  身份上 —— 把它声明到 `shared_accounts` 中，让 map 显示 `approximate`，而不是假装。
- **FUNCTION 按设计为 `unknown`** —— 执行一个函数可能读也可能写，而 pgAudit 不说明是
  哪一种；本产品不会强加一个标签。非数据类（ROLE、MISC）会被跳过，而不是发出毫无意义的边。

## 相关

- [接入一个来源](/zh/how-to/connect-a-source/) —— connector 模型与诚实分层（honest-tier）
  分类法。
- [CloudTrail](/zh/how-to/connectors/cloudtrail/) —— 面向 S3 对象的同一套 clean 层理念。
- [Connector 与覆盖层](/zh/reference/connectors/) —— 完整目录。
