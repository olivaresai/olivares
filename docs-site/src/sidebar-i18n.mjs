// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Sidebar label translations. The Diátaxis sidebar in astro.config.mjs
// uses explicit English labels; this attaches per-locale `translations` to each
// one so the navigation localizes alongside the page content. Leaf entries that
// rely on `autogenerate` localize automatically from each locale's page title.
//
// Machine-translated; English is authoritative (native review pending). Generated
// from the docs-i18n translation pass — to regenerate, re-run that pass. A label
// with no entry (or a missing locale) falls back to the English label.
//
// Keys are Starlight `translations` keys and MUST be each locale's BCP-47 `lang`
// from src/site-locales.mjs — for Chinese that is "zh-CN", not "zh" (with
// "zh" keys every zh page silently fell back to the English sidebar).

/** @type {Record<string, Record<string, string>>} */
export const SIDEBAR_LABELS = {
  "API & manage-as-code (platform)": {
    "es": "API y gestión como código (plataforma)",
    "zh-CN": "API 与代码化管理（平台）",
    "ru": "API и управление как код (платформа)",
    "ja": "API とコード管理 (プラットフォーム)",
    "de": "API & Manage-as-Code (Plattform)",
    "fr": "API et gestion-as-code (plateforme)"
  },
  "API stability & deprecation policy": {
    "es": "Estabilidad de la API y política de obsolescencia",
    "zh-CN": "API 稳定性与弃用策略",
    "ru": "Стабильность API и политика устаревания",
    "ja": "API の安定性と非推奨ポリシー",
    "de": "API-Stabilität & Deprecation-Richtlinie",
    "fr": "Stabilité de l'API et politique de dépréciation"
  },
  "Access & resource map (R/RW)": {
    "es": "Mapa de acceso y recursos (R/RW)",
    "zh-CN": "访问与资源映射（R/RW）",
    "ru": "Карта доступа и ресурсов (R/RW)",
    "ja": "アクセスとリソースマップ (R/RW)",
    "de": "Zugriffs- & Ressourcenkarte (R/RW)",
    "fr": "Carte des accès et ressources (R/RW)"
  },
  "Agent simulation/testing sandbox": {
    "es": "Sandbox de simulación y pruebas de agentes",
    "zh-CN": "智能体模拟/测试沙箱",
    "ru": "Песочница для симуляции и тестирования агентов",
    "ja": "エージェントのシミュレーション/テスト用サンドボックス",
    "de": "Sandbox für Agentensimulation/-tests",
    "fr": "Bac à sable de simulation/test d'agents"
  },
  "Architecture": {
    "es": "Arquitectura",
    "zh-CN": "架构",
    "ru": "Архитектура",
    "ja": "アーキテクチャ",
    "de": "Architektur",
    "fr": "Architecture"
  },
  "Architecture decisions (ADR)": {
    "es": "Decisiones de arquitectura (ADR)",
    "zh-CN": "架构决策（ADR）",
    "ru": "Архитектурные решения (ADR)",
    "ja": "アーキテクチャ決定 (ADR)",
    "de": "Architekturentscheidungen (ADR)",
    "fr": "Décisions d'architecture (ADR)"
  },
  "Back up and restore": {
    "es": "Copia de seguridad y restauración",
    "zh-CN": "备份与恢复",
    "ru": "Резервное копирование и восстановление",
    "ja": "バックアップと復元",
    "de": "Sichern und wiederherstellen",
    "fr": "Sauvegarder et restaurer"
  },
  "Build and ship a connector": {
    "es": "Crear y publicar un conector",
    "zh-CN": "构建并发布连接器",
    "ru": "Создание и публикация коннектора",
    "ja": "コネクターの構築と公開",
    "de": "Connector entwickeln und ausliefern",
    "fr": "Créer et publier un connecteur"
  },
  "CLI": {
    "es": "CLI",
    "zh-CN": "CLI",
    "ru": "CLI",
    "ja": "CLI",
    "de": "CLI",
    "fr": "CLI"
  },
  "Claude Code adoption": {
    "es": "Adopción de Claude Code",
    "zh-CN": "Claude Code 采用情况",
    "ru": "Внедрение Claude Code",
    "ja": "Claude Code の利用状況",
    "de": "Claude-Code-Adoption",
    "fr": "Adoption de Claude Code"
  },
  "Compliance & regulatory": {
    "es": "Cumplimiento y normativa",
    "zh-CN": "合规与监管",
    "ru": "Соответствие и нормативные требования",
    "ja": "コンプライアンスと規制",
    "de": "Compliance & Regulatorik",
    "fr": "Conformité et réglementation"
  },
  "Console screens & permissions": {
    "es": "Pantallas de la consola y permisos",
    "zh-CN": "控制台页面与权限",
    "ru": "Экраны консоли и разрешения",
    "ja": "コンソール画面と権限",
    "de": "Konsolen-Bildschirme & Berechtigungen",
    "fr": "Écrans de la console et permissions"
  },
  "Configuration": {
    "es": "Configuración",
    "zh-CN": "配置",
    "ru": "Конфигурация",
    "ja": "設定",
    "de": "Konfiguration",
    "fr": "Configuration"
  },
  "Connect & observe": {
    "es": "Conectar y observar",
    "zh-CN": "连接与观测",
    "ru": "Подключение и наблюдение",
    "ja": "接続と観測",
    "de": "Verbinden & beobachten",
    "fr": "Connecter et observer"
  },
  "Connect Claude Code": {
    "es": "Conectar Claude Code",
    "zh-CN": "接入 Claude Code",
    "ru": "Подключить Claude Code",
    "ja": "Claude Code を接続する",
    "de": "Claude Code verbinden",
    "fr": "Connecter Claude Code"
  },
  "Connect a source": {
    "es": "Conectar una fuente",
    "zh-CN": "连接数据源",
    "ru": "Подключить источник",
    "ja": "ソースを接続する",
    "de": "Quelle verbinden",
    "fr": "Connecter une source"
  },
  "Connector guides": {
    "es": "Guías de conectores",
    "zh-CN": "连接器指南",
    "ru": "Руководства по коннекторам",
    "ja": "コネクターガイド",
    "de": "Connector-Anleitungen",
    "fr": "Guides des connecteurs"
  },
  "Connectors & coverage tiers": {
    "es": "Conectores y niveles de cobertura",
    "zh-CN": "连接器与覆盖层级",
    "ru": "Коннекторы и уровни охвата",
    "ja": "コネクターとカバレッジ階層",
    "de": "Connectors & Abdeckungsstufen",
    "fr": "Connecteurs et niveaux de couverture"
  },
  "Cookbook": {
    "es": "Recetario",
    "zh-CN": "实战手册",
    "ru": "Сборник рецептов",
    "ja": "クックブック",
    "de": "Kochbuch",
    "fr": "Livre de recettes"
  },
  "Cost & AI FinOps": {
    "es": "Coste y FinOps de IA",
    "zh-CN": "成本与 AI FinOps",
    "ru": "Затраты и AI FinOps",
    "ja": "コストと AI FinOps",
    "de": "Kosten & AI-FinOps",
    "fr": "Coûts et FinOps de l'IA"
  },
  "Data, knowledge & context": {
    "es": "Datos, conocimiento y contexto",
    "zh-CN": "数据、知识与上下文",
    "ru": "Данные, знания и контекст",
    "ja": "データ・ナレッジ・コンテキスト",
    "de": "Daten, Wissen & Kontext",
    "fr": "Données, connaissances et contexte"
  },
  "Deploy with Docker": {
    "es": "Desplegar con Docker",
    "zh-CN": "使用 Docker 部署",
    "ru": "Развёртывание с Docker",
    "ja": "Docker でデプロイする",
    "de": "Mit Docker bereitstellen",
    "fr": "Déployer avec Docker"
  },
  "Deployment & integration": {
    "es": "Despliegue e integración",
    "zh-CN": "部署与集成",
    "ru": "Развёртывание и интеграция",
    "ja": "デプロイと連携",
    "de": "Bereitstellung & Integration",
    "fr": "Déploiement et intégration"
  },
  "EU AI Act evidence from runtime data": {
    "es": "Evidencias del EU AI Act a partir de datos de ejecución",
    "zh-CN": "从运行时数据生成 EU AI Act 证据",
    "ru": "Доказательства EU AI Act из данных времени выполнения",
    "ja": "ランタイムデータからの EU AI Act 証跡",
    "de": "EU AI Act-Nachweise aus Laufzeitdaten",
    "fr": "Preuves EU AI Act à partir des données d'exécution"
  },
  "Enterprise OTel for Claude Code": {
    "es": "OTel empresarial para Claude Code",
    "zh-CN": "面向 Claude Code 的企业级 OTel",
    "ru": "Корпоративный OTel для Claude Code",
    "ja": "Claude Code 向けエンタープライズ OTel",
    "de": "Enterprise OTel für Claude Code",
    "fr": "OTel d'entreprise pour Claude Code"
  },
  "Event bus (AsyncAPI 3.0)": {
    "es": "Bus de eventos (AsyncAPI 3.0)",
    "zh-CN": "事件总线（AsyncAPI 3.0）",
    "ru": "Шина событий (AsyncAPI 3.0)",
    "ja": "イベントバス (AsyncAPI 3.0)",
    "de": "Event-Bus (AsyncAPI 3.0)",
    "fr": "Bus d'événements (AsyncAPI 3.0)"
  },
  "Eventing & webhooks": {
    "es": "Eventos y webhooks",
    "zh-CN": "事件与 Webhook",
    "ru": "События и вебхуки",
    "ja": "イベントと Webhook",
    "de": "Eventing & Webhooks",
    "fr": "Événements et webhooks"
  },
  "Executive dashboards & reporting (console)": {
    "es": "Cuadros de mando ejecutivos e informes (consola)",
    "zh-CN": "管理层仪表盘与报表（控制台）",
    "ru": "Управленческие дашборды и отчётность (консоль)",
    "ja": "エグゼクティブ向けダッシュボードとレポート (コンソール)",
    "de": "Management-Dashboards & Reporting (Konsole)",
    "fr": "Tableaux de bord exécutifs et rapports (console)"
  },
  "Explanation": {
    "es": "Explicación",
    "zh-CN": "原理说明",
    "ru": "Пояснение",
    "ja": "解説",
    "de": "Erläuterung",
    "fr": "Explication"
  },
  "Forward to Splunk": {
    "es": "Reenviar a Splunk",
    "zh-CN": "转发至 Splunk",
    "ru": "Пересылка в Splunk",
    "ja": "Splunk へ転送する",
    "de": "An Splunk weiterleiten",
    "fr": "Transférer vers Splunk"
  },
  "From zero to a read/write access graph": {
    "es": "De cero a un grafo de acceso de lectura/escritura",
    "zh-CN": "从零构建读写访问图",
    "ru": "От нуля до графа доступа на чтение/запись",
    "ja": "ゼロから読み取り/書き込みアクセスグラフまで",
    "de": "Von null zum Lese-/Schreib-Zugriffsgraphen",
    "fr": "De zéro à un graphe d'accès lecture/écriture"
  },
  "Getting started by scenario": {
    "es": "Primeros pasos por escenario",
    "zh-CN": "按场景快速上手",
    "ru": "Начало работы по сценариям",
    "ja": "シナリオ別の入門",
    "de": "Erste Schritte nach Szenario",
    "fr": "Premiers pas par scénario"
  },
  "gRPC services & methods": {
    "es": "Servicios y métodos gRPC",
    "zh-CN": "gRPC 服务与方法",
    "ru": "Сервисы и методы gRPC",
    "ja": "gRPC のサービスとメソッド",
    "de": "gRPC-Dienste & -Methoden",
    "fr": "Services et méthodes gRPC"
  },
  "Glossary": {
    "es": "Glosario",
    "zh-CN": "术语表",
    "ru": "Глоссарий",
    "ja": "用語集",
    "de": "Glossar",
    "fr": "Glossaire"
  },
  "Govern & build": {
    "es": "Gobernar y construir",
    "zh-CN": "治理与构建",
    "ru": "Управление и разработка",
    "ja": "ガバナンスと構築",
    "de": "Steuern & entwickeln",
    "fr": "Gouverner et construire"
  },
  "Govern and approve": {
    "es": "Gobernar y aprobar",
    "zh-CN": "治理与审批",
    "ru": "Управление и согласование",
    "ja": "ガバナンスと承認",
    "de": "Steuern und freigeben",
    "fr": "Gouverner et approuver"
  },
  "Govern Postgres content": {
    "es": "Gobernar contenido de Postgres",
    "zh-CN": "治理 Postgres 内容",
    "ru": "Управление контентом Postgres",
    "ja": "Postgres コンテンツのガバナンス",
    "de": "Postgres-Inhalte steuern",
    "fr": "Gouverner le contenu Postgres"
  },
  "Govern your file server": {
    "es": "Gobernar tu servidor de archivos",
    "zh-CN": "治理你的文件服务器",
    "ru": "Управление файловым сервером",
    "ja": "ファイルサーバーのガバナンス",
    "de": "Dateiserver steuern",
    "fr": "Gouverner votre serveur de fichiers"
  },
  "Governed data for Claude": {
    "es": "Datos gobernados para Claude",
    "zh-CN": "面向 Claude 的受治理数据",
    "ru": "Управляемые данные для Claude",
    "ja": "Claude 向けのガバナンス済みデータ",
    "de": "Governed Data für Claude",
    "fr": "Données gouvernées pour Claude"
  },
  "Harden a deployment": {
    "es": "Fortalecer un despliegue",
    "zh-CN": "强化部署安全",
    "ru": "Усиление защиты развёртывания",
    "ja": "デプロイを堅牢化する",
    "de": "Bereitstellung härten",
    "fr": "Renforcer un déploiement"
  },
  "Health, SLA & uptime": {
    "es": "Estado, SLA y disponibilidad",
    "zh-CN": "健康状况、SLA 与可用性",
    "ru": "Состояние, SLA и доступность",
    "ja": "ヘルス・SLA・稼働時間",
    "de": "Health, SLA & Verfügbarkeit",
    "fr": "Santé, SLA et disponibilité"
  },
  "Honesty & limits": {
    "es": "Honestidad y límites",
    "zh-CN": "诚实声明与局限",
    "ru": "Честность и ограничения",
    "ja": "誠実さと制約",
    "de": "Ehrlichkeit & Grenzen",
    "fr": "Honnêteté et limites"
  },
  "How this documentation is organized": {
    "es": "Cómo está organizada esta documentación",
    "zh-CN": "本文档的组织方式",
    "ru": "Как организована эта документация",
    "ja": "このドキュメントの構成",
    "de": "Wie diese Dokumentation aufgebaut ist",
    "fr": "Comment cette documentation est organisée"
  },
  "How-to guides": {
    "es": "Guías prácticas",
    "zh-CN": "操作指南",
    "ru": "Практические руководства",
    "ja": "ハウツーガイド",
    "de": "Anleitungen",
    "fr": "Guides pratiques"
  },
  "Identity, permissions & governance": {
    "es": "Identidad, permisos y gobernanza",
    "zh-CN": "身份、权限与治理",
    "ru": "Идентификация, разрешения и управление",
    "ja": "アイデンティティ・権限・ガバナンス",
    "de": "Identität, Berechtigungen & Governance",
    "fr": "Identité, permissions et gouvernance"
  },
  "Inline inference proxy (PEP)": {
    "es": "Proxy de inferencia en línea (PEP)",
    "zh-CN": "内联推理代理（PEP）",
    "ru": "Встроенный прокси вывода (PEP)",
    "ja": "インライン推論プロキシ (PEP)",
    "de": "Inline-Inferenz-Proxy (PEP)",
    "fr": "Proxy d'inférence en ligne (PEP)"
  },
  "Install & operate": {
    "es": "Instalar y operar",
    "zh-CN": "安装与运维",
    "ru": "Установка и эксплуатация",
    "ja": "インストールと運用",
    "de": "Installieren & betreiben",
    "fr": "Installer et exploiter"
  },
  "Install air-gapped": {
    "es": "Instalación aislada (air-gapped)",
    "zh-CN": "离线（隔离网络）安装",
    "ru": "Установка в изолированной среде",
    "ja": "エアギャップ環境にインストールする",
    "de": "Air-Gapped installieren",
    "fr": "Installer en environnement isolé"
  },
  "Internal catalog & marketplace": {
    "es": "Catálogo interno y marketplace",
    "zh-CN": "内部目录与市场",
    "ru": "Внутренний каталог и маркетплейс",
    "ja": "社内カタログとマーケットプレイス",
    "de": "Interner Katalog & Marketplace",
    "fr": "Catalogue interne et place de marché"
  },
  "Inventory & discovery": {
    "es": "Inventario y descubrimiento",
    "zh-CN": "清点与发现",
    "ru": "Инвентаризация и обнаружение",
    "ja": "インベントリと検出",
    "de": "Inventar & Discovery",
    "fr": "Inventaire et découverte"
  },
  "Live operation & sessions": {
    "es": "Operación en vivo y sesiones",
    "zh-CN": "实时运维与会话",
    "ru": "Работа в реальном времени и сессии",
    "ja": "ライブ運用とセッション",
    "de": "Live-Betrieb & Sitzungen",
    "fr": "Exploitation en direct et sessions"
  },
  "Live-ingest": {
    "es": "Ingesta en vivo",
    "zh-CN": "实时采集",
    "ru": "Приём в реальном времени",
    "ja": "ライブ取り込み",
    "de": "Live-Ingest",
    "fr": "Ingestion en direct"
  },
  "MCP, skills & capabilities": {
    "es": "MCP, skills y capacidades",
    "zh-CN": "MCP、技能与能力",
    "ru": "MCP, навыки и возможности",
    "ja": "MCP・スキル・ケイパビリティ",
    "de": "MCP, Skills & Fähigkeiten",
    "fr": "MCP, compétences et capacités"
  },
  "Manage as code (Terraform)": {
    "es": "Gestión como código (Terraform)",
    "zh-CN": "代码化管理（Terraform）",
    "ru": "Управление как код (Terraform)",
    "ja": "コードとして管理 (Terraform)",
    "de": "Manage-as-Code (Terraform)",
    "fr": "Gestion as code (Terraform)"
  },
  "Model & provider management": {
    "es": "Gestión de modelos y proveedores",
    "zh-CN": "模型与供应商管理",
    "ru": "Управление моделями и провайдерами",
    "ja": "モデルとプロバイダーの管理",
    "de": "Modell- & Anbieterverwaltung",
    "fr": "Gestion des modèles et fournisseurs"
  },
  "Model operations (own models)": {
    "es": "Operaciones de modelos (modelos propios)",
    "zh-CN": "模型运维（自有模型）",
    "ru": "Операции с моделями (собственные модели)",
    "ja": "モデルオペレーション (自社モデル)",
    "de": "Modellbetrieb (eigene Modelle)",
    "fr": "Opérations des modèles (modèles propres)"
  },
  "Modules catalog": {
    "es": "Catálogo de módulos",
    "zh-CN": "模块目录",
    "ru": "Каталог модулей",
    "ja": "モジュールカタログ",
    "de": "Modulkatalog",
    "fr": "Catalogue des modules"
  },
  "Monitor with Prometheus": {
    "es": "Monitorizar con Prometheus",
    "zh-CN": "使用 Prometheus 监控",
    "ru": "Мониторинг с Prometheus",
    "ja": "Prometheus で監視する",
    "de": "Mit Prometheus überwachen",
    "fr": "Surveiller avec Prometheus"
  },
  "Multi-tenancy & org management (platform)": {
    "es": "Multitenencia y gestión de organizaciones (plataforma)",
    "zh-CN": "多租户与组织管理（平台）",
    "ru": "Мультитенантность и управление организацией (платформа)",
    "ja": "マルチテナンシーと組織管理 (プラットフォーム)",
    "de": "Mandantenfähigkeit & Org-Verwaltung (Plattform)",
    "fr": "Multi-locataire et gestion des organisations (plateforme)"
  },
  "Observability": {
    "es": "Observabilidad",
    "zh-CN": "可观测性",
    "ru": "Наблюдаемость",
    "ja": "オブザーバビリティ",
    "de": "Observability",
    "fr": "Observabilité"
  },
  "Olivares AI control plane API": {
    "es": "API del plano de control de Olivares AI",
    "zh-CN": "Olivares AI 控制平面 API",
    "ru": "API плоскости управления Olivares AI",
    "ja": "Olivares AI コントロールプレーン API",
    "de": "Olivares AI Control-Plane-API",
    "fr": "API du control plane Olivares AI"
  },
  "Open core & licensing": {
    "es": "Núcleo abierto y licencias",
    "zh-CN": "开放核心与许可",
    "ru": "Открытое ядро и лицензирование",
    "ja": "オープンコアとライセンス",
    "de": "Open Core & Lizenzierung",
    "fr": "Open core et licences"
  },
  "Supporting the project": {
    "es": "Apoyar el proyecto",
    "zh-CN": "支持该项目",
    "ru": "Поддержка проекта",
    "ja": "プロジェクトを支援する",
    "de": "Das Projekt unterstützen",
    "fr": "Soutenir le projet"
  },
  "Orchestration & A2A": {
    "es": "Orquestación y A2A",
    "zh-CN": "编排与 A2A",
    "ru": "Оркестрация и A2A",
    "ja": "オーケストレーションと A2A",
    "de": "Orchestrierung & A2A",
    "fr": "Orchestration et A2A"
  },
  "Output integrations & notifications": {
    "es": "Integraciones de salida y notificaciones",
    "zh-CN": "输出集成与通知",
    "ru": "Интеграции вывода и уведомления",
    "ja": "出力連携と通知",
    "de": "Ausgabe-Integrationen & Benachrichtigungen",
    "fr": "Intégrations de sortie et notifications"
  },
  "Overview": {
    "es": "Visión general",
    "zh-CN": "概览",
    "ru": "Обзор",
    "ja": "概要",
    "de": "Übersicht",
    "fr": "Vue d'ensemble"
  },
  "Overview — the 30 modules": {
    "es": "Visión general — los 30 módulos",
    "zh-CN": "概览 —— 30 个模块",
    "ru": "Обзор — 30 модулей",
    "ja": "概要 — 30個のモジュール",
    "de": "Übersicht — die 30 Module",
    "fr": "Vue d'ensemble — les 30 modules"
  },
  "Fine-tuning & inference execution (planned)": {
    "es": "Ejecución de fine-tuning e inferencia (planificado)",
    "zh-CN": "微调与推理执行（规划中）",
    "ru": "Исполнение fine-tuning и инференса (планируется)",
    "ja": "ファインチューニングと推論の実行 (予定)",
    "de": "Fine-Tuning- & Inferenz-Ausführung (geplant)",
    "fr": "Exécution du fine-tuning et de l'inférence (prévu)"
  },
  "Paths by role": {
    "es": "Itinerarios por rol",
    "zh-CN": "按角色划分的路径",
    "ru": "Маршруты по ролям",
    "ja": "ロール別のパス",
    "de": "Pfade nach Rolle",
    "fr": "Parcours par rôle"
  },
  "Positioning & fit": {
    "es": "Posicionamiento y encaje",
    "zh-CN": "定位与适用场景",
    "ru": "Позиционирование и применимость",
    "ja": "ポジショニングと適合性",
    "de": "Positionierung & Einsatz",
    "fr": "Positionnement et adéquation"
  },
  "Posture export to control towers": {
    "es": "Exportación de postura a torres de control",
    "zh-CN": "态势导出至控制塔",
    "ru": "Экспорт состояния защиты в центры управления",
    "ja": "コントロールタワーへのポスチャエクスポート",
    "de": "Posture-Export an Control Towers",
    "fr": "Export de la posture vers les tours de contrôle"
  },
  "Privileged-session recording": {
    "es": "Grabación de sesiones privilegiadas",
    "zh-CN": "特权会话录制",
    "ru": "Запись привилегированных сессий",
    "ja": "特権セッションの記録",
    "de": "Aufzeichnung privilegierter Sitzungen",
    "fr": "Enregistrement des sessions privilégiées"
  },
  "Quality, evals & testing": {
    "es": "Calidad, evaluaciones y pruebas",
    "zh-CN": "质量、评估与测试",
    "ru": "Качество, оценки и тестирование",
    "ja": "品質・評価・テスト",
    "de": "Qualität, Evals & Tests",
    "fr": "Qualité, évaluations et tests"
  },
  "Quickstart": {
    "es": "Inicio rápido",
    "zh-CN": "快速开始",
    "ru": "Быстрый старт",
    "ja": "クイックスタート",
    "de": "Schnellstart",
    "fr": "Démarrage rapide"
  },
  "REST API": {
    "es": "REST API",
    "zh-CN": "REST API",
    "ru": "REST API",
    "ja": "REST API",
    "de": "REST API",
    "fr": "API REST"
  },
  "Red-teaming & adversarial testing": {
    "es": "Red-teaming y pruebas adversariales",
    "zh-CN": "红队与对抗测试",
    "ru": "Red-teaming и состязательное тестирование",
    "ja": "レッドチームと敵対的テスト",
    "de": "Red-Teaming & Adversarial-Tests",
    "fr": "Red teaming et tests adversariaux"
  },
  "Reference": {
    "es": "Referencia",
    "zh-CN": "参考",
    "ru": "Справочник",
    "ja": "リファレンス",
    "de": "Referenz",
    "fr": "Référence"
  },
  "Reporting (HTML/PDF)": {
    "es": "Informes (HTML/PDF)",
    "zh-CN": "报告（HTML/PDF）",
    "ru": "Отчёты (HTML/PDF)",
    "ja": "レポート (HTML/PDF)",
    "de": "Reporting (HTML/PDF)",
    "fr": "Rapports (HTML/PDF)"
  },
  "Run Claude Code with Olivares": {
    "es": "Ejecutar Claude Code con Olivares",
    "zh-CN": "在 Olivares 中运行 Claude Code",
    "ru": "Запуск Claude Code с Olivares",
    "ja": "Olivares で Claude Code を実行する",
    "de": "Claude Code mit Olivares ausführen",
    "fr": "Exécuter Claude Code avec Olivares"
  },
  "SIEM/ITSM forwarder": {
    "es": "Reenviador SIEM/ITSM",
    "zh-CN": "SIEM/ITSM 转发器",
    "ru": "Пересыльщик SIEM/ITSM",
    "ja": "SIEM/ITSM フォワーダー",
    "de": "SIEM/ITSM-Forwarder",
    "fr": "Relais SIEM/ITSM"
  },
  "Saved console views": {
    "es": "Vistas guardadas de la consola",
    "zh-CN": "已保存的控制台视图",
    "ru": "Сохранённые представления консоли",
    "ja": "保存済みコンソールビュー",
    "de": "Gespeicherte Konsolenansichten",
    "fr": "Vues enregistrées de la console"
  },
  "Security & threat model": {
    "es": "Seguridad y modelo de amenazas",
    "zh-CN": "安全与威胁模型",
    "ru": "Безопасность и модель угроз",
    "ja": "セキュリティと脅威モデル",
    "de": "Sicherheit & Bedrohungsmodell",
    "fr": "Sécurité et modèle de menaces"
  },
  "Security, guardrails & audit": {
    "es": "Seguridad, salvaguardas y auditoría",
    "zh-CN": "安全、护栏与审计",
    "ru": "Безопасность, ограничители и аудит",
    "ja": "セキュリティ・ガードレール・監査",
    "de": "Sicherheit, Guardrails & Audit",
    "fr": "Sécurité, garde-fous et audit"
  },
  "Self-host the control plane": {
    "es": "Autoalojar el plano de control",
    "zh-CN": "自托管控制平面",
    "ru": "Самостоятельный хостинг плоскости управления",
    "ja": "コントロールプレーンをセルフホストする",
    "de": "Control Plane selbst hosten",
    "fr": "Auto-héberger le control plane"
  },
  "Source & credential scoping": {
    "es": "Acotación de fuentes y credenciales",
    "zh-CN": "数据源与凭据范围限定",
    "ru": "Ограничение области источников и учётных данных",
    "ja": "ソースと認証情報のスコープ設定",
    "de": "Quellen- & Credential-Scoping",
    "fr": "Cadrage des sources et identifiants"
  },
  "Start here": {
    "es": "Empieza aquí",
    "zh-CN": "从这里开始",
    "ru": "Начните здесь",
    "ja": "ここから始める",
    "de": "Hier starten",
    "fr": "Commencer ici"
  },
  "Troubleshooting": {
    "es": "Resolución de problemas",
    "zh-CN": "故障排查",
    "ru": "Устранение неполадок",
    "ja": "トラブルシューティング",
    "de": "Fehlerbehebung",
    "fr": "Dépannage"
  },
  "Tutorials": {
    "es": "Tutoriales",
    "zh-CN": "教程",
    "ru": "Учебные пособия",
    "ja": "チュートリアル",
    "de": "Tutorials",
    "fr": "Tutoriels"
  },
  "Upgrade and roll back": {
    "es": "Actualizar y revertir",
    "zh-CN": "升级与回滚",
    "ru": "Обновление и откат",
    "ja": "アップグレードとロールバック",
    "de": "Upgrade und Rollback",
    "fr": "Mettre à niveau et revenir en arrière"
  },
  "Use the client SDKs": {
    "es": "Usar los SDK de cliente",
    "zh-CN": "使用客户端 SDK",
    "ru": "Использование клиентских SDK",
    "ja": "クライアント SDK を使う",
    "de": "Die Client-SDKs nutzen",
    "fr": "Utiliser les SDK clients"
  },
  "SIEM & telemetry egress": {
    "es": "Salida a SIEM y telemetría",
    "zh-CN": "SIEM 与遥测出口",
    "ru": "Экспорт в SIEM и телеметрия",
    "ja": "SIEM とテレメトリの送出",
    "de": "SIEM- & Telemetrie-Egress",
    "fr": "Export SIEM et télémétrie"
  },
  "Verified connectors (third-party)": {
    "es": "Conectores verificados (de terceros)",
    "zh-CN": "已验证连接器（第三方）",
    "ru": "Проверенные коннекторы (сторонние)",
    "ja": "検証済みコネクター (サードパーティ)",
    "de": "Verifizierte Connectors (Drittanbieter)",
    "fr": "Connecteurs vérifiés (tiers)"
  },
  "Verify a release": {
    "es": "Verificar una versión",
    "zh-CN": "验证发行版",
    "ru": "Проверка релиза",
    "ja": "リリースを検証する",
    "de": "Release verifizieren",
    "fr": "Vérifier une version"
  },
  "Voice & realtime agents": {
    "es": "Agentes de voz y en tiempo real",
    "zh-CN": "语音与实时智能体",
    "ru": "Голосовые агенты и агенты реального времени",
    "ja": "音声・リアルタイムエージェント",
    "de": "Sprach- & Echtzeit-Agenten",
    "fr": "Agents vocaux et temps réel"
  },
  "What is Olivares AI?": {
    "es": "¿Qué es Olivares AI?",
    "zh-CN": "什么是 Olivares AI？",
    "ru": "Что такое Olivares AI?",
    "ja": "Olivares AI とは?",
    "de": "Was ist Olivares AI?",
    "fr": "Qu'est-ce qu'Olivares AI ?"
  }
}

/**
 * Recursively attach `translations` to sidebar items whose English label has a
 * mapping in SIDEBAR_LABELS. Items without a string label (the OpenAPI sidebar
 * group placeholder, `autogenerate` directives) are returned untouched so their
 * identity and plugin wiring are preserved.
 *
 * @param {any[]} items
 * @returns {any[]}
 */
export function localizeSidebar(items) {
  return items.map((item) => {
    if (typeof item.label !== 'string') return item
    const next = { ...item }
    const map = SIDEBAR_LABELS[item.label]
    if (map && Object.keys(map).length > 0) next.translations = map
    if (Array.isArray(item.items)) next.items = localizeSidebar(item.items)
    return next
  })
}
