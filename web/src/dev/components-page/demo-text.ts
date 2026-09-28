// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The demo content of the capture-only components page, in the three languages the page is
// captured in (English, and German and Japanese for the text-fit check). It is fixture text
// with the design's neutral demo identities, not product copy: it never reaches a product
// route, so it stays out of the locale files. The components' own words come from the
// `ui.*` keys of `common.json` in all seven locales.

export type DemoLang = 'en' | 'de' | 'ja'

export interface DemoText {
  crumbLabel: string
  crumbUp: string
  crumbCurrent: string
  note: string
  views: { states: string; primitives: string }
  viewLabel: string
  names: {
    loading: string
    empty: string
    filtered: string
    error: string
    stale: string
    disabled: string
    noAccess: string
    unknown: string
  }
  loadingLabel: string
  emptyTitle: string
  emptyText: string
  newSession: string
  filteredTitle: string
  filters: string
  staleRows: Array<{ title: string; time: string }>
  deploy: string
  deployReason: string
  reviewPlan: string
  startSession: string
  signedOut: string
  signIn: string
  noAccessTitle: string
  askOwner: string
  startAgain: string
  sections: {
    buttons: string
    fields: string
    choices: string
    tabs: string
    tags: string
    glyphs: string
    marks: string
    code: string
  }
  buttons: {
    primary: string
    secondary: string
    quiet: string
    danger: string
    small: string
    large: string
  }
  fields: {
    host: string
    hostHint: string
    region: string
    regions: string[]
    notify: string
    reduceMotion: string
  }
  segments: string[]
  segmentReason: string
  tabs: Array<{ label: string; count?: string }>
  tabPanel: string
  tags: {
    ready: string
    signedOut: string
    update: string
    cannotCheck: string
    notInstalled: string
    scope: string
  }
  working: string
  marks: string[]
  command: string
}

const en: DemoText = {
  crumbLabel: 'Breadcrumb',
  crumbUp: 'Design system',
  crumbCurrent: 'States every list and action must show',
  note: 'Same components in every area',
  views: { states: 'States', primitives: 'Primitives' },
  viewLabel: 'Page',
  names: {
    loading: 'Loading',
    empty: 'Empty',
    filtered: 'Filtered',
    error: 'Error',
    stale: 'Stale data',
    disabled: 'Disabled with reason',
    noAccess: 'No access',
    unknown: 'Outcome not known',
  },
  loadingLabel: 'Loading sessions…',
  emptyTitle: 'No sessions in telescopes yet',
  emptyText: 'Start one here, or from a terminal.',
  newSession: 'New session',
  filteredTitle: 'No session matches these filters',
  filters: 'State: Failed · AI tool: Grok Build',
  staleRows: [
    { title: 'Move stream ingest to async', time: '6m' },
    { title: 'Publish camera calibration', time: '2m' },
    { title: 'Fix the OBS scene switcher', time: '1h' },
  ],
  deploy: 'Deploy to production',
  deployReason:
    'The plan changed after it was approved. Approve the new plan first.',
  reviewPlan: 'Review plan',
  startSession: 'Start session',
  signedOut: 'grok-b is signed out. Choose another account, or sign in again.',
  signIn: 'Sign in',
  noAccessTitle: 'You cannot open this workspace',
  askOwner: 'Ask the owner for access',
  startAgain: 'Start again',
  sections: {
    buttons: 'Buttons',
    fields: 'Fields',
    choices: 'Switches and segmented choices',
    tabs: 'Tabs',
    tags: 'Tags',
    glyphs: 'Status glyphs',
    marks: 'Mono marks',
    code: 'Command line',
  },
  buttons: {
    primary: 'Start session',
    secondary: 'Add account',
    quiet: 'Open diagnostics',
    danger: 'Remove account',
    small: 'Retry',
    large: 'Start setup',
  },
  fields: {
    host: 'Host',
    hostHint: 'A name or an address the engine can reach.',
    region: 'Region',
    regions: ['Europe (Madrid)', 'North America (Ohio)', 'Asia (Tokyo)'],
    notify: 'Notify me when a session needs me',
    reduceMotion: 'Reduce motion',
  },
  segments: ['List', 'Table', 'Board'],
  segmentReason: 'The board view needs a work item stream.',
  tabs: [
    { label: 'Items', count: '24' },
    { label: 'Decisions', count: '3' },
    { label: 'Changes' },
    { label: 'Context' },
  ],
  tabPanel: 'Work items registered by the sessions of this workspace.',
  tags: {
    ready: 'Ready',
    signedOut: 'Signed out',
    update: 'Update',
    cannotCheck: 'Cannot be checked',
    notInstalled: 'Not installed',
    scope: 'in telescopes',
  },
  working: 'Working',
  marks: ['Claude Code', 'Codex', 'Grok Build', 'OpenCode', 'telescopes'],
  command: 'olivares agent tool detect',
}

const de: DemoText = {
  crumbLabel: 'Brotkrumennavigation',
  crumbUp: 'Designsystem',
  crumbCurrent: 'Zustände, die jede Liste und jede Aktion zeigt',
  note: 'Dieselben Bausteine in jedem Bereich',
  views: { states: 'Zustände', primitives: 'Bausteine' },
  viewLabel: 'Seite',
  names: {
    loading: 'Wird geladen',
    empty: 'Leer',
    filtered: 'Gefiltert',
    error: 'Fehler',
    stale: 'Veraltete Daten',
    disabled: 'Deaktiviert mit Grund',
    noAccess: 'Kein Zugriff',
    unknown: 'Ergebnis unbekannt',
  },
  loadingLabel: 'Sitzungen werden geladen…',
  emptyTitle: 'Noch keine Sitzungen in telescopes',
  emptyText: 'Starte eine hier oder im Terminal.',
  newSession: 'Neue Sitzung',
  filteredTitle: 'Keine Sitzung passt zu diesen Filtern',
  filters: 'Status: Fehlgeschlagen · KI-Werkzeug: Grok Build',
  staleRows: [
    { title: 'Stream-Import asynchron machen', time: '6 Min.' },
    { title: 'Kamerakalibrierung veröffentlichen', time: '2 Min.' },
    { title: 'OBS-Szenenwechsler reparieren', time: '1 Std.' },
  ],
  deploy: 'In Produktion bereitstellen',
  deployReason:
    'Der Plan hat sich nach der Freigabe geändert. Gib zuerst den neuen Plan frei.',
  reviewPlan: 'Plan prüfen',
  startSession: 'Sitzung starten',
  signedOut:
    'grok-b ist abgemeldet. Wähle ein anderes Konto oder melde dich erneut an.',
  signIn: 'Anmelden',
  noAccessTitle: 'Du kannst diesen Arbeitsbereich nicht öffnen',
  askOwner: 'Den Eigentümer um Zugriff bitten',
  startAgain: 'Erneut starten',
  sections: {
    buttons: 'Schaltflächen',
    fields: 'Felder',
    choices: 'Schalter und Segmentauswahl',
    tabs: 'Tabs',
    tags: 'Markierungen',
    glyphs: 'Statussymbole',
    marks: 'Monogramme',
    code: 'Befehlszeile',
  },
  buttons: {
    primary: 'Sitzung starten',
    secondary: 'Konto hinzufügen',
    quiet: 'Diagnose öffnen',
    danger: 'Konto entfernen',
    small: 'Erneut versuchen',
    large: 'Einrichtung starten',
  },
  fields: {
    host: 'Host',
    hostHint: 'Ein Name oder eine Adresse, die die Engine erreicht.',
    region: 'Region',
    regions: ['Europa (Madrid)', 'Nordamerika (Ohio)', 'Asien (Tokio)'],
    notify: 'Benachrichtigen, wenn eine Sitzung mich braucht',
    reduceMotion: 'Bewegung reduzieren',
  },
  segments: ['Liste', 'Tabelle', 'Board'],
  segmentReason: 'Die Board-Ansicht braucht einen Strom von Arbeitseinträgen.',
  tabs: [
    { label: 'Einträge', count: '24' },
    { label: 'Entscheidungen', count: '3' },
    { label: 'Änderungen' },
    { label: 'Kontext' },
  ],
  tabPanel:
    'Arbeitseinträge, die die Sitzungen dieses Arbeitsbereichs registriert haben.',
  tags: {
    ready: 'Bereit',
    signedOut: 'Abgemeldet',
    update: 'Update',
    cannotCheck: 'Nicht prüfbar',
    notInstalled: 'Nicht installiert',
    scope: 'in telescopes',
  },
  working: 'Läuft',
  marks: ['Claude Code', 'Codex', 'Grok Build', 'OpenCode', 'telescopes'],
  command: 'olivares agent tool detect',
}

const ja: DemoText = {
  crumbLabel: 'パンくずリスト',
  crumbUp: 'デザインシステム',
  crumbCurrent: 'すべてのリストと操作が示す状態',
  note: 'すべての領域で同じコンポーネント',
  views: { states: '状態', primitives: '基本部品' },
  viewLabel: 'ページ',
  names: {
    loading: '読み込み中',
    empty: '空',
    filtered: 'フィルター適用',
    error: 'エラー',
    stale: '古いデータ',
    disabled: '理由付きで無効',
    noAccess: 'アクセス不可',
    unknown: '結果不明',
  },
  loadingLabel: 'セッションを読み込み中…',
  emptyTitle: 'telescopes にはまだセッションがありません',
  emptyText: 'ここで開始するか、ターミナルから開始します。',
  newSession: '新しいセッション',
  filteredTitle: 'このフィルターに一致するセッションはありません',
  filters: '状態: 失敗 · AI ツール: Grok Build',
  staleRows: [
    { title: 'ストリーム取り込みを非同期にする', time: '6分' },
    { title: 'カメラ較正を公開する', time: '2分' },
    { title: 'OBS シーン切り替えを修正する', time: '1時間' },
  ],
  deploy: '本番環境にデプロイ',
  deployReason:
    '承認後にプランが変更されました。先に新しいプランを承認してください。',
  reviewPlan: 'プランを確認',
  startSession: 'セッションを開始',
  signedOut:
    'grok-b はサインアウトしています。別のアカウントを選ぶか、もう一度サインインしてください。',
  signIn: 'サインイン',
  noAccessTitle: 'このワークスペースを開く権限がありません',
  askOwner: 'オーナーにアクセスを依頼',
  startAgain: 'もう一度開始',
  sections: {
    buttons: 'ボタン',
    fields: 'フィールド',
    choices: 'スイッチとセグメント選択',
    tabs: 'タブ',
    tags: 'タグ',
    glyphs: '状態グリフ',
    marks: 'モノグラム',
    code: 'コマンドライン',
  },
  buttons: {
    primary: 'セッションを開始',
    secondary: 'アカウントを追加',
    quiet: '診断を開く',
    danger: 'アカウントを削除',
    small: '再試行',
    large: 'セットアップを開始',
  },
  fields: {
    host: 'ホスト',
    hostHint: 'エンジンが到達できる名前またはアドレス。',
    region: 'リージョン',
    regions: ['ヨーロッパ（マドリード）', '北米（オハイオ）', 'アジア（東京）'],
    notify: 'セッションが対応を必要とするときに通知',
    reduceMotion: '動きを減らす',
  },
  segments: ['リスト', 'テーブル', 'ボード'],
  segmentReason: 'ボード表示には作業項目のストリームが必要です。',
  tabs: [
    { label: '項目', count: '24' },
    { label: '決定', count: '3' },
    { label: '変更' },
    { label: 'コンテキスト' },
  ],
  tabPanel: 'このワークスペースのセッションが登録した作業項目。',
  tags: {
    ready: '準備完了',
    signedOut: 'サインアウト',
    update: '更新あり',
    cannotCheck: '確認できません',
    notInstalled: '未インストール',
    scope: 'telescopes 内',
  },
  working: '実行中',
  marks: ['Claude Code', 'Codex', 'Grok Build', 'OpenCode', 'telescopes'],
  command: 'olivares agent tool detect',
}

export const DEMO: Record<DemoLang, DemoText> = { en, de, ja }

export function demoFor(lang: string): DemoText {
  return lang === 'de' || lang === 'ja' ? DEMO[lang] : en
}
