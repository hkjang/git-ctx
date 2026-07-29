const $ = (s) => document.querySelector(s);
const $$ = (s) => [...document.querySelectorAll(s)];
// rows는 목록 응답을 항상 배열로 만듭니다. Go는 빈 슬라이스를 JSON null로
// 직렬화하므로, 보정하지 않으면 데이터가 없는 새 설치에서 화면이 깨집니다.
const rows = (value) => (Array.isArray(value) ? value : []);

// markEmptyTables는 머리글만 남은 표에 안내 문구를 넣습니다. 표를 그리는 코드가
// 여러 곳에 흩어져 있어도 빈 상태 처리가 한 곳에서 일관되게 적용됩니다.
function markEmptyTables(root = document) {
  root.querySelectorAll(".table-wrap table > tbody").forEach((body) => {
    if (body.querySelector("tr")) return;
    const columns = body.closest("table").querySelectorAll("thead th").length || 1;
    body.innerHTML = `<tr><td class="empty" colspan="${columns}">표시할 항목이 없습니다.</td></tr>`;
  });
}

/* ---------------------------------------------------------------------------
 * 공용 UI 유틸리티
 * 화면 로직 어디서나 같은 방식으로 알림·모달·테마를 다루기 위한 최소 도구입니다.
 * ------------------------------------------------------------------------- */

// toast는 화면 하단에 비차단 알림을 띄웁니다. action을 주면 버튼이 함께 붙습니다.
function toast(message, level = "info", action = null) {
  const stack = $("#toast-stack");
  if (!stack) return;
  const item = document.createElement("div");
  item.className = `toast ${level}`;
  const text = document.createElement("div");
  text.className = "toast-message";
  text.textContent = message;
  item.append(text);
  if (action) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "secondary";
    button.textContent = action.label;
    button.onclick = () => {
      action.run();
      item.remove();
    };
    item.append(button);
  }
  const close = document.createElement("button");
  close.type = "button";
  close.className = "secondary";
  close.textContent = "×";
  close.setAttribute("aria-label", "알림 닫기");
  close.onclick = () => item.remove();
  item.append(close);
  stack.append(item);
  setTimeout(() => item.remove(), level === "error" ? 15000 : 7000);
}

const THEME_KEY = "git_ctx_theme";
function applyTheme(theme) {
  if (theme === "light" || theme === "dark") {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem(THEME_KEY, theme);
  } else {
    delete document.documentElement.dataset.theme;
    localStorage.removeItem(THEME_KEY);
  }
}
function setupTheme() {
  applyTheme(localStorage.getItem(THEME_KEY) || "");
  $("#theme-toggle").onclick = () => {
    const current =
      document.documentElement.dataset.theme ||
      (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
    applyTheme(current === "dark" ? "light" : "dark");
  };
}

const categories = [
  "keycloak",
  "bitbucket",
  "gitlab",
  "confluence",
  "jira",
  "mcp",
  "search",
  "model",
  "opensearch",
  "vector",
  "index",
  "security",
  "vault",
  "notifications",
  "logging",
  "observability",
  "backup",
  "retention",
  "operations",
  "ui",
];
const integrationSettingFields = {
  keycloak: [
    ["baseUrl", "Keycloak Base URL", "url", ""],
    ["realm", "Realm", "text", ""],
    ["clientId", "Client ID", "text", "git-ctx"],
    ["clientSecret", "Client Secret", "password", ""],
  ],
  bitbucket: [
    ["baseUrl", "Bitbucket Base URL", "url", ""],
    ["apiPrefix", "REST API Prefix", "text", "/rest/api/1.0"],
    ["searchApiPath", "Code Search API Path", "text", "/rest/search/latest/search"],
    ["pat", "Personal Access Token", "password", ""],
    ["username", "Username (PAT 미사용 시)", "text", ""],
    ["password", "Password", "password", ""],
    ["searchTestQuery", "Code Search 검증 질의", "text", "README"],
    ["autoRegisterWebhook", "저장소 등록 시 Webhook 자동 등록", "boolean", true],
    ["webhookSecret", "Webhook Secret", "password", ""],
    ["tlsVerify", "TLS 인증서 검증 사용", "boolean", true],
    ["caCertificate", "사내 CA PEM", "textarea", ""],
    ["proxyUrl", "Proxy URL", "url", ""],
    ["timeoutSeconds", "Timeout(초)", "number", 30],
  ],
  gitlab: [
    ["baseUrl", "GitLab Base URL", "url", ""],
    ["token", "Access Token", "password", ""],
    ["searchTestQuery", "Code Search 검증 질의", "text", "README"],
    ["autoRegisterWebhook", "저장소 등록 시 Webhook 자동 등록", "boolean", true],
    ["webhookSecret", "Webhook Secret", "password", ""],
    ["tlsVerify", "TLS 인증서 검증 사용", "boolean", true],
    ["caCertificate", "사내 CA PEM", "textarea", ""],
    ["proxyUrl", "Proxy URL", "url", ""],
    ["timeoutSeconds", "Timeout(초)", "number", 30],
  ],
  confluence: [
    ["baseUrl", "Confluence Base URL", "url", ""],
    ["authType", "인증 방식", "select:bearer|basic", "bearer"],
    ["token", "Personal Access Token", "password", ""],
    ["username", "Basic Auth Username", "text", ""],
    ["password", "Basic Auth Password", "password", ""],
    ["allowedPrincipals", "허용 사용자·그룹 (쉼표 구분)", "array", ""],
    ["timeoutSeconds", "Timeout(초)", "number", 30],
  ],
  jira: [
    ["baseUrl", "Jira Base URL", "url", ""],
    ["authType", "인증 방식", "select:bearer|basic", "bearer"],
    ["token", "Personal Access Token", "password", ""],
    ["username", "Basic Auth Username", "text", ""],
    ["password", "Basic Auth Password", "password", ""],
    ["allowedPrincipals", "허용 사용자·그룹 (쉼표 구분)", "array", ""],
    ["timeoutSeconds", "Timeout(초)", "number", 30],
  ],
  mcp: [
    ["strictCompatibility", "Context7 Strict Compatibility (2개 도구만 노출)", "boolean", false],
    ["allowedOrigins", "허용 Origin (쉼표 구분)", "array", ""],
    ["maxRequestBytes", "최대 요청 크기(Byte)", "number", 1048576],
  ],
  search: [
    ["keywordWeight", "키워드 검색 가중치", "number", 1],
    ["vectorWeight", "벡터 검색 가중치", "number", 0.35],
    ["candidateLimit", "초기 후보 수", "number", 5000],
    ["finalK", "최종 문서 수", "number", 8],
    ["rerankLimit", "재순위화 후보 수", "number", 30],
  ],
  index: [
    ["pollingMinutes", "무결성 Polling 주기(분)", "number", 30],
    ["freshnessSloMinutes", "검색 최신성 SLO(분)", "number", 60],
  ],
  security: [
    ["trustedProxyCidrs", "신뢰 Proxy CIDR (쉼표 구분)", "array", ""],
  ],
  model: [
    [
      "provider",
      "Embedding Provider",
      "select:local|openai-compatible",
      "local",
    ],
    ["baseUrl", "OpenAI Compatible API URL", "url", ""],
    ["apiKey", "Embedding API Key", "password", ""],
    ["model", "Embedding Model", "text", ""],
    ["timeoutSeconds", "Embedding Timeout(초)", "number", 30],
    ["rerankerEnabled", "Reranker 사용", "boolean", false],
    [
      "rerankerProvider",
      "Reranker Provider",
      "select:openai-compatible",
      "openai-compatible",
    ],
    ["rerankerBaseUrl", "Reranker API URL", "url", ""],
    ["rerankerApiKey", "Reranker API Key", "password", ""],
    ["rerankerModel", "Reranker Model", "text", ""],
    ["rerankerTimeoutSeconds", "Reranker Timeout(초)", "number", 15],
    ["tlsVerify", "TLS 인증서 검증 사용", "boolean", true],
    ["caCertificate", "사내 CA PEM", "textarea", ""],
    ["proxyUrl", "Proxy URL", "url", ""],
  ],
  vector: [
    ["provider", "벡터 DB", "select:none|pgvector|milvus", "none"],
    ["dsn", "pgvector PostgreSQL DSN (비우면 플랫폼 DB)", "password", ""],
    ["baseUrl", "Milvus Base URL", "url", ""],
    ["collection", "컬렉션·테이블 이름", "text", "git_ctx_chunk_vectors"],
    ["database", "Milvus Database", "text", "default"],
    ["token", "Milvus Token", "password", ""],
    ["username", "Milvus Username", "text", ""],
    ["password", "Milvus Password", "password", ""],
    ["dimensions", "벡터 차원 (0=색인에서 자동 감지)", "number", 0],
    ["tlsVerify", "TLS 인증서 검증 사용", "boolean", true],
    ["caCertificate", "사내 CA PEM", "textarea", ""],
    ["proxyUrl", "Proxy URL", "url", ""],
    ["timeoutSeconds", "Timeout(초)", "number", 10],
  ],
  opensearch: [
    ["enabled", "OpenSearch 사용", "boolean", false],
    ["baseUrl", "OpenSearch URL", "url", ""],
    ["index", "청크 인덱스", "text", "git-ctx-chunks"],
    ["username", "Username", "text", ""],
    ["password", "Password", "password", ""],
    ["apiKey", "API Key (Basic 인증 대체)", "password", ""],
    ["tlsVerify", "TLS 인증서 검증 사용", "boolean", true],
    ["caCertificate", "사내 CA PEM", "textarea", ""],
    ["proxyUrl", "Proxy URL", "url", ""],
    ["timeoutSeconds", "Timeout(초)", "number", 30],
  ],
  vault: [
    ["enabled", "Vault KV v2 사용", "boolean", false],
    ["baseUrl", "Vault URL", "url", ""],
    ["token", "Vault Token", "password", ""],
    ["namespace", "Vault Enterprise Namespace", "text", ""],
    ["mount", "KV v2 Mount", "text", "secret"],
    ["prefix", "Secret 경로 Prefix", "text", "git-ctx"],
    ["tlsVerify", "TLS 인증서 검증 사용", "boolean", true],
    ["caCertificate", "사내 CA PEM", "textarea", ""],
    ["proxyUrl", "Proxy URL", "url", ""],
    ["timeoutSeconds", "Timeout(초)", "number", 30],
  ],
  observability: [
    ["enabled", "OpenTelemetry 사용", "boolean", false],
    ["otlpEndpoint", "OTLP HTTP Trace Endpoint", "url", ""],
    ["serviceName", "Telemetry Service Name", "text", "git-ctx"],
    ["sampleRatio", "Trace Sample Ratio (0~1)", "number", 1],
    ["headers", "추가 HTTP Header (JSON)", "json", {}],
    ["timeoutSeconds", "Timeout(초)", "number", 10],
    ["tlsVerify", "TLS 인증서 검증 사용", "boolean", true],
    ["allowInsecureLocalhost", "Localhost HTTP 허용", "boolean", false],
    ["caCertificate", "사내 CA PEM", "textarea", ""],
    ["proxyUrl", "Proxy URL", "url", ""],
  ],
  backup: [
    ["enabled", "예약 백업 사용", "boolean", false],
    ["directory", "백업 디렉터리", "text", "/var/lib/git-ctx/backups"],
    ["intervalHours", "백업 주기(시간)", "number", 24],
    ["retentionCount", "보존 개수", "number", 7],
    ["maxBytes", "백업 최대 크기(Byte)", "number", 536870912],
  ],
  logging: [
    ["level", "로그 레벨", "select:debug|info|warn|error", "info"],
  ],
  operations: [
    ["listenAddress", "서비스 수신 주소", "text", ":4747"],
    ["readHeaderTimeoutSeconds", "요청 헤더 Timeout(초)", "number", 10],
    ["readTimeoutSeconds", "요청 읽기 Timeout(초)", "number", 30],
    ["writeTimeoutSeconds", "응답 쓰기 Timeout(초)", "number", 60],
    ["idleTimeoutSeconds", "유휴 연결 Timeout(초)", "number", 90],
    ["shutdownTimeoutSeconds", "종료 대기 Timeout(초)", "number", 15],
    ["maintenanceMode", "점검 모드", "boolean", false],
    ["maintenanceMessage", "점검 안내", "textarea", ""],
  ],
  retention: [
    ["auditLogDays", "감사 로그 보존일 (0=영구)", "number", 365],
    ["mcpCallDays", "MCP 호출 기록 보존일 (0=영구)", "number", 90],
    ["notificationDays", "사용자 알림 보존일 (0=영구)", "number", 90],
    ["webhookEventDays", "Webhook 이벤트 보존일 (0=영구)", "number", 30],
    ["indexJobDays", "완료·실패 색인 작업 보존일 (0=영구)", "number", 30],
    ["securityEventDays", "색인 보안 이벤트 보존일 (0=영구)", "number", 180],
    ["settingVersionDays", "과거 설정 버전 보존일 (0=영구)", "number", 365],
  ],
  notifications: [
    ["inAppEnabled", "인앱 보안·만료 알림", "boolean", true],
    ["apiKeyExpiryWarningDays", "API 키 만료 사전 알림일 (0=해제)", "number", 7],
    ["rateLimitAlertsEnabled", "API 키 호출량 초과 알림", "boolean", true],
    ["externalEnabled", "외부 알림 전송", "boolean", false],
    ["webhookUrl", "운영 Webhook URL", "url", ""],
    ["webhookAuthorization", "Webhook Authorization", "password", ""],
    ["messengerWebhookUrl", "사내 메신저 Webhook URL", "url", ""],
    ["messengerAuthorization", "메신저 Authorization", "password", ""],
    ["smtpEnabled", "SMTP 이메일 전송", "boolean", false],
    ["smtpHost", "SMTP Host", "text", ""],
    ["smtpPort", "SMTP Port", "number", 587],
    ["smtpUsername", "SMTP Username", "text", ""],
    ["smtpPassword", "SMTP Password", "password", ""],
    ["smtpFrom", "발신 주소", "text", ""],
    ["smtpTlsMode", "SMTP TLS", "select:starttls|tls|none", "starttls"],
    ["testRecipient", "연결 시험 수신 이메일", "email", ""],
    ["timeoutSeconds", "전송 Timeout(초)", "number", 15],
    ["maxAttempts", "최대 재시도", "number", 5],
    ["tlsVerify", "외부 알림 TLS 인증서 검증", "boolean", true],
    ["caCertificate", "외부 알림 사내 CA PEM", "textarea", ""],
    ["proxyUrl", "Webhook Proxy URL", "url", ""],
  ],
  ui: [
    ["publicUrl", "서비스 Public URL", "url", "http://localhost:4747"],
    ["serviceName", "서비스 이름", "text", "git-ctx"],
    ["tagline", "상단 설명", "text", "사내 개발 지식 MCP"],
    ["logoUrl", "로고 URL", "text", "/logo.svg"],
    ["faviconUrl", "파비콘 URL", "text", "/favicon.svg"],
    ["notice", "서비스 공지", "textarea", ""],
  ],
};
const settingCategoryMeta = {
  keycloak: ["Keycloak SSO", "4개 항목으로 자동 구성하는 OIDC 로그인"],
  bitbucket: ["Bitbucket", "Bitbucket Server 6.9.1 연결과 Webhook"],
  gitlab: ["GitLab", "GitLab API v4 연결과 Webhook"],
  confluence: ["Confluence", "Space와 Page 문서 수집·검색, Fail-Closed Principal ACL"],
  jira: ["Jira", "Project와 Issue·Comment 지식 수집, Fail-Closed Principal ACL"],
  mcp: ["MCP", "Transport, Origin, 호출 제한"],
  search: ["검색", "키워드·벡터 가중치와 결과 수"],
  model: ["모델", "Embedding과 Reranker"],
  opensearch: ["OpenSearch", "BM25 projection과 인증"],
  vector: ["벡터 DB", "pgvector·Milvus 연동, 미설정 시 메타 DB 내장 벡터 사용"],
  index: ["색인", "Polling과 기본 색인 정책"],
  security: ["보안", "신뢰 프록시와 키 정책"],
  vault: ["Vault", "KV v2 Secret backend"],
  notifications: ["알림", "인앱·Webhook·사내 메신저·SMTP와 재시도 정책"],
  logging: ["로깅", "재기동 없이 적용하는 구조화 로그 레벨"],
  observability: ["관측성", "OpenTelemetry Export"],
  backup: ["백업", "주기·경로·보존"],
  retention: ["보존", "감사·호출·알림·색인 운영 데이터 수명주기"],
  operations: ["운영", "수신 주소·Timeout·동적 점검 모드"],
  ui: ["UI", "서비스명·로고·공지"],
};
const settingDefaults = (category) => {
  const defaults = Object.fromEntries(
    (integrationSettingFields[category] || []).map(([key, , , value]) => [
      key,
      value,
    ]),
  );
  return defaults;
};
let bootstrapInfo = { required: false, tokenFile: "", ssoConfigured: false };
const isAdminEntry = location.pathname === "/admin" || location.pathname.startsWith("/admin/");
const isRecoveryEntry =
  isAdminEntry && new URLSearchParams(location.search).get("recovery") === "1";
const api = async (url, options = {}) => {
  const bootstrapToken = sessionStorage.getItem("git_ctx_bootstrap_token");
  const response = await fetch(url, {
    ...options,
    headers: {
      "Content-Type": "application/json",
      ...(bootstrapToken ? { Authorization: `Bearer ${bootstrapToken}` } : {}),
      ...(options.headers || {}),
    },
  });
  if (response.status === 204) return null;
  const body = await response
    .json()
    .catch(() => ({ detail: "응답을 읽을 수 없습니다." }));
  if (!response.ok) {
    const error = new Error(body.detail || body.title || `HTTP ${response.status}`);
    error.status = response.status;
    error.code = body.code || "";
    throw error;
  }
  return body;
};
/* ---------------------------------------------------------------------------
 * 가이드 모달
 * 내용은 guides.js 의 순수 데이터이며, 아래 렌더러가 공통 서식을 만듭니다.
 * 새 가이드를 추가할 때 이 파일은 수정할 필요가 없습니다.
 * ------------------------------------------------------------------------- */
let lastAccessDiagnostics = null;

function guideSectionHTML(section) {
  const parts = [`<h4>${esc(section.title || "")}</h4>`];
  for (const paragraph of section.body || []) parts.push(`<p>${esc(paragraph)}</p>`);
  if (section.steps?.length) {
    parts.push(`<ol>${section.steps.map((step) => `<li>${esc(step)}</li>`).join("")}</ol>`);
  }
  if (section.table) {
    parts.push(
      `<div class="table-wrap"><table><thead><tr>${section.table.head.map((cell) => `<th>${esc(cell)}</th>`).join("")}</tr></thead><tbody>${section.table.rows
        .map((row) => `<tr>${row.map((cell) => `<td>${esc(cell)}</td>`).join("")}</tr>`)
        .join("")}</tbody></table></div>`,
    );
  }
  if (section.code) parts.push(`<pre>${esc(section.code)}</pre>`);
  if (section.notice) parts.push(`<div class="guide-notice">${esc(section.notice)}</div>`);
  return `<section class="guide-section">${parts.join("")}</section>`;
}

function accessDiagnosticsHTML(access) {
  if (!access) return '<div class="notice">권한 진단을 불러오는 중입니다…</div>';
  const chips = (values, ok) =>
    values?.length
      ? values.map((value) => `<span class="chip ${ok ? "ok" : ""}">${esc(value)}</span>`).join("")
      : '<span class="chip error">없음</span>';
  const blocked = (access.settings || []).filter((item) => !item.allowed);
  return `<div class="access-grid">
    <article><h4>계정</h4><div>${esc(access.username || "-")}</div><small>${esc(access.subject || "")}</small></article>
    <article><h4>플랫폼 역할</h4>${chips(access.roles, true)}<div class="field-help">${access.rolesManagedLocally ? "사용자 관리 화면에서 수동 관리 중입니다." : "로그인할 때 Keycloak 역할로 동기화됩니다."}</div></article>
    <article><h4>소스 ACL Principal</h4>${
      access.unrestrictedSearch
        ? '<span class="chip ok">관리자 — 전체 저장소 검색</span>' + chips(access.aclPrincipals, true)
        : chips(access.aclPrincipals, access.aclReady)
    }<div class="field-help">${
      access.unrestrictedSearch
        ? "platform-admin, source-admin, search-admin 역할은 저장소 ACL과 무관하게 검색합니다."
        : access.aclReady
          ? "저장소 ACL 검사에 사용됩니다."
          : "비어 있으면 모든 검색 결과가 차단됩니다."
    }</div></article>
    <article><h4>Keycloak 역할 매핑</h4>${access.keycloak?.configured ? chips(access.keycloak.roleMappings?.length ? access.keycloak.roleMappings : ["이름이 같은 역할만 자동 인식"], true) : '<span class="chip error">미설정</span>'}</article>
    <article><h4>수정할 수 없는 설정</h4>${blocked.length ? blocked.map((item) => `<span class="chip error">${esc(item.category)}</span>`).join("") : '<span class="chip ok">전체 수정 가능</span>'}</article>
  </div>`;
}

async function loadAccessDiagnostics() {
  try {
    lastAccessDiagnostics = await api("/api/v1/me/access");
  } catch {
    lastAccessDiagnostics = null;
  }
  const target = $("#account-access");
  if (target) target.innerHTML = accessDiagnosticsHTML(lastAccessDiagnostics);
  return lastAccessDiagnostics;
}

function openGuide(topic) {
  const guide = GitCtxGuides.get(topic);
  const dialog = $("#guide-dialog");
  if (!guide) {
    toast(`'${topic}' 가이드는 아직 준비되지 않았습니다.`, "error");
    return;
  }
  $("#guide-title").textContent = guide.title;
  const body = [
    `<div class="guide-summary">${esc(guide.summary || "")}</div>`,
    guide.audience ? `<p class="guide-audience">필요 역할: <code>${esc(guide.audience)}</code></p>` : "",
    ...(guide.sections || []).map(guideSectionHTML),
  ];
  if (guide.troubleshooting?.length) {
    body.push(
      `<section class="guide-section"><h4>자주 겪는 문제</h4>${guide.troubleshooting
        .map(
          (item) =>
            `<div class="guide-trouble"><strong>${esc(item.symptom)}</strong><div class="field-help">원인: ${esc(item.cause || "")}</div><div>조치: ${esc(item.fix)}</div></div>`,
        )
        .join("")}</section>`,
    );
  }
  if (guide.diagnostics) {
    body.push(`<section class="guide-section" id="guide-diagnostics"><h4>내 권한 진단</h4>${accessDiagnosticsHTML(lastAccessDiagnostics)}</section>`);
  }
  $("#guide-body").innerHTML = body.join("");
  $("#guide-diagnose").hidden = !guide.diagnostics;
  dialog.showModal();
  if (guide.diagnostics) {
    loadAccessDiagnostics().then((access) => {
      const target = document.querySelector("#guide-diagnostics");
      if (target) target.innerHTML = `<h4>내 권한 진단</h4>${accessDiagnosticsHTML(access)}`;
    });
  }
}

// openGuideIndex는 전체 가이드 목록을 보여 줍니다. guides.js에 항목을 추가하면
// 여기에도 자동으로 나타납니다.
function openGuideIndex() {
  $("#guide-title").textContent = "설정·운영 가이드";
  $("#guide-body").innerHTML =
    `<div class="guide-summary">필요한 주제를 선택하세요. 각 설정 탭에서도 [이 탭 가이드] 버튼으로 같은 내용을 열 수 있습니다.</div>
     <section class="guide-section"><div class="guide-index">${GitCtxGuides.topics()
       .map((topic) => {
         const guide = GitCtxGuides.get(topic);
         return `<button type="button" data-guide-topic="${esc(topic)}"><span>${esc(guide.title)}</span><small>${esc((guide.summary || "").slice(0, 70))}…</small></button>`;
       })
       .join("")}</div></section>`;
  $("#guide-diagnose").hidden = true;
  $$("[data-guide-topic]").forEach((button) => {
    button.onclick = () => openGuide(button.dataset.guideTopic);
  });
  if (!$("#guide-dialog").open) $("#guide-dialog").showModal();
}

function setupGuides() {
  $("#guide-close").onclick = () => $("#guide-dialog").close();
  $("#guide-dismiss").onclick = () => $("#guide-dialog").close();
  $("#guide-diagnose").onclick = () =>
    loadAccessDiagnostics().then((access) => {
      const target = document.querySelector("#guide-diagnostics");
      if (target) target.innerHTML = `<h4>내 권한 진단</h4>${accessDiagnosticsHTML(access)}`;
      toast("권한 정보를 다시 확인했습니다.", "ok");
    });
  $("#help-button").onclick = openGuideIndex;
  document.addEventListener("click", (event) => {
    const trigger = event.target.closest("[data-guide]");
    if (trigger) openGuide(trigger.dataset.guide);
  });
}

// reportError는 오류를 사용자 언어로 보여주고, 권한 오류에는 가이드를 바로 연결합니다.
function reportError(error, context = "") {
  const prefix = context ? `${context}: ` : "";
  if (error?.status === 403) {
    toast(prefix + error.message, "error", { label: "ACL 가이드 열기", run: () => openGuide("acl") });
    return;
  }
  if (error?.status === 401) {
    toast(prefix + "인증이 만료되었습니다. 다시 로그인하세요.", "error");
    return;
  }
  toast(prefix + (error?.message || "알 수 없는 오류"), "error");
}

$("#endpoint").textContent = location.origin;
async function loadBranding() {
  try {
    const config = await api("/api/v1/public/config");
    document.title = config.serviceName || "git-ctx";
    $(".header-logo").src = config.logoUrl || "/logo.svg";
    $(".header-logo").alt = config.serviceName || "git-ctx";
    $("#favicon").href = config.faviconUrl || "/favicon.svg";
    $("#brand-tagline").textContent = config.tagline || "사내 개발 지식 MCP";
    const versionText = `v${config.version || "unknown"}`;
    $("#service-version").textContent = versionText;
    $("#login-version").textContent = `서비스 버전 ${versionText}`;
    bootstrapInfo = {
      required: Boolean(config.bootstrapRequired),
      tokenFile: config.bootstrapTokenFile || "backups/bootstrap-admin.token",
      ssoConfigured: Boolean(config.ssoConfigured),
    };
    $("#login").disabled = !bootstrapInfo.ssoConfigured;
    $("#login").title = bootstrapInfo.ssoConfigured
      ? "Keycloak SSO로 로그인"
      : "관리자가 Keycloak SSO를 먼저 설정해야 합니다.";
    // The one-time bootstrap credential is an administrative recovery path.
    // Never expose it on the regular user sign-in page.
    $("#bootstrap-login").hidden = !isAdminEntry || !bootstrapInfo.required;
    $("#recovery-login").hidden = !isRecoveryEntry;
    $("#entry-description").textContent = isAdminEntry
      ? "관리 설정과 사용자·역할을 운영합니다. 일반 사용자는 서비스 홈에서 Keycloak SSO로 로그인하세요."
      : "Keycloak SSO로 로그인하여 접근 가능한 Bitbucket·GitLab 문서를 검색하고 MCP 키를 관리하세요.";
    $("#admin-entry-link").hidden = isAdminEntry;
    if (config.notice) {
      $("#service-notice").textContent = config.notice;
      $("#service-notice").hidden = false;
    }
  } catch (e) {
    console.warn("브랜드 설정을 불러오지 못했습니다.", e);
  }
}
async function loadPublicStatus() {
  try {
    const response = await fetch("/api/v1/public/status", { cache: "no-store" });
    const status = await response.json();
    $("#database-status").textContent =
      `메타 DB ${status.status === "connected" ? "연결됨" : "연결 실패"} · ${status.driver || "unknown"}${status.recoveryMode ? " 복구 모드" : ""} · ${Number(status.latencyMs || 0).toFixed(1)}ms`;
    $("#database-status").classList.toggle("ok", response.ok && !status.recoveryMode);
    $("#database-status").classList.toggle("error", !response.ok || status.recoveryMode);
  } catch {
    $("#database-status").textContent = "메타 DB 상태 API에 연결할 수 없습니다.";
    $("#database-status").classList.add("error");
  }
}
$("#login").onclick = () => {
  if (bootstrapInfo.ssoConfigured) {
    const returnTo = isAdminEntry ? "/admin" : "/";
    location.href = `/auth/login?return_to=${encodeURIComponent(returnTo)}`;
  }
};
$("#bootstrap-login").onclick = () => {
  const token = prompt(
    `서버의 ${bootstrapInfo.tokenFile} 파일에 생성된 일회용 토큰을 입력하세요.`,
  );
  if (!token) return;
  api("/api/v1/bootstrap/login", {
    method: "POST",
    body: JSON.stringify({ token: token.trim() }),
  })
    .then(() => {
      sessionStorage.removeItem("git_ctx_bootstrap_token");
      boot();
    })
    .catch((e) => {
      $("#status").textContent = e.message;
      $("#status").classList.remove("ok");
    });
};
$("#recovery-login").onclick = () => {
  const token = prompt(
    "서버에서 `git-ctx recovery-token` 명령으로 생성한 짧은 만료시간의 일회용 복구 토큰을 입력하세요.",
  );
  if (!token) return;
  api("/api/v1/recovery/login", {
    method: "POST",
    body: JSON.stringify({ token: token.trim() }),
  })
    .then(() => {
      history.replaceState(null, "", "/admin");
      boot();
    })
    .catch((e) => {
      $("#status").textContent = e.message;
      $("#status").classList.remove("ok");
    });
};
const performLogout = async () => {
  const result = await api("/auth/logout", { method: "POST" });
  if (result?.logoutUrl) {
    location.href = result.logoutUrl;
  } else {
    location.reload();
  }
};
$("#logout").onclick = performLogout;
$("#profile-logout").onclick = performLogout;
async function boot() {
  try {
    const me = await api("/api/v1/me");
    $("#status").textContent = "Keycloak 인증이 완료되었습니다.";
    $("#status").classList.add("ok");
    // 로그인 후에는 소개 문구 대신 작업 화면이 먼저 보이도록 환영 패널을 접습니다.
    document.querySelector(".welcome-panel").classList.add("compact");
    $("#login").hidden = true;
    $("#bootstrap-login").hidden = true;
    $("#recovery-login").hidden = true;
    $("#logout").hidden = true;
    $("#profile-menu").hidden = false;
    $("#quick-nav-button").hidden = false;
    $("#profile-name").textContent = me.Username || "내 프로필";
    $("#profile-avatar").textContent = (me.Username || "U").slice(0, 1).toUpperCase();
    // 운영 역할은 소스 계정 매핑 없이도 전체 저장소를 검색하므로 Fail Closed로
    // 표시하지 않습니다.
    const unrestricted = (me.Roles || []).some((role) =>
      ["platform-admin", "source-admin", "search-admin"].includes(role),
    );
    $("#identity").textContent =
      `${me.Username} · 역할: ${(me.Roles || []).join(", ")} · ACL: ${me.ACLPrincipal || (unrestricted ? "관리자 역할로 전체 저장소 검색" : "매핑되지 않음(Fail Closed)")}`;
    $("#profile-version").textContent = `서비스 버전 v${me.Version || "unknown"}`;
    const roles = new Set(me.Roles || []);
    configureMCPKeyScopes(roles);
    const capabilities = GitCtxRoles.capabilitiesFor(me.Roles || []);
    const hasAdmin = Object.values(capabilities).some(Boolean);
    if (hasAdmin) {
      setupAdmin(roles, capabilities);
      setupOps(capabilities);
    }
    setupWorkspaceNavigation(hasAdmin);
    setupProfileMenu();
    setupQuickNavigation(capabilities);
    applyInitialNavigation(hasAdmin);
    loadKeys();
    loadActivity();
    setupKnowledgeSearch();
    loadAccessDiagnostics();
  } catch (e) {
    $("#status").textContent =
      "Keycloak으로 로그인하면 개인 MCP 키와 관리자 기능을 사용할 수 있습니다.";
    if (isAdminEntry && !bootstrapInfo.required && !isRecoveryEntry) {
      location.href = `/auth/login?return_to=${encodeURIComponent("/admin")}`;
    }
  }
}
function setupKnowledgeSearch() {
  const output = $("#knowledge-result");
  $("#search-code-form").onsubmit = async (event) => {
    event.preventDefault();
    const data = Object.fromEntries(new FormData(event.currentTarget));
    output.textContent = "검색 중…";
    try {
      const result = await api("/api/v1/tools/search-code/test", {
        method: "POST",
        body: JSON.stringify({ ...data, limit: 20 }),
      });
      // 원시 JSON 대신 사람이 읽는 요약을 먼저 보여 주고, 왜 결과가 비었는지
      // 알 수 있도록 서버 진단 메시지를 항상 함께 출력합니다.
      const repositories = result.Repositories || [];
      const hits = result.Hits || [];
      const lines = [
        `질의: ${result.Query}`,
        `저장소 ${repositories.length}건 · 코드 ${hits.length}건`,
        "",
      ];
      if (repositories.length) {
        lines.push("── 저장소 ──");
        for (const item of repositories) {
          lines.push(`${item.LibraryID}  (${item.SourceType}, ${item.DefaultBranch || "-"})  ${item.Name || ""}`);
        }
        lines.push("");
      }
      if (hits.length) {
        lines.push("── 코드 ──");
        for (const hit of hits) {
          lines.push(`${hit.LibraryID} · ${hit.Path}#L${hit.LineStart}-L${hit.LineEnd} @${hit.Ref || "-"}`);
          lines.push(String(hit.Snippet || "").split("\n").slice(0, 6).join("\n"));
          lines.push("");
        }
      }
      if (result.Warning) lines.push(`⚠ ${result.Warning}`);
      for (const diagnostic of result.Diagnostics || []) lines.push(`· ${diagnostic}`);
      output.textContent = lines.join("\n");
    } catch (error) {
      output.textContent = error.message;
      reportError(error, "코드 검색");
    }
  };
  $("#semantic-form").onsubmit = async (event) => {
    event.preventDefault();
    const data = Object.fromEntries(new FormData(event.currentTarget));
    output.textContent = "의미 기반으로 검색하는 중…";
    try {
      const result = await api("/api/v1/tools/semantic/test", { method: "POST", body: JSON.stringify({ ...data, limit: 10 }) });
      const hits = rows(result.Hits);
      const lines = [`질의: ${result.Query} · ${hits.length}건 · 검색 방식: ${result.Mode}`, ""];
      for (const hit of hits) {
        lines.push(`[${hit.Score.toFixed(2)}] ${hit.LibraryID} · ${hit.FilePath}#L${hit.LineStart}-L${hit.LineEnd}`);
        lines.push(String(hit.Content || "").split("\n").slice(0, 6).join("\n"));
        lines.push("");
      }
      if (!hits.length) lines.push("의미가 충분히 가까운 결과가 없습니다. 표현을 바꾸거나 search-code로 정확한 용어를 찾아보세요.");
      for (const diagnostic of rows(result.Diagnostics)) lines.push(`· ${diagnostic}`);
      output.textContent = lines.join("\n");
    } catch (error) {
      output.textContent = error.message;
      reportError(error, "의미 검색");
    }
  };
  $("#find-file-form").onsubmit = async (event) => {
    event.preventDefault();
    const data = Object.fromEntries(new FormData(event.currentTarget));
    output.textContent = "파일을 찾는 중…";
    try {
      const result = await api("/api/v1/tools/find-file/test", {
        method: "POST",
        body: JSON.stringify({ ...data, limit: 100 }),
      });
      const files = rows(result.Files);
      const lines = [`패턴: ${result.Pattern}`, `${files.length}건`, ""];
      let current = "";
      for (const file of files) {
        if (file.LibraryID !== current) {
          current = file.LibraryID;
          lines.push(`── ${file.LibraryID} (${file.SourceType}, ${file.Ref}) ──`);
        }
        lines.push(`  ${file.Path}  ${file.SizeBytes ? `${file.SizeBytes}B` : ""} ${file.ContentIndexed ? "[본문 색인됨]" : "[본문 미색인]"}`);
      }
      if (!files.length) lines.push("일치하는 파일이 없습니다. *.확장자 또는 경로 조각으로 다시 시도해 보세요.");
      for (const diagnostic of rows(result.Diagnostics)) lines.push(`· ${diagnostic}`);
      output.textContent = lines.join("\n");
    } catch (error) {
      output.textContent = error.message;
      reportError(error, "파일 검색");
    }
  };
  $("#read-file-form").onsubmit = async (event) => {
    event.preventDefault();
    const data = Object.fromEntries(new FormData(event.currentTarget));
    output.textContent = "파일을 읽는 중…";
    try {
      const file = await api("/api/v1/tools/read-file/test", {
        method: "POST",
        body: JSON.stringify({
          ...data,
          startLine: Number(data.startLine) || 0,
          endLine: Number(data.endLine) || 0,
        }),
      });
      const header = `${file.Path} · ${file.LibraryID} · ref ${file.Ref} · ${file.StartLine}-${file.EndLine}/${file.TotalLines}줄 · ${file.Origin}`;
      output.textContent = [header, "", file.Content, "", ...rows(file.Diagnostics).map((item) => `· ${item}`)].join("\n");
    } catch (error) {
      output.textContent = error.message;
      reportError(error, "파일 열기");
    }
  };
  $("#directory-form").onsubmit = async (event) => {
    event.preventDefault();
    const data = Object.fromEntries(new FormData(event.currentTarget));
    output.textContent = "목록을 불러오는 중…";
    try {
      const listing = await api("/api/v1/tools/directory/test", { method: "POST", body: JSON.stringify(data) });
      const lines = [`${listing.Path || "(루트)"} · ${listing.LibraryID} · ref ${listing.Ref}`, ""];
      for (const entry of rows(listing.Entries)) {
        lines.push(entry.Directory ? `  ${entry.Name}/  (파일 ${entry.Files}개)` : `  ${entry.Name}${entry.ContentIndexed ? "" : "  [본문 미색인]"}`);
      }
      output.textContent = lines.join("\n");
    } catch (error) {
      output.textContent = error.message;
      reportError(error, "디렉터리 조회");
    }
  };
  $("#file-history-form").onsubmit = async (event) => {
    event.preventDefault();
    const data = Object.fromEntries(new FormData(event.currentTarget));
    output.textContent = "이력을 불러오는 중…";
    try {
      const history = await api("/api/v1/tools/file-history/test", { method: "POST", body: JSON.stringify({ ...data, limit: 30 }) });
      const lines = [`${history.Path} · ${history.LibraryID} · ref ${history.Ref}`, ""];
      for (const commit of rows(history.Commits)) {
        lines.push(`${commit.DisplayID}  ${date(commit.AuthoredAt)}  ${commit.Author}`);
        lines.push(`  ${String(commit.Message || "").split("\n")[0]}`);
      }
      for (const diagnostic of rows(history.Diagnostics)) lines.push(`· ${diagnostic}`);
      output.textContent = lines.join("\n");
    } catch (error) {
      output.textContent = error.message;
      reportError(error, "파일 이력");
    }
  };
  $("#merge-request-form").onsubmit = async (event) => {
    event.preventDefault();
    const data = Object.fromEntries(new FormData(event.currentTarget));
    output.textContent = "MR·PR을 검색하는 중…";
    try {
      const result = await api("/api/v1/tools/merge-requests/test", { method: "POST", body: JSON.stringify({ ...data, limit: 20 }) });
      const lines = [`검색어: ${result.Query || "(전체)"} · ${rows(result.Requests).length}건`, ""];
      for (const item of rows(result.Requests)) {
        lines.push(`${item.ID} [${item.State}] ${item.Title}`);
        lines.push(`  ${item.LibraryID} · ${item.Author} · ${item.SourceRef} → ${item.TargetRef} · ${date(item.UpdatedAt)}`);
        const summary = String(item.Description || "").split("\n").slice(0, 3).join(" ").trim();
        if (summary) lines.push(`  ${summary}`);
        if (item.URL) lines.push(`  ${item.URL}`);
        lines.push("");
      }
      for (const diagnostic of rows(result.Diagnostics)) lines.push(`· ${diagnostic}`);
      output.textContent = lines.join("\n");
    } catch (error) {
      output.textContent = error.message;
      reportError(error, "MR·PR 검색");
    }
  };
  $("#dependents-form").onsubmit = async (event) => {
    event.preventDefault();
    const data = Object.fromEntries(new FormData(event.currentTarget));
    output.textContent = "사용처를 찾는 중…";
    try {
      const result = await api("/api/v1/tools/dependents/test", { method: "POST", body: JSON.stringify({ ...data, limit: 200 }) });
      const items = rows(result.Dependents);
      const lines = [`대상: ${result.Target} · ${items.length}건 · 저장소 ${rows(result.Repositories).length}개`, ""];
      let current = "";
      for (const item of items) {
        if (item.LibraryID !== current) {
          current = item.LibraryID;
          lines.push(`── ${item.LibraryID} (${item.Ref}) ──`);
        }
        lines.push(`  ${item.FilePath}:${item.LineNumber}  ${item.FromSymbol || "(파일)"} ${item.Kind} → ${item.Target}`);
      }
      for (const diagnostic of rows(result.Diagnostics)) lines.push(`· ${diagnostic}`);
      output.textContent = lines.join("\n");
    } catch (error) {
      output.textContent = error.message;
      reportError(error, "사용처 역추적");
    }
  };
  $("#repository-map-form").onsubmit = async (event) => {
    event.preventDefault();
    const data = Object.fromEntries(new FormData(event.currentTarget));
    try {
      output.textContent = JSON.stringify(await api("/api/v1/tools/repository-map/test", { method: "POST", body: JSON.stringify(data) }), null, 2);
    } catch (error) {
      output.textContent = error.message;
    }
  };
  $("#symbol-search-form").onsubmit = async (event) => {
    event.preventDefault();
    const data = Object.fromEntries(new FormData(event.currentTarget));
    try {
      output.textContent = JSON.stringify(await api("/api/v1/tools/symbols/test", { method: "POST", body: JSON.stringify({ ...data, limit: 30 }) }), null, 2);
    } catch (error) {
      output.textContent = error.message;
    }
  };
  $("#dependency-form").onsubmit = async (event) => {
    event.preventDefault();
    const data = Object.fromEntries(new FormData(event.currentTarget));
    try {
      output.textContent = JSON.stringify(await api("/api/v1/tools/dependencies/test", { method: "POST", body: JSON.stringify({ ...data, limit: 100 }) }), null, 2);
    } catch (error) {
      output.textContent = error.message;
    }
  };
  $("#change-impact-form").onsubmit = async (event) => {
    event.preventDefault();
    const data = Object.fromEntries(new FormData(event.currentTarget));
    try {
      output.textContent = JSON.stringify(await api("/api/v1/tools/change-impact/test", { method: "POST", body: JSON.stringify({ ...data, limit: 100 }) }), null, 2);
    } catch (error) {
      output.textContent = error.message;
    }
  };
  $("#context-pack-form").onsubmit = async (event) => {
    event.preventDefault();
    const data = Object.fromEntries(new FormData(event.currentTarget));
    try {
      output.textContent = JSON.stringify(await api("/api/v1/tools/context-pack/test", { method: "POST", body: JSON.stringify(data) }), null, 2);
    } catch (error) {
      output.textContent = error.message;
    }
  };
  $("#runbook-form").onsubmit = async (event) => {
    event.preventDefault();
    const data = Object.fromEntries(new FormData(event.currentTarget));
    try {
      output.textContent = JSON.stringify(await api("/api/v1/tools/runbooks/test", { method: "POST", body: JSON.stringify({ ...data, limit: 20 }) }), null, 2);
    } catch (error) {
      output.textContent = error.message;
    }
  };
  $("#context-export-form").onsubmit = async (event) => {
    event.preventDefault();
    const data = Object.fromEntries(new FormData(event.currentTarget));
    data.libraryIds = data.libraryIds.split(",").map((value) => value.trim()).filter(Boolean);
    try {
      output.textContent = (await api("/api/v1/tools/export/test", { method: "POST", body: JSON.stringify(data) })).content;
    } catch (error) {
      output.textContent = error.message;
    }
  };
}
let currentRoleSet = new Set();
let activeScopeEditor = null;
const userMCPScopes = [
  "resolve-library-id", "query-docs", "search-repositories", "search-source",
  "search-code", "find-file", "read-file", "get-file-history", "list-directory", "search-merge-requests", "find-dependents", "search-semantic", "get-repository-map", "find-symbol", "get-symbol-context",
  "trace-dependencies", "compare-refs", "get-change-impact", "get-context-pack",
  "find-runbook", "export-context", "explain-search-result",
];
const managementMCPScopes = ["get-platform-status", "list-index-jobs", "reindex-repository"];

function grantableMCPScopes() {
  const platform = currentRoleSet.has("platform-admin");
  const source = platform || currentRoleSet.has("source-admin");
  const operator = source || currentRoleSet.has("readonly-operator");
  const anyAdmin = platform || [...currentRoleSet].some((role) =>
    ["source-admin", "mcp-admin", "search-admin", "security-admin", "auditor", "readonly-operator"].includes(role),
  );
  return [
    ...userMCPScopes,
    ...(anyAdmin ? ["get-platform-status"] : []),
    ...(operator ? ["list-index-jobs"] : []),
    ...(source ? ["reindex-repository"] : []),
  ];
}

function openKeyScopeEditor(key, administrator = false, onSaved = loadKeys) {
  const dialog = $("#key-scope-dialog");
  const allowed = new Set(grantableMCPScopes());
  const selected = new Set(key.scopes || []);
  $("#key-scope-description").textContent =
    `${administrator ? `${key.username} / ` : ""}${key.name} (${key.prefix})`;
  $("#key-scope-options").innerHTML = [...userMCPScopes, ...managementMCPScopes]
    .map((scope) => `<label><input type="checkbox" name="scope" value="${scope}" ${selected.has(scope) ? "checked" : ""} ${allowed.has(scope) ? "" : "disabled"} /> ${scope}</label>`)
    .join("");
  activeScopeEditor = { key, administrator, onSaved };
  dialog.showModal();
}

$("#key-scope-close").onclick = () => $("#key-scope-dialog").close();
$("#key-scope-cancel").onclick = () => $("#key-scope-dialog").close();
$("#key-scope-form").onsubmit = async (event) => {
  event.preventDefault();
  if (!activeScopeEditor) return;
  const scopes = new FormData(event.currentTarget).getAll("scope");
  if (!scopes.length) {
    alert("하나 이상의 도구 Scope를 선택해야 합니다.");
    return;
  }
  const { key, administrator, onSaved } = activeScopeEditor;
  const path = administrator
    ? `/api/v1/admin/api-keys/${encodeURIComponent(key.id)}/scopes`
    : `/api/v1/me/api-keys/${encodeURIComponent(key.id)}/scopes`;
  try {
    await api(path, { method: "PUT", body: JSON.stringify({ scopes }) });
    $("#key-scope-dialog").close();
    activeScopeEditor = null;
    await onSaved();
  } catch (error) {
    $("#key-scope-description").textContent = error.message;
  }
};

function configureMCPKeyScopes(roles) {
  currentRoleSet = new Set(roles);
  const platform = roles.has("platform-admin");
  const source = platform || roles.has("source-admin");
  const operator = source || roles.has("readonly-operator");
  const anyAdmin =
    platform ||
    [...roles].some((role) =>
      ["source-admin", "mcp-admin", "search-admin", "security-admin", "auditor", "readonly-operator"].includes(role),
    );
  const fieldset = $("#admin-key-scopes");
  fieldset.hidden = !anyAdmin;
  const access = { "get-platform-status": anyAdmin, "list-index-jobs": operator, "reindex-repository": source };
  fieldset.querySelectorAll('[name="scope"]').forEach((input) => {
    input.disabled = !access[input.value];
    if (input.disabled) input.checked = false;
  });
}
let openWorkspaceView = () => {};
let openPersonalView = () => {};
let openAdminPanel = () => {};
let openSettingCategory = () => {};

/* ---------------------------------------------------------------------------
 * 화면 위치 유지
 * 현재 작업 영역·패널·설정 탭을 주소의 fragment에 기록해, 새로고침하거나 링크를
 * 공유해도 같은 화면이 그대로 열리게 합니다.
 * ------------------------------------------------------------------------- */
const viewState = { workspace: "personal", personal: "account", panel: "", category: "" };
let restoringView = false;

function rememberView(patch) {
  Object.assign(viewState, patch);
  if (restoringView) return;
  const hash =
    viewState.workspace === "admin"
      ? `#/admin/${viewState.panel || "settings-admin"}${viewState.panel === "settings-admin" && viewState.category ? `/${viewState.category}` : ""}`
      : `#/personal/${viewState.personal || "account"}`;
  if (location.hash !== hash) history.replaceState(null, "", location.pathname + location.search + hash);
}

// 초기 화면을 그리는 동안 rememberView가 주소를 덮어쓰므로, 진입 시점의 값을
// 한 번만 캡처해 두고 복원에 사용합니다.
const initialViewHash = location.hash;

function parseViewHash() {
  const parts = initialViewHash.replace(/^#\/?/, "").split("/").filter(Boolean);
  // 이전 버전이 사용하던 #admin/keycloak 형식도 계속 인식합니다.
  if (parts[0] === "admin" && parts[1] === "keycloak" && parts.length === 2) {
    return { workspace: "admin", panel: "settings-admin", category: "keycloak" };
  }
  if (parts[0] === "admin") return { workspace: "admin", panel: parts[1] || "", category: parts[2] || "" };
  if (parts[0] === "personal") return { workspace: "personal", personal: parts[1] || "" };
  return null;
}
function setupWorkspaceNavigation(hasAdmin) {
  $("#app-sidebar").hidden = false;
  $("#sidebar-toggle").hidden = false;
  $("#sidebar-toggle").onclick = () => {
    const sidebar = $("#app-sidebar");
    const collapsed = sidebar.dataset.collapsed !== "true";
    sidebar.dataset.collapsed = String(collapsed);
    $("#sidebar-toggle").setAttribute("aria-expanded", String(!collapsed));
  };
  $("#workspace-menu").hidden = false;
  $("#admin-workspace-button").hidden = !hasAdmin;
  $("#profile-acl").onclick = () => openGuide("acl");
  setupPersonalNavigation();
  openWorkspaceView = (workspace) => {
    const admin = workspace === "admin" && hasAdmin;
    $("#personal-workspace").hidden = admin;
    $("#admin").hidden = !admin;
    $("#personal-menu").hidden = admin;
    $("#admin-menu").hidden = !admin;
    document.querySelectorAll("[data-workspace]").forEach((button) => {
      const active = button.dataset.workspace === (admin ? "admin" : "personal");
      button.classList.toggle("active", active);
      button.setAttribute("aria-current", active ? "page" : "false");
    });
    rememberView({ workspace: admin ? "admin" : "personal" });
  };
  document.querySelectorAll("[data-workspace]").forEach(
    (button) => (button.onclick = () => openWorkspaceView(button.dataset.workspace)),
  );
  openWorkspaceView("personal");
}
function applyInitialNavigation(hasAdmin) {
  if (location.pathname.startsWith("/admin") && !hasAdmin) {
    $("#status").textContent = "관리자 권한이 없어 관리자 화면에 접근할 수 없습니다.";
    $("#status").classList.remove("ok");
    return;
  }
  // 새로고침이나 공유 링크로 들어온 경우 기록된 화면을 그대로 복원합니다.
  const saved = parseViewHash();
  if (saved) {
    restoringView = true;
    if (saved.workspace === "admin" && hasAdmin) {
      openWorkspaceView("admin");
      if (saved.panel) openAdminPanel(saved.panel);
      if (saved.category) openSettingCategory(saved.category);
    } else {
      openWorkspaceView("personal");
      if (saved.personal) openPersonalView(saved.personal);
    }
    restoringView = false;
    rememberView({});
    return;
  }
  if (location.pathname.startsWith("/admin") && hasAdmin) {
    openWorkspaceView("admin");
    openAdminPanel("settings-admin");
    openSettingCategory("keycloak");
  }
}
function setupPersonalNavigation() {
  openPersonalView = (target) => {
    document.querySelectorAll(".personal-panel").forEach(
      (panel) => (panel.hidden = panel.id !== target),
    );
    document.querySelectorAll("[data-personal-target]").forEach((button) => {
      const active = button.dataset.personalTarget === target;
      button.classList.toggle("active", active);
      button.setAttribute("aria-current", active ? "page" : "false");
    });
    rememberView({ personal: target });
  };
  document.querySelectorAll("[data-personal-target]").forEach(
    (button) => (button.onclick = () => openPersonalView(button.dataset.personalTarget)),
  );
  openPersonalView("account");
}
function navigatePersonal(target) {
  openWorkspaceView("personal");
  openPersonalView(target);
  window.scrollTo({ top: 0, behavior: "smooth" });
}
function setupProfileMenu() {
  const toggle = $("#profile-toggle");
  const dropdown = $("#profile-dropdown");
  const close = () => {
    dropdown.hidden = true;
    toggle.setAttribute("aria-expanded", "false");
  };
  toggle.onclick = (event) => {
    event.stopPropagation();
    dropdown.hidden = !dropdown.hidden;
    toggle.setAttribute("aria-expanded", String(!dropdown.hidden));
  };
  document.querySelectorAll("[data-profile-target]").forEach((button) => {
    button.onclick = () => {
      navigatePersonal(button.dataset.profileTarget);
      close();
    };
  });
  document.addEventListener("click", (event) => {
    if (!$("#profile-menu").contains(event.target)) close();
  });
}
function setupQuickNavigation(capabilities) {
  const dialog = $("#quick-nav-dialog");
  const query = $("#quick-nav-query");
  const personal = [
    ["프로필", "내 공간", () => navigatePersonal("account")],
    ["MCP 연결", "내 공간", () => navigatePersonal("connections")],
    ["API 키 관리", "내 공간", () => navigatePersonal("keys")],
    ["내 활동·저장소", "내 공간", () => navigatePersonal("activity")],
  ];
  const admin = [
    ["관리자 설정", "관리자", "settings-admin", capabilities.settings],
    ["사용자 관리", "관리자", "users-admin-section", capabilities.users],
    ["MCP 도구 운영", "관리자", "mcp-admin-section", capabilities.mcp],
    ["소스·색인", "관리자", "source-admin-section", capabilities.source],
    ["검색 품질", "관리자", "quality-admin-section", capabilities.quality],
    ["보안·Secret", "관리자", "security-admin-section", capabilities.security || capabilities.securityEvents],
    ["감사 로그", "관리자", "audit-admin-section", capabilities.audit],
    ["데이터베이스", "관리자", "database-admin-section", capabilities.status],
    ["운영 상태", "관리자", "status-admin-section", capabilities.status],
    ["백업·복구", "관리자", "backup-admin-section", capabilities.backup],
  ].filter((entry) => entry[3]).map(([label, group, target]) => [label, group, () => {
    openWorkspaceView("admin");
    document.querySelector(`[data-admin-target="${target}"]`)?.click();
    window.scrollTo({ top: 0, behavior: "smooth" });
  }]);
  const entries = [...personal, ...admin];
  let selected = 0;
  const render = () => {
    const needle = query.value.trim().toLowerCase();
    const filtered = entries.filter(([label, group]) => `${label} ${group}`.toLowerCase().includes(needle));
    selected = 0;
    $("#quick-nav-results").innerHTML = filtered.map(([label, group], index) => `<button type="button" class="${index === 0 ? "active" : ""}" data-quick-index="${entries.indexOf(filtered[index])}"><span>${esc(label)}</span><small>${esc(group)}</small></button>`).join("") || '<div class="empty">일치하는 메뉴가 없습니다.</div>';
    document.querySelectorAll("[data-quick-index]").forEach((button) => {
      button.onclick = () => {
        entries[Number(button.dataset.quickIndex)][2]();
        dialog.close();
      };
    });
  };
  const open = () => {
    query.value = "";
    render();
    dialog.showModal();
    query.focus();
  };
  $("#quick-nav-button").onclick = open;
  $("#quick-nav-close").onclick = () => dialog.close();
  query.oninput = render;
  query.onkeydown = (event) => {
    const buttons = [...document.querySelectorAll("[data-quick-index]")];
    if (!buttons.length) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      selected = (selected + (event.key === "ArrowDown" ? 1 : -1) + buttons.length) % buttons.length;
      buttons.forEach((button, index) => button.classList.toggle("active", index === selected));
      buttons[selected].scrollIntoView({ block: "nearest" });
    } else if (event.key === "Enter") {
      event.preventDefault();
      buttons[selected].click();
    }
  };
  document.addEventListener("keydown", (event) => {
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
      event.preventDefault();
      dialog.open ? dialog.close() : open();
    }
  });
}
async function loadActivity() {
  try {
    const [notifications, repos, usage, calls] = (
      await Promise.all([
        api("/api/v1/me/notifications"),
        api("/api/v1/me/repositories"),
        api("/api/v1/me/usage"),
        api("/api/v1/me/calls"),
      ])
    ).map(rows);
    $("#my-notifications").innerHTML = notifications.length
      ? `<table><tbody>${notifications.map((n) => `<tr><td>${n.read ? "" : "● "}${esc(n.title)}<br><small>${esc(n.message)}</small></td><td>${date(n.createdAt)}</td><td>${n.read ? "" : `<button data-read="${esc(n.id)}">읽음</button>`}</td></tr>`).join("")}</tbody></table>`
      : "새 알림이 없습니다.";
    document.querySelectorAll("[data-read]").forEach(
      (b) =>
        (b.onclick = async () => {
          await api(
            `/api/v1/me/notifications/${encodeURIComponent(b.dataset.read)}/read`,
            { method: "POST" },
          );
          loadActivity();
        }),
    );
    $("#my-repositories").innerHTML =
      `<table><thead><tr><th>Library ID</th><th>이름</th><th>기본 브랜치</th><th>최근 색인</th></tr></thead><tbody>${repos.map((r) => `<tr><td>${esc(r.libraryId)}</td><td>${esc(r.name)}</td><td>${esc(r.defaultBranch)}</td><td>${date(r.indexedAt)}</td></tr>`).join("")}</tbody></table>`;
    $("#my-usage").innerHTML =
      `<table><thead><tr><th>도구</th><th>결과</th><th>호출</th><th>평균/최대 지연</th><th>평균/최대 응답</th><th>예산 초과</th></tr></thead><tbody>${usage.map((x) => `<tr><td>${esc(x.tool)}</td><td>${esc(x.outcome)}</td><td>${x.calls}</td><td>${Math.round(x.averageLatencyMs)}/${Math.round(x.maximumLatencyMs)} ms</td><td>${Math.round((x.averageResponseBytes || 0) / 1024)}/${Math.round((x.maximumResponseBytes || 0) / 1024)} KB</td><td>${x.truncatedCalls ? `<strong>${x.truncatedCalls}회 잘림</strong>` : "-"}</td></tr>`).join("")}</tbody></table>`;
    $("#my-calls").innerHTML =
      `<table><thead><tr><th>시간</th><th>키</th><th>도구</th><th>Library</th><th>결과/지연</th><th></th></tr></thead><tbody>${calls
        .slice(0, 100)
        .map(
          (x) =>
            `<tr><td>${date(x.occurredAt)}</td><td>${esc(x.apiKeyPrefix)}</td><td>${esc(x.tool)}</td><td>${esc(x.libraryId)}</td><td>${esc(x.outcome)} ${x.resultCount ?? 0}건 / ${x.durationMs}ms${x.traceSummary ? `<br><small>${esc(x.traceSummary)}</small>` : ""}</td><td><button class="secondary" data-my-trace="${esc(x.id)}">X-ray</button></td></tr>`,
        )
        .join("")}</tbody></table>`;
    $$("[data-my-trace]").forEach((button) => (button.onclick = () => openCallTrace(button.dataset.myTrace, true)));
    markEmptyTables();
  } catch (e) {
    console.warn(e);
  }
}
async function loadKeys() {
  const keys = rows(await api("/api/v1/me/api-keys"));
  $("#key-list").innerHTML =
    `<table><thead><tr><th>이름</th><th>Prefix / 제한</th><th>상태</th><th>만료</th><th>마지막 사용</th><th></th></tr></thead><tbody>${keys.map((k) => `<tr><td>${esc(k.name)}</td><td>${esc(k.prefix)}<br><small>${esc((k.scopes || []).join(", "))}<br>${esc((k.restrictions?.allowedCidrs || []).join(", "))} ${esc((k.restrictions?.allowedRepositories || []).join(", "))}<br>분/시/일 ${k.restrictions?.ratePerMinute || 0}/${k.restrictions?.ratePerHour || 0}/${k.restrictions?.ratePerDay || 0}</small></td><td>${esc(k.status)}</td><td>${date(k.expiresAt)}</td><td>${date(k.lastUsedAt)}</td><td>${k.status === "active" || k.status === "disabled" ? `<button class="secondary" data-scopes="${k.id}">Scope 편집</button> ` : ""}${k.status === "active" ? `<button class="secondary" data-disable="${k.id}">중지</button> <button data-rotate="${k.id}">회전</button> <button class="danger" data-revoke="${k.id}">폐기</button>` : k.status === "disabled" ? `<button data-enable="${k.id}">재활성화</button> <button class="danger" data-revoke="${k.id}">폐기</button>` : ""}</td></tr>`).join("")}</tbody></table>`;
  document.querySelectorAll("[data-scopes]").forEach(
    (button) =>
      (button.onclick = () => {
        const key = keys.find((item) => item.id === button.dataset.scopes);
        if (key) openKeyScopeEditor(key);
      }),
  );
  document.querySelectorAll("[data-revoke]").forEach(
    (b) =>
      (b.onclick = async () => {
        if (confirm("이 키를 즉시 폐기할까요?")) {
          await api(`/api/v1/me/api-keys/${b.dataset.revoke}`, {
            method: "DELETE",
          });
          loadKeys();
        }
      }),
  );
  document.querySelectorAll("[data-disable]").forEach(
    (b) =>
      (b.onclick = async () => {
        await api(`/api/v1/me/api-keys/${b.dataset.disable}/disable`, {
          method: "POST",
        });
        loadKeys();
      }),
  );
  document.querySelectorAll("[data-enable]").forEach(
    (b) =>
      (b.onclick = async () => {
        await api(`/api/v1/me/api-keys/${b.dataset.enable}/enable`, {
          method: "POST",
        });
        loadKeys();
      }),
  );
  document.querySelectorAll("[data-rotate]").forEach(
    (b) =>
      (b.onclick = async () => {
        const overlap = prompt(
          "기존 키와 신규 키의 중복 유효 시간(분, 최대 1440)",
          "10",
        );
        if (overlap === null) return;
        const result = await api(
          `/api/v1/me/api-keys/${b.dataset.rotate}/rotate`,
          {
            method: "POST",
            body: JSON.stringify({ overlapMinutes: Number(overlap) }),
          },
        );
        showSecret(result.secret);
        loadKeys();
      }),
  );
  markEmptyTables();
}
$("#new-key").onclick = () => ($("#key-form").hidden = !$("#key-form").hidden);
$("#key-form").onsubmit = async (e) => {
  e.preventDefault();
  const form = new FormData(e.target),
    expiry = form.get("expiresAt"),
    list = (name) =>
      String(form.get(name) || "")
        .split(",")
        .map((x) => x.trim())
        .filter(Boolean);
  const result = await api("/api/v1/me/api-keys", {
    method: "POST",
    body: JSON.stringify({
      name: form.get("name"),
      scopes: form.getAll("scope"),
      expiresAt: expiry ? new Date(expiry + "T23:59:59Z").toISOString() : null,
      restrictions: {
        allowedCidrs: list("cidrs"),
        allowedRepositories: list("repositories"),
        ratePerMinute: Number(form.get("rateMinute")),
        ratePerHour: Number(form.get("rateHour")),
        ratePerDay: Number(form.get("rateDay")),
      },
    }),
  });
  showSecret(result.secret);
  e.target.reset();
  e.target.hidden = true;
  loadKeys();
};
function showSecret(secret) {
  $("#secret").hidden = false;
  $("#secret").innerHTML =
    `<strong>지금 복사하세요. 다시 표시되지 않습니다.</strong><br><code>${esc(secret)}</code>`;
}
// 외부 연결 테스트가 있는 카테고리. 나머지는 [설정 검증]만 노출합니다.
const connectionTestCategories = [
  "keycloak",
  "bitbucket",
  "gitlab",
  "confluence",
  "jira",
  "model",
  "opensearch",
  "observability",
  "backup",
  "vault",
  "notifications",
];
function renderSettingFields(category, value) {
  const fields = integrationSettingFields[category] || [];
  $("#setting-fields").hidden = fields.length === 0;
  $("#test-connection").hidden = !connectionTestCategories.includes(category);
  $("#preview-keycloak").hidden = category !== "keycloak";
  $("#setting-fields").innerHTML = fields
    .map(([key, label, type, fallback]) => {
      const current = value[key] ?? fallback;
      if (type === "boolean")
        return `<label class="toggle-control" data-field-key="${key}"><span>${esc(label)}</span><span class="toggle-row"><input data-setting-key="${key}" data-setting-type="boolean" type="checkbox" ${current ? "checked" : ""} /><span data-toggle-state="${key}">${current ? "사용함" : "사용 안 함"}</span></span></label>`;
      if (type === "textarea")
        return `<label class="wide" data-field-key="${key}">${esc(label)}<textarea data-setting-key="${key}" data-setting-type="string" rows="4">${esc(current)}</textarea></label>`;
      if (type === "json")
        return `<label class="wide" data-field-key="${key}">${esc(label)}<textarea data-setting-key="${key}" data-setting-type="json" rows="4">${esc(JSON.stringify(current || {}, null, 2))}</textarea></label>`;
      if (type.startsWith("select:")) {
        const choices = type.slice(7).split("|");
        return `<label data-field-key="${key}">${esc(label)}<select data-setting-key="${key}" data-setting-type="string">${choices.map((choice) => `<option ${choice === current ? "selected" : ""}>${esc(choice)}</option>`).join("")}</select></label>`;
      }
      if (type === "password") {
        const stored = current === "********";
        const reference =
          typeof current === "string" && current.startsWith("secret://");
        const shown = stored || reference ? current : "";
        const help = stored
          ? "저장된 비밀값입니다. 변경하려면 새 값을 입력하세요."
          : reference
            ? `관리 Secret 참조: ${current}`
            : "저장 시 암호화되며 다시 원문으로 표시되지 않습니다.";
        // 저장된 비밀값을 지우려면 명시적인 조작이 필요합니다. 입력란을 비우는
        // 것만으로는 "변경 없음"과 구분되지 않아 값이 그대로 유지됩니다.
        const clearButton = stored
          ? `<button type="button" class="secondary" data-clear-secret="${key}">저장된 값 지우기</button>`
          : "";
        return `<label data-field-key="${key}">${esc(label)}<input data-setting-key="${key}" data-setting-type="password" data-secret-stored="${stored}" type="password" value="${esc(shown)}" autocomplete="new-password" /><small class="field-help">${esc(help)}</small>${clearButton}</label>`;
      }
      const inputType = type === "array" ? "text" : type;
      const shown = Array.isArray(current) ? current.join(",") : current;
      const numeric = type === "number" ? ' step="any"' : "";
      const required = category === "keycloak" && ["baseUrl", "realm", "clientId"].includes(key) ? " required" : "";
      return `<label data-field-key="${key}">${esc(label)}<input data-setting-key="${key}" data-setting-type="${type}" type="${inputType}"${numeric}${required} value="${esc(shown)}" /></label>`;
    })
    .join("");
  document.querySelectorAll("[data-setting-key]").forEach(
    (field) =>
      (field.oninput = () => {
        let next;
        try {
          next = JSON.parse($("#setting-json").value || "{}");
        } catch {
          next = {};
        }
        const type = field.dataset.settingType;
        try {
          next[field.dataset.settingKey] =
          type === "boolean"
            ? field.checked
            : type === "number"
              ? field.value.trim() === ""
                ? undefined
                : Number(field.value)
              : type === "array"
                ? field.value
                    .split(",")
                    .map((item) => item.trim())
                    .filter(Boolean)
                : type === "json"
                  ? JSON.parse(field.value || "{}")
                : type === "password" &&
                    (field.value === "********" ||
                      (!field.value && field.dataset.secretStored === "true"))
                  ? "********"
                : field.value;
          // 비운 숫자 항목은 0 으로 저장되지 않도록 키 자체를 제거합니다.
          if (next[field.dataset.settingKey] === undefined) delete next[field.dataset.settingKey];
          $("#setting-json").value = JSON.stringify(next, null, 2);
          field.setCustomValidity("");
          if (field.dataset.settingKey === "tlsVerify") applyTLSFieldState();
        } catch {
          field.setCustomValidity("올바른 JSON을 입력하세요.");
        }
      }),
  );
  document
    .querySelectorAll('[data-setting-type="password"][data-secret-stored="true"]')
    .forEach(
      (field) =>
        (field.onfocus = () => {
          if (field.value === "********") field.select();
        }),
    );
  $$("[data-clear-secret]").forEach(
    (button) =>
      (button.onclick = () => {
        const key = button.dataset.clearSecret;
        const field = document.querySelector(`[data-setting-key="${key}"]`);
        if (!field || !confirm(`${key} 에 저장된 비밀값을 지웁니다. 저장하면 이 항목은 비어 있게 됩니다.`)) return;
        field.dataset.secretStored = "false";
        field.value = "";
        field.dispatchEvent(new Event("input"));
        button.remove();
      }),
  );
  applyTLSFieldState();
}

// 저장 중인 설정의 버전. 다른 관리자가 먼저 저장했으면 서버가 409 로 막고,
// 화면은 그 사실과 함께 다시 불러올지 묻습니다. 마지막 저장이 이기는 조용한
// 덮어쓰기가 설정 화면에서 가장 흔한 사고입니다.
let loadedSettingVersion = 0;

async function saveSettingValue(category, json, force = false) {
  const body = JSON.parse(json);
  if (loadedSettingVersion > 0) body.expectedVersion = loadedSettingVersion;
  try {
    return await api(`/api/v1/admin/settings/${category}${force ? "?force=true" : ""}`, {
      method: "PUT",
      body: JSON.stringify(body),
    });
  } catch (error) {
    if (error.status === 409) {
      if (confirm(`${error.message}\n\n지금 화면을 다시 불러올까요? (편집 중인 내용은 사라집니다)`)) {
        await loadCurrentSetting(category);
      }
      return null;
    }
    if (error.status === 400 && /setting_validation_failed|검증|unreachable|refused|dial|timeout/i.test(`${error.title || ""} ${error.message}`)) {
      // 대상 서버 점검 중에도 설정을 준비할 수 있어야 합니다. 다만 건너뛴
      // 사실을 저장 결과와 감사 로그에 남깁니다.
      if (confirm(`연결 검증에 실패했습니다.\n\n${error.message}\n\n검증을 건너뛰고 그대로 저장할까요?`)) {
        return saveSettingValue(category, json, true);
      }
      return null;
    }
    throw error;
  }
}

// 설정 내보내기·가져오기
// 온프레미스에서는 검증한 환경을 다른 환경에 그대로 옮기는 일이 잦습니다. 파일에
// 비밀값을 담지 않고, 적용 전에 무엇이 바뀌는지 먼저 보여 주는 것이 핵심입니다.
let importDocument = null;
function setupSettingTransfer(capabilities) {
  const block = $("#setting-transfer");
  block.hidden = !capabilities.platform;
  if (!capabilities.platform) return;
  $("#export-settings").onclick = () => {
    location.href = "/api/v1/admin/settings-export";
  };
  const output = $("#import-settings-result");
  const readFile = async () => {
    const file = $("#import-settings-file").files?.[0];
    if (!file) {
      output.innerHTML = '<p class="field-help">가져올 JSON 파일을 먼저 선택하세요.</p>';
      return null;
    }
    return file.text();
  };
  const render = (result) => {
    const items = rows(result.results);
    const label = { ready: "적용 대기", applied: "적용됨", unchanged: "변경 없음", invalid: "검증 실패", forbidden: "권한 없음", skipped: "알 수 없는 영역", failed: "저장 실패" };
    output.innerHTML = `<div class="notice ${result.dryRun ? "" : "ok"}">${result.dryRun ? "미리보기" : `적용 완료 · ${result.applied}개 영역`}</div>
<table><thead><tr><th>영역</th><th>상태</th><th>변경 항목</th></tr></thead><tbody>${items
      .map(
        (item) =>
          `<tr><td>${esc(item.category)}</td><td>${esc(label[item.status] || item.status)}<br><small>${esc(item.detail || "")}</small></td>
<td>${rows(item.changes).map((change) => `${esc(change.field)}: <code>${esc(change.before)}</code> → <code>${esc(change.after)}</code>`).join("<br>") || "-"}
${item.missingSecrets ? `<br><small>입력 필요: ${esc(item.missingSecrets.join(", "))}</small>` : ""}</td></tr>`,
      )
      .join("")}</tbody></table>`;
  };
  $("#preview-import-settings").onclick = async () => {
    const text = await readFile();
    if (!text) return;
    try {
      importDocument = text;
      render(await api("/api/v1/admin/settings-import", { method: "POST", body: text }));
      $("#apply-import-settings").hidden = false;
    } catch (error) {
      output.innerHTML = `<div class="notice error">${esc(error.message)}</div>`;
    }
  };
  $("#apply-import-settings").onclick = async () => {
    if (!importDocument || !confirm("미리본 변경 내용을 지금 적용합니다. 각 영역은 새 버전으로 기록되어 되돌릴 수 있습니다.")) return;
    try {
      render(await api("/api/v1/admin/settings-import?apply=true", { method: "POST", body: importDocument }));
      $("#apply-import-settings").hidden = true;
      await loadCurrentSetting($("#category").value);
    } catch (error) {
      output.innerHTML = `<div class="notice error">${esc(error.message)}</div>`;
    }
  };
}

// loadSettingHistory는 누가 언제 몇 번째 버전을 저장했는지만 보여 줍니다.
// 저장된 값 자체는 암호문으로만 남아 있어 화면에 노출하지 않습니다.
async function loadSettingHistory(category) {
  const target = $("#setting-history-list");
  try {
    const versions = rows(await api(`/api/v1/admin/settings/${category}/versions`));
    target.innerHTML = `<table><thead><tr><th>버전</th><th>변경자</th><th>변경 시각</th><th></th></tr></thead><tbody>${versions
      .map(
        (item) =>
          `<tr><td>v${item.version}</td><td>${esc(item.changedBy)}</td><td>${date(item.changedAt)}</td><td><button class="secondary" data-version-view="${item.version}">비교·되돌리기</button></td></tr>`,
      )
      .join("")}</tbody></table>`;
    $$("[data-version-view]").forEach(
      (button) => (button.onclick = () => openSettingVersion(category, Number(button.dataset.versionView))),
    );
    markEmptyTables();
  } catch (error) {
    target.innerHTML = `<div class="empty">${esc(error.message)}</div>`;
  }
}

// openSettingVersion은 과거 버전과 현재 값의 차이를 먼저 보여 준 뒤 되돌립니다.
// 비밀값은 "변경됨"까지만 표시하고 내용은 드러내지 않습니다.
async function openSettingVersion(category, version) {
  const dialog = $("#setting-version-dialog");
  try {
    const detail = await api(`/api/v1/admin/settings/${category}/versions/${version}`);
    $("#setting-version-title").textContent = `${category} v${version} · 현재 v${detail.currentVersion}`;
    $("#setting-version-meta").textContent = `${detail.changedBy || "알 수 없음"} · ${date(detail.changedAt)}`;
    const changes = rows(detail.changes);
    $("#setting-version-diff").innerHTML = changes.length
      ? `<table><thead><tr><th>항목</th><th>현재</th><th>이 버전</th></tr></thead><tbody>${changes
          .map(
            (change) =>
              `<tr><td>${esc(change.field)}${change.secret ? " 🔒" : ""}</td><td><code>${esc(change.before)}</code></td><td><code>${esc(change.after)}</code></td></tr>`,
          )
          .join("")}</tbody></table>`
      : '<p class="field-help">현재 값과 동일합니다. 되돌려도 바뀌는 항목이 없습니다.</p>';
    $("#setting-version-json").textContent = JSON.stringify(detail.value, null, 2);
    $("#setting-version-restore").onclick = async () => {
      if (!confirm(`${category} 설정을 v${version} 내용으로 되돌립니다. 이력은 지워지지 않고 새 버전으로 기록됩니다.`)) return;
      try {
        const result = await api(`/api/v1/admin/settings/${category}/versions/${version}/restore`, { method: "POST" });
        dialog.close();
        showAdmin(`v${version} 내용을 v${result.version} 으로 되돌렸습니다.`, true);
        await loadCurrentSetting(category);
      } catch (error) {
        showAdmin(`되돌리지 못했습니다: ${error.message}`, false);
      }
    };
    dialog.showModal();
  } catch (error) {
    showAdmin(`설정 버전을 불러오지 못했습니다: ${error.message}`, false);
  }
}

// refreshVectorStatus 는 벡터 DB 연결 상태와 저장된 벡터 수를 비교해 보여 줍니다.
// 미설정도 정상 상태이므로 오류처럼 표시하지 않습니다.
async function refreshVectorStatus() {
  const panel = $("#vector-status");
  panel.hidden = false;
  panel.className = "result-panel";
  panel.textContent = "벡터 DB 상태를 확인하는 중…";
  try {
    const status = await api("/api/v1/admin/vector/status");
    if (!status.configured) {
      panel.className = "result-panel";
      panel.innerHTML = `<h4>벡터 DB 미사용</h4><ul class="result-list"><li>${esc(status.detail)}</li><li>메타 DB에 저장된 임베딩 ${status.storedVectors}개</li></ul>`;
      return;
    }
    const ready = status.ready && !status.error;
    panel.className = `result-panel ${ready ? "ok" : "error"}`;
    panel.innerHTML =
      `<h4>${esc(status.provider)} · ${ready ? "연결됨" : "연결 실패"}</h4><ul class="result-list">` +
      `<li>대상: ${esc(status.target || "-")} · 컬렉션 ${esc(status.collection || "-")}</li>` +
      `<li>벡터 DB ${status.vectors ?? 0}개 / 메타 DB 임베딩 ${status.storedVectors}개</li>` +
      `<li>${esc(status.error || status.detail || "")}</li></ul>`;
  } catch (error) {
    panel.className = "result-panel error";
    panel.textContent = error.message;
  }
}

function renderSettingResult(ok, title, lines, payload) {
  const panel = $("#setting-test-result");
  panel.hidden = false;
  panel.className = `wide result-panel ${ok ? "ok" : "error"}`;
  panel.innerHTML =
    `<h4>${esc(title)}</h4><ul class="result-list">${(lines || []).map((line) => `<li>${esc(line)}</li>`).join("")}</ul>` +
    (payload ? `<details><summary>적용될 설정 값 (마스킹됨)</summary><pre>${esc(JSON.stringify(payload, null, 2))}</pre></details>` : "");
}

/* ---------------------------------------------------------------------------
 * Keycloak 역할·Claim 매핑 편집기
 * 관리자가 ACL 문제로 설정을 저장하지 못하는 가장 흔한 원인이 역할 매핑 부재라서,
 * JSON을 직접 편집하지 않고도 매핑을 추가·삭제할 수 있게 합니다.
 * ------------------------------------------------------------------------- */
const platformRoleChoices = [
  "platform-admin",
  "security-admin",
  "mcp-admin",
  "source-admin",
  "search-admin",
  "auditor",
  "readonly-operator",
  "developer",
  "service-account",
];

function currentSettingValue() {
  try {
    return JSON.parse($("#setting-json").value || "{}");
  } catch {
    return {};
  }
}
function writeSettingValue(value) {
  $("#setting-json").value = JSON.stringify(value, null, 2);
}

function renderKeycloakMappings() {
  const value = currentSettingValue();
  const rows = [
    ...Object.entries(value.realmRoleMappings || {}).map(([source, target]) => ["realm", source, target]),
    ...Object.entries(value.clientRoleMappings || {}).map(([source, target]) => ["client", source, target]),
  ];
  $("#keycloak-role-mappings").innerHTML = rows.length
    ? `<table><thead><tr><th>종류</th><th>Keycloak 역할</th><th>플랫폼 역할</th><th></th></tr></thead><tbody>${rows
        .map(
          ([kind, source, target]) =>
            `<tr><td>${kind}</td><td><code>${esc(source)}</code></td><td><code>${esc(target)}</code></td><td><button type="button" class="danger" data-drop-mapping="${esc(kind)}:${esc(source)}">삭제</button></td></tr>`,
        )
        .join("")}</tbody></table>`
    : '<div class="empty">등록된 매핑이 없습니다. Keycloak 역할 이름이 플랫폼 역할과 같으면 매핑 없이도 인식됩니다.</div>';
  $$("[data-drop-mapping]").forEach((button) => {
    button.onclick = () => {
      const [kind, ...rest] = button.dataset.dropMapping.split(":");
      const source = rest.join(":");
      const next = currentSettingValue();
      const bucket = kind === "realm" ? "realmRoleMappings" : "clientRoleMappings";
      if (next[bucket]) delete next[bucket][source];
      writeSettingValue(next);
      renderKeycloakMappings();
      toast("매핑을 제거했습니다. [저장]을 눌러야 반영됩니다.", "info");
    };
  });
  $("#mapping-platform-role").innerHTML = platformRoleChoices
    .map((role) => `<option value="${role}">${role}</option>`)
    .join("");
  $("#claim-bitbucket").value = value.bitbucketUserSlugClaim || "";
  $("#claim-gitlab").value = value.gitlabUserIdClaim || "";
  $("#claim-groups").value = value.groupsClaim || "";
}

function setupKeycloakMappingEditor() {
  $("#add-role-mapping").onclick = () => {
    const source = $("#mapping-source-role").value.trim();
    if (!source) {
      toast("Keycloak 역할 이름을 입력하세요.", "error");
      return;
    }
    const bucket = $("#mapping-kind").value === "realm" ? "realmRoleMappings" : "clientRoleMappings";
    const value = currentSettingValue();
    value[bucket] = { ...(value[bucket] || {}), [source]: $("#mapping-platform-role").value };
    writeSettingValue(value);
    $("#mapping-source-role").value = "";
    renderKeycloakMappings();
    toast("매핑을 추가했습니다. [저장]을 눌러야 반영됩니다.", "ok");
  };
  const claimFields = [
    ["#claim-bitbucket", "bitbucketUserSlugClaim"],
    ["#claim-gitlab", "gitlabUserIdClaim"],
    ["#claim-groups", "groupsClaim"],
  ];
  for (const [selector, key] of claimFields) {
    $(selector).oninput = () => {
      const value = currentSettingValue();
      const entered = $(selector).value.trim();
      if (entered) value[key] = entered;
      else delete value[key];
      writeSettingValue(value);
    };
  }
}

function applyTLSFieldState() {
  const toggle = document.querySelector('[data-setting-key="tlsVerify"]');
  if (!toggle) return;
  const enabled = toggle.checked;
  const state = document.querySelector('[data-toggle-state="tlsVerify"]');
  if (state) state.textContent = enabled ? "사용함" : "사용 안 함";
  const caField = document.querySelector('[data-field-key="caCertificate"]');
  if (caField) {
    caField.hidden = !enabled;
    caField.querySelectorAll("input,textarea").forEach((field) => (field.disabled = !enabled));
  }
}
function setupAdmin(roles, capabilities) {
  const allowedCategories = GitCtxRoles.categoriesFor(categories, [...roles]);
  $("#settings-admin").hidden = !capabilities.settings;
  if (capabilities.users) {
    $("#new-admin-user").onclick = () => openAdminUserForm();
    $("#cancel-admin-user").onclick = () => ($("#admin-user-form").hidden = true);
    $("#admin-user-form").onsubmit = async (event) => {
      event.preventDefault();
      const id = $("#admin-user-id").value;
      const payload = {
        subject: $("#admin-user-subject").value.trim(),
        username: $("#admin-user-username").value.trim(),
        email: $("#admin-user-email").value.trim(),
        status: $("#admin-user-status").value,
        roles: [...document.querySelectorAll('[name="admin-role"]:checked')].map((input) => input.value),
      };
      try {
        await api(id ? `/api/v1/admin/users/${encodeURIComponent(id)}` : "/api/v1/admin/users", {
          method: id ? "PUT" : "POST",
          body: JSON.stringify(payload),
        });
        $("#admin-user-form").hidden = true;
        await refreshAdminUsers();
      } catch (error) {
        showAdmin(error.message, false);
      }
    };
  }
  if (!capabilities.settings) return;
  $("#category").innerHTML = allowedCategories
    .map((c) => `<option>${c}</option>`)
    .join("");
  $("#setting-tabs").innerHTML = allowedCategories
    .map(
      (category) =>
        `<button type="button" role="tab" data-setting-tab="${category}" title="${esc(settingCategoryMeta[category]?.[1] || "")}">${esc(settingCategoryMeta[category]?.[0] || category)}</button>`,
    )
    .join("");
  // 설정 탭이 19개까지 늘어나므로 이름·설명·카테고리 키로 즉시 필터링합니다.
  $("#setting-search").oninput = () => {
    const needle = $("#setting-search").value.trim().toLowerCase();
    $$("[data-setting-tab]").forEach((tab) => {
      const category = tab.dataset.settingTab;
      const haystack = `${category} ${(settingCategoryMeta[category] || []).join(" ")}`.toLowerCase();
      tab.hidden = needle !== "" && !haystack.includes(needle);
    });
  };
  const selectCategory = async (category) => {
    $("#category").value = category;
    document.querySelectorAll("[data-setting-tab]").forEach((tab) => {
      const active = tab.dataset.settingTab === category;
      tab.classList.toggle("active", active);
      tab.setAttribute("aria-selected", String(active));
    });
    const meta = settingCategoryMeta[category] || [category, "동적 운영 설정"];
    $("#setting-context").innerHTML = `<strong>${esc(meta[0])}</strong><span>${esc(meta[1])}</span>`;
    $("#login-keycloak").hidden = category !== "keycloak";
    $("#keycloak-runtime-status").hidden = category !== "keycloak";
    $("#advanced-setting-json").hidden = category === "keycloak";
    $("#keycloak-mapping-card").hidden = category !== "keycloak";
    $("#source-test-card").hidden = !["bitbucket", "gitlab"].includes(category);
    $("#vector-card").hidden = category !== "vector";
    if (category === "vector") refreshVectorStatus();
    $("#setting-test-result").hidden = true;
    $("#setting-guide-button").hidden = !GitCtxGuides.has(category);
    $("#setting-guide-button").dataset.guide = category;
    rememberView({ category });
    await loadCurrentSetting(category);
  };
  openSettingCategory = (category) => {
    if (document.querySelector(`[data-setting-tab="${category}"]`)) selectCategory(category);
  };
  document.querySelectorAll("[data-setting-tab]").forEach(
    (tab) => (tab.onclick = () => selectCategory(tab.dataset.settingTab)),
  );
  $("#category").onchange = () => selectCategory($("#category").value);
  const loadCurrentSetting = async (category = $("#category").value) => {
    try {
      const x = await api(`/api/v1/admin/settings/${category}`);
      if ($("#category").value !== category) return;
      loadedSettingVersion = x.version || 0;
      $("#setting-json").value = JSON.stringify(x.value, null, 2);
      renderSettingFields(category, x.value);
      const maskedCount = (x.maskedFields || []).length;
      $("#setting-load-status").className = "wide notice ok";
      $("#setting-load-status").textContent =
        `저장된 설정 v${x.version}을 불러왔습니다 · ${date(x.updatedAt)} · ${x.updatedBy || "알 수 없는 관리자"}${maskedCount ? ` · 비밀값 ${maskedCount}개 마스킹됨` : ""}`;
      $("#delete-setting").hidden = false;
      loadSettingHistory(category);
      if (category === "keycloak") {
        renderKeycloakMappings();
        refreshKeycloakStatus();
      }
    } catch (e) {
      if ($("#category").value !== category) return;
      if (e.status !== 404) {
        showAdmin(`설정을 불러오지 못했습니다: ${e.message}`, false);
        return;
      }
      loadedSettingVersion = 0;
      const defaults = settingDefaults(category);
      $("#setting-json").value = JSON.stringify(defaults, null, 2);
      renderSettingFields(category, defaults);
      $("#setting-load-status").className = "wide notice";
      $("#setting-load-status").textContent =
        "저장된 설정이 없습니다. 기본값을 표시합니다.";
      $("#delete-setting").hidden = true;
      loadSettingHistory(category);
      if (category === "keycloak") {
        renderKeycloakMappings();
        refreshKeycloakStatus();
      }
    }
  };
  // 연결 테스트와 검증 결과는 배너 한 줄이 아니라 구조화된 패널로 남겨,
  // 어떤 단계가 통과했고 무엇이 실패했는지 화면에서 바로 확인할 수 있게 합니다.
  const runSettingCheck = async (kind) => {
    const category = $("#category").value;
    const button = kind === "test" ? $("#test-connection") : $("#validate-setting");
    const label = button.textContent;
    button.disabled = true;
    button.textContent = kind === "test" ? "테스트 중…" : "검증 중…";
    try {
      const result = await api(`/api/v1/admin/settings/${category}/${kind}`, {
        method: "POST",
        body: $("#setting-json").value,
      });
      const rows = [];
      if (kind === "test") {
        rows.push("외부 연결과 자격 증명 검증 통과");
        const querySearch = result.details?.querySearch;
        if (querySearch) {
          rows.push(
            querySearch.status === "verified"
              ? `코드 검색 API 확인: ${querySearch.project}/${querySearch.repository} @ ${querySearch.ref} · 질의 "${querySearch.query}" · 결과 ${querySearch.matches}건`
              : `코드 검색 API 건너뜀: ${querySearch.reason || "확인 대상 없음"}`,
          );
        }
      } else {
        rows.push("입력값 형식과 정규화 검증 통과. 저장하지 않았습니다.");
      }
      renderSettingResult(true, `${category} ${kind === "test" ? "연결 테스트" : "설정 검증"} 성공`, rows, result.normalized);
      toast(`${category} ${kind === "test" ? "연결 테스트" : "설정 검증"}에 성공했습니다.`, "ok");
    } catch (error) {
      renderSettingResult(false, `${category} ${kind === "test" ? "연결 테스트" : "설정 검증"} 실패`, [error.message]);
      reportError(error);
    } finally {
      button.disabled = false;
      button.textContent = label;
    }
  };
  $("#test-connection").onclick = () => runSettingCheck("test");
  $("#validate-setting").onclick = () => runSettingCheck("validate");
  // 저장 전 설정으로 실제 원격 검색을 수행해 "연동은 됐는데 검색이 0건"인 상황을
  // 설정 화면에서 바로 구분할 수 있게 합니다.
  $("#source-test-form").onsubmit = async (event) => {
    event.preventDefault();
    const category = $("#category").value;
    const panel = $("#source-test-result");
    panel.hidden = false;
    panel.className = "result-panel";
    panel.textContent = "원격 검색을 실행하고 있습니다…";
    try {
      const result = await api("/api/v1/tools/search-code/test", {
        method: "POST",
        body: JSON.stringify({
          query: $("#source-test-query").value || "README",
          sourceType: category,
          project: $("#source-test-project").value,
          limit: 10,
        }),
      });
      const repositories = result.Repositories || [];
      const hits = result.Hits || [];
      panel.className = `result-panel ${hits.length || repositories.length ? "ok" : "error"}`;
      panel.innerHTML =
        `<h4>저장소 ${repositories.length}건 · 코드 ${hits.length}건</h4>` +
        (repositories.length
          ? `<div>${repositories.slice(0, 8).map((item) => `<span class="chip">${esc(item.LibraryID)}</span>`).join("")}</div>`
          : "") +
        (hits.length
          ? `<ul class="result-list">${hits.slice(0, 8).map((hit) => `<li><code>${esc(hit.LibraryID)}</code> · ${esc(hit.Path)} (L${hit.LineStart})</li>`).join("")}</ul>`
          : "") +
        ((result.Diagnostics || []).length
          ? `<ul class="result-list">${result.Diagnostics.map((item) => `<li>${esc(item)}</li>`).join("")}</ul>`
          : "") +
        (result.Warning ? `<div class="guide-notice">${esc(result.Warning)}</div>` : "");
    } catch (error) {
      panel.className = "result-panel error";
      panel.textContent = error.message;
    }
  };
  setupKeycloakMappingEditor();
  $("#refresh-vector-status").onclick = refreshVectorStatus;
  $("#rebuild-vectors").onclick = async () => {
    if (!confirm("메타 DB의 모든 임베딩을 벡터 DB로 다시 적재합니다. 대상이 많으면 시간이 걸릴 수 있습니다. 진행할까요?")) return;
    const button = $("#rebuild-vectors");
    button.disabled = true;
    button.textContent = "적재 중…";
    try {
      const result = await api("/api/v1/admin/vector/rebuild", { method: "POST", body: "{}" });
      showAdmin(`${result.provider}에 벡터 ${result.projected}개를 적재했습니다.`, true);
      refreshVectorStatus();
    } catch (error) {
      reportError(error, "벡터 재적재");
    } finally {
      button.disabled = false;
      button.textContent = "벡터 재적재 (마이그레이션)";
    }
  };
  $("#setting-json").oninput = () => {
    try {
      renderSettingFields(
        $("#category").value,
        JSON.parse($("#setting-json").value),
      );
      if ($("#category").value === "keycloak") renderKeycloakMappings();
    } catch {}
  };
  if (allowedCategories.length) selectCategory(allowedCategories[0]);
  $("#preview-keycloak").onclick = async () => {
    if ($("#category").value !== "keycloak")
      return showAdmin("keycloak 영역에서만 미리볼 수 있습니다.", false);
    const token = prompt(
      "테스트 사용자의 짧은 만료 Access/ID Token을 입력하세요. 저장·기록되지 않습니다.",
    );
    if (!token) return;
    try {
      const x = await api("/api/v1/admin/settings/keycloak/preview", {
        method: "POST",
        body: JSON.stringify({
          config: JSON.parse($("#setting-json").value),
          token,
        }),
      });
      showAdmin(
        `사용자 ${x.username} · 역할 ${(x.roles || []).join(", ")} · 그룹 ${(x.groups || []).join(", ")} · Bitbucket ${x.bitbucketUserSlug || "미매핑"} · GitLab ${x.gitlabUserId || "미매핑"}`,
        true,
      );
    } catch (e) {
      showAdmin(e.message, false);
    }
  };
  $("#login-keycloak").onclick = () => {
    if ($("#category").value !== "keycloak") return;
    location.href = `/auth/login?return_to=${encodeURIComponent("/admin")}`;
  };
  $("#save-setting").onclick = async () => {
    const button = $("#save-setting");
    try {
      if (!$("#setting-fields").querySelectorAll("input,select,textarea").length) {
        JSON.parse($("#setting-json").value);
      }
      const invalid = $("#setting-fields").querySelector(":invalid");
      if (invalid) {
        invalid.reportValidity();
        return;
      }
      JSON.parse($("#setting-json").value);
      button.disabled = true;
      button.textContent = "저장·검증 중…";
      const x = await saveSettingValue($("#category").value, $("#setting-json").value);
      if (!x) return;
      if ($("#category").value === "keycloak" && bootstrapInfo.required) {
        showAdmin(
          `버전 ${x.version} 저장 완료. 이제 “Keycloak 로그인 시험”으로 platform-admin 로그인을 완료하세요. 성공할 때까지 최초 관리자 복구 세션은 유지됩니다.`,
          true,
        );
      } else {
        const restart = x.restartRequired
          ? " 점검 모드는 즉시 반영되며 수신 주소와 Timeout은 서비스 재기동 후 반영됩니다."
          : "";
        showAdmin(`버전 ${x.version} 저장 완료.${restart}`, true);
      }
      if (x.validationSkipped) {
        renderSettingResult(false, "연결 검증을 건너뛰고 저장했습니다", [x.validationSkipped, x.warning]);
      }
      await loadCurrentSetting($("#category").value);
    } catch (e) {
      renderSettingResult(false, "설정을 저장하지 못했습니다", [e.message]);
      reportError(e, "저장 실패");
    } finally {
      button.disabled = false;
      button.textContent = "저장";
    }
  };
  setupSettingTransfer(capabilities);
  $("#setting-version-close").onclick = () => $("#setting-version-dialog").close();
  $("#setting-version-cancel").onclick = () => $("#setting-version-dialog").close();
  $("#delete-setting").onclick = async () => {
    const category = $("#category").value;
    if (!confirm(`${settingCategoryMeta[category]?.[0] || category} 설정을 삭제하시겠습니까?`)) return;
    try {
      await api(`/api/v1/admin/settings/${category}`, { method: "DELETE" });
      showAdmin("설정을 삭제했습니다.", true);
      await loadCurrentSetting(category);
    } catch (error) {
      showAdmin(`삭제하지 못했습니다: ${error.message}`, false);
    }
  };
}
async function refreshKeycloakStatus() {
  const target = $("#keycloak-runtime-status");
  target.hidden = false;
  target.className = "notice";
  target.textContent = "저장된 OIDC 설정의 실제 Discovery 상태를 확인하고 있습니다…";
  try {
    const status = await api("/api/v1/admin/settings/keycloak/status");
    target.className = "notice ok";
    target.innerHTML = `<strong>OIDC v${status.version} 적용됨</strong><br>${esc(status.issuerUrl)}<br><small>Client ${esc(status.clientId)} · Redirect ${esc(status.redirectUrl)}<br>Authorization ${esc(status.metadata?.authorizationEndpoint || "-")}<br>Token ${esc(status.metadata?.tokenEndpoint || "-")}<br>JWKS ${esc(status.metadata?.jwksUri || "-")}</small>`;
  } catch (error) {
    target.className = "notice error";
    target.textContent = error.status === 404 ? "저장된 Keycloak OIDC 설정이 없습니다." : `저장된 OIDC 설정을 적용할 수 없습니다: ${error.message}`;
  }
}
let discovered = [];
let activeCapabilities = {};

/* ---------------------------------------------------------------------------
 * 초기 설정 진행 상황
 * 새로 설치한 인스턴스에서 무엇이 남았는지 한 화면에 보여 주고, 각 단계에서
 * 필요한 설정 화면으로 바로 이동시킵니다.
 * ------------------------------------------------------------------------- */
const setupStatusLabels = { done: "완료", warn: "확인 필요", todo: "미완료" };

async function refreshSetupStatus() {
  const panel = $("#setup-status-panel");
  try {
    const status = await api("/api/v1/admin/setup-status");
    panel.hidden = false;
    $("#setup-status-summary").textContent = status.ready
      ? `초기 설정 ${status.completed}/${status.total} 단계가 모두 준비되었습니다.`
      : `초기 설정 ${status.completed}/${status.total} 단계 완료 — 남은 단계를 눌러 바로 이동하세요.`;
    $("#setup-status-steps").innerHTML = rows(status.steps)
      .map(
        (step) =>
          `<article class="setup-step ${esc(step.status)}"><header><span class="chip ${step.status === "done" ? "ok" : step.status === "todo" ? "error" : ""}">${esc(setupStatusLabels[step.status] || step.status)}</span><strong>${esc(step.title)}</strong></header><p>${esc(step.detail)}</p><button type="button" class="secondary" data-setup-target="${esc(step.target)}" data-setup-category="${esc(step.category || "")}">이동</button></article>`,
      )
      .join("");
    $$("[data-setup-target]").forEach((button) => {
      button.onclick = () => {
        document.querySelector(`[data-admin-target="${button.dataset.setupTarget}"]`)?.click();
        if (button.dataset.setupCategory) {
          document.querySelector(`[data-setting-tab="${button.dataset.setupCategory}"]`)?.click();
        }
        window.scrollTo({ top: 0, behavior: "smooth" });
      };
    });
  } catch {
    // 운영 권한이 없는 관리자는 이 카드를 보지 않습니다.
    panel.hidden = true;
  }
}

// runSearchDiagnostics는 다른 사용자의 ACL로 검색을 재현합니다. 서버는 코드
// 조각과 파일 경로를 제외한 판정 근거만 돌려줍니다.
async function runSearchDiagnostics(event) {
  event.preventDefault();
  const panel = $("#search-diagnostics-result");
  panel.hidden = false;
  panel.className = "result-panel";
  panel.textContent = "진단 중…";
  try {
    const result = await api("/api/v1/admin/search-diagnostics", {
      method: "POST",
      body: JSON.stringify({
        username: $("#diagnostics-username").value,
        query: $("#diagnostics-query").value,
        sourceType: $("#diagnostics-source").value,
      }),
    });
    const target = result.target || {};
    panel.className = `result-panel ${result.hitCount || result.repositoryCount ? "ok" : "error"}`;
    panel.innerHTML =
      `<h4>${esc(target.username || "")} · 저장소 ${result.repositoryCount}건 · 코드 ${result.hitCount}건</h4>` +
      `<div>역할 ${(target.roles || []).map((role) => `<span class="chip">${esc(role)}</span>`).join("") || '<span class="chip error">없음</span>'}</div>` +
      `<div>ACL Principal ${(target.aclPrincipals || []).map((principal) => `<span class="chip ${target.aclReady ? "ok" : ""}">${esc(principal)}</span>`).join("") || '<span class="chip error">매핑 없음</span>'}</div>` +
      (rows(result.repositories).length
        ? `<ul class="result-list">${result.repositories.map((item) => `<li><code>${esc(item.libraryId)}</code> · ${esc(item.sourceType)} · 코드 ${item.hits}건</li>`).join("")}</ul>`
        : "") +
      (rows(result.diagnostics).length
        ? `<ul class="result-list">${result.diagnostics.map((item) => `<li>${esc(item)}</li>`).join("")}</ul>`
        : "") +
      (result.warning ? `<div class="guide-notice">${esc(result.warning)}</div>` : "") +
      (result.error ? `<div class="guide-notice">${esc(result.error)}</div>` : "") +
      `<small class="field-help">${esc(result.note || "")}</small>`;
  } catch (error) {
    panel.className = "result-panel error";
    panel.textContent = error.message;
    reportError(error, "검색 진단");
  }
}
function setupOps(capabilities) {
  activeCapabilities = capabilities;
  $("#mcp-admin-section").hidden = !capabilities.mcp;
  $("#source-admin-section").hidden = !capabilities.source;
  $("#status-admin-section").hidden = !capabilities.status;
  $("#database-admin-section").hidden = !capabilities.status;
  $("#backup-admin-section").hidden = !capabilities.backup;
  $("#quality-admin-section").hidden = !capabilities.quality;
  $("#quality-write-section").hidden = !capabilities.qualityWrite;
  $("#quality-run-controls").hidden = !capabilities.qualityWrite;
  $("#security-admin-section").hidden = !(
    capabilities.security || capabilities.securityEvents
  );
  $("#security-keys-section").hidden = !capabilities.security;
  $("#security-events-section").hidden = !capabilities.securityEvents;
  $("#audit-admin-section").hidden = !capabilities.audit;
  $("#create-backup").hidden = !capabilities.backupWrite;
  $("#refresh-database").onclick = refreshDatabase;
  $("#database-migration").hidden = !capabilities.platform;
  if (capabilities.platform) {
    $("#test-database").onclick = () => runDatabaseAction("test");
    $("#migrate-database").onclick = () => runDatabaseAction("migrate");
  }
  if (capabilities.security) {
    $("#secret-form").onsubmit = async (event) => {
      event.preventDefault();
      const form = new FormData(event.target);
      try {
        await api("/api/v1/admin/secrets", {
          method: "POST",
          body: JSON.stringify({
            name: form.get("name"),
            backend: form.get("backend"),
            value: form.get("value"),
          }),
        });
        event.target.reset();
        showAdmin("비밀정보를 등록·회전했습니다. 원문은 다시 표시되지 않습니다.", true);
        refreshSecurity(capabilities);
      } catch (e) {
        showAdmin(e.message, false);
      }
    };
  }
  if (capabilities.backupWrite) {
    $("#create-backup").onclick = async () => {
      try {
        await api("/api/v1/admin/backups", { method: "POST" });
        showAdmin("암호화 백업을 생성했습니다.", true);
        refreshBackups(capabilities);
      } catch (e) {
        showAdmin(e.message, false);
      }
    };
  }
  if (capabilities.sourceWrite) $("#discover").onclick = discover;
  else $("#discover").hidden = true;
  setupMCPAdmin(capabilities);
  $("#refresh-ops").onclick = () => {
    refreshOps(capabilities);
    refreshSecurity(capabilities);
  };
  refreshOps(capabilities);
  refreshSecurity(capabilities);
  refreshBackups(capabilities);
  setupIndexPolicyDialog();
  $("#refresh-index-diagnostics").onclick = () => refreshIndexDiagnostics(capabilities);
  $("#refresh-source-health").onclick = () => refreshSourceHealth(capabilities);
  refreshSourceHealth(capabilities);
  $("#refresh-setup-status").onclick = refreshSetupStatus;
  refreshSetupStatus();
  if (capabilities.qualityWrite) {
    $("#search-diagnostics-form").onsubmit = runSearchDiagnostics;
  } else {
    $("#search-diagnostics-form").hidden = true;
  }
  if (capabilities.quality) {
    $("#refresh-quality").onclick = () => refreshQuality(capabilities);
    $("#refresh-context-packs").onclick = () => refreshContextPacks(capabilities);
    refreshQuality(capabilities);
    refreshContextPacks(capabilities);
  }
  if (capabilities.qualityWrite) {
    $("#create-quality-case").onclick = () => createQualityCase(capabilities);
    $("#run-quality").onclick = () => runQuality(capabilities);
    $("#context-pack-admin-form").onsubmit = (event) => saveContextPack(event, capabilities);
    $("#reset-context-pack").onclick = resetContextPack;
  }
  setupAdminNavigation(capabilities);
}
function setupAdminNavigation(capabilities) {
  const entries = [
    ["settings-admin", "설정", capabilities.settings, "⚙️"],
    ["users-admin-section", "사용자", capabilities.users, "👥"],
    ["mcp-admin-section", "MCP", capabilities.mcp, "🧩"],
    ["source-admin-section", "소스·색인", capabilities.source, "📚"],
    ["quality-admin-section", "검색 품질", capabilities.quality, "🎯"],
    ["security-admin-section", "보안·Secret", capabilities.security || capabilities.securityEvents, "🛡️"],
    ["audit-admin-section", "감사", capabilities.audit, "🧾"],
    ["database-admin-section", "데이터베이스", capabilities.status, "🗄️"],
    ["status-admin-section", "운영 상태", capabilities.status, "📊"],
    ["backup-admin-section", "백업·복구", capabilities.backup, "💾"],
  ].filter((entry) => entry[2]);
  $("#admin-menu").innerHTML =
    '<p class="side-nav-title">관리자</p>' +
    entries
      .map(([id, label, , icon]) => `<button type="button" data-admin-target="${id}"><span class="nav-icon">${icon}</span>${label}</button>`)
      .join("");
  const open = (target) => {
    document.querySelectorAll(".admin-panel").forEach((panel) => (panel.hidden = panel.id !== target));
    document.querySelectorAll("[data-admin-target]").forEach((button) => button.classList.toggle("active", button.dataset.adminTarget === target));
    if (target === "database-admin-section") refreshDatabase();
    if (target === "users-admin-section") refreshAdminUsers();
    rememberView({ panel: target });
  };
  openAdminPanel = (target) => {
    if (document.querySelector(`[data-admin-target="${target}"]`)) open(target);
  };
  document.querySelectorAll("[data-admin-target]").forEach(
    (button) => (button.onclick = () => open(button.dataset.adminTarget)),
  );
  if (entries.length) open(entries[0][0]);
}

let adminUserRoles = [];
async function refreshAdminUsers() {
  try {
    const result = (await api("/api/v1/admin/users")) || {};
    adminUserRoles = result.roles || [];
    $("#admin-user-roles").innerHTML =
      `<legend>플랫폼 역할</legend>${adminUserRoles.map((role) => `<label><input type="checkbox" name="admin-role" value="${esc(role)}" /> ${esc(role)}${role === "platform-admin" ? " (최고관리자)" : ""}</label>`).join("")}`;
    result.users = rows(result.users);
    $("#admin-users").innerHTML = result.users.length
      ? `<table><thead><tr><th>사용자</th><th>상태</th><th>역할</th><th>생성</th><th></th></tr></thead><tbody>${result.users.map((user) => `<tr><td>${esc(user.username)}<br><small>${esc(user.email || "")}<br>${esc(user.subject)}</small></td><td>${esc(user.status)}</td><td>${esc((user.roles || []).join(", "))}</td><td>${date(user.createdAt)}</td><td><button data-edit-user="${esc(user.id)}">수정</button> <button class="danger" data-delete-user="${esc(user.id)}">삭제</button></td></tr>`).join("")}</tbody></table>`
      : "등록된 사용자가 없습니다.";
    document.querySelectorAll("[data-edit-user]").forEach((button) => {
      button.onclick = () => openAdminUserForm(result.users.find((user) => user.id === button.dataset.editUser));
    });
    document.querySelectorAll("[data-delete-user]").forEach((button) => {
      button.onclick = async () => {
        if (!confirm("사용자를 삭제 상태로 전환하고 세션과 API 키를 폐기할까요?")) return;
        await api(`/api/v1/admin/users/${encodeURIComponent(button.dataset.deleteUser)}`, { method: "DELETE" });
        refreshAdminUsers();
      };
    });
    markEmptyTables();
  } catch (error) {
    $("#admin-users").innerHTML = `<div class="notice error">${esc(error.message)}</div>`;
  }
}
function openAdminUserForm(user = null) {
  $("#admin-user-form").hidden = false;
  $("#admin-user-id").value = user?.id || "";
  $("#admin-user-subject").value = user?.subject || "";
  $("#admin-user-subject").disabled = Boolean(user);
  $("#admin-user-username").value = user?.username || "";
  $("#admin-user-email").value = user?.email || "";
  $("#admin-user-status").value = user?.status === "disabled" ? "disabled" : "active";
  const roles = new Set(user?.roles || ["developer"]);
  document.querySelectorAll('[name="admin-role"]').forEach((input) => (input.checked = roles.has(input.value)));
}
async function refreshDatabase() {
  try {
    const status = await api("/api/v1/admin/database/status");
    const pool = status.pool || {};
    const migrations = status.migrations || {};
    $("#admin-database-status").innerHTML = [
      ["연결", `${status.status} · ${Number(status.latencyMs || 0).toFixed(1)}ms`],
      ["Driver / DB", `${status.driver}${status.database ? ` · ${status.database}` : ""}`],
      ["서버", status.serverVersion || "-"],
      ["접속 사용자", status.user || "-"],
      ["Connection Pool", `사용 ${pool.inUse || 0} · 유휴 ${pool.idle || 0} · 전체 ${pool.open || 0}`],
      ["Migration", `${migrations.count || 0}개 · ${migrations.latest || "없음"}`],
      ["기동 모드", status.recoveryMode ? "SQLite 복구 모드" : "정상 운영 모드"],
    ].map(([label, value]) => `<article><span>${esc(label)}</span><strong>${esc(value)}</strong></article>`).join("");
    if (status.recoveryMode && status.warning) {
      $("#database-action-result").hidden = false;
      $("#database-action-result").className = "notice error";
      $("#database-action-result").textContent = status.warning;
    }
  } catch (error) {
    $("#admin-database-status").innerHTML = `<div class="notice error">${esc(error.message)}</div>`;
  }
}
async function runDatabaseAction(action) {
  const result = $("#database-action-result");
  const dsn = $("#database-dsn").value.trim();
  if (!dsn) {
    result.hidden = false;
    result.className = "notice error";
    result.textContent = "PostgreSQL DSN을 입력하세요.";
    return;
  }
  const body = { dsn };
  if (action === "migrate") {
    body.confirm = $("#database-confirm").value.trim();
  }
  const button = action === "test" ? $("#test-database") : $("#migrate-database");
  button.disabled = true;
  try {
    const response = await api(`/api/v1/admin/database/${action}`, {
      method: "POST",
      body: JSON.stringify(body),
    });
    result.hidden = false;
    result.className = "notice ok";
    result.textContent = action === "test"
      ? `연결 성공: PostgreSQL ${response.serverVersion || ""} · ${response.database || ""} · ${Number(response.latencyMs || 0).toFixed(1)}ms`
      : "데이터 이전이 완료되었습니다. PostgreSQL 적용을 위해 서비스를 재시작하세요.";
    if (action === "migrate") {
      $("#database-dsn").value = "";
      $("#database-confirm").value = "";
    }
  } catch (error) {
    result.hidden = false;
    result.className = "notice error";
    result.textContent = error.message;
  } finally {
    button.disabled = false;
  }
}
const qualityCSV = (selector) =>
  $(selector)
    .value.split(",")
    .map((value) => value.trim())
    .filter(Boolean);
const contextPackItems = () =>
  $("#context-pack-items").value
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line) => {
      const [libraryId, ref = "", queryHint = ""] = line.split("|").map((value) => value.trim());
      return { libraryId, ref, queryHint };
    });
function resetContextPack() {
  $("#context-pack-admin-form").reset();
  $("#context-pack-id").value = "";
  $("#context-pack-enabled").checked = true;
}
async function saveContextPack(event, capabilities) {
  event.preventDefault();
  if (!capabilities.qualityWrite) return;
  const id = $("#context-pack-id").value;
  const body = {
    slug: $("#context-pack-slug").value,
    name: $("#context-pack-name").value,
    description: $("#context-pack-description").value,
    enabled: $("#context-pack-enabled").checked,
    items: contextPackItems(),
  };
  try {
    await api(id ? `/api/v1/admin/context-packs/${encodeURIComponent(id)}` : "/api/v1/admin/context-packs", {
      method: id ? "PUT" : "POST",
      body: JSON.stringify(body),
    });
    resetContextPack();
    showAdmin("Context Pack을 저장했습니다.", true);
    refreshContextPacks(capabilities);
  } catch (error) {
    showAdmin(error.message, false);
  }
}
async function refreshContextPacks(capabilities) {
  if (!capabilities.quality) return;
  try {
    const packs = rows(await api("/api/v1/admin/context-packs"));
    $("#context-pack-list").innerHTML =
      `<table><thead><tr><th>이름/Slug</th><th>Library</th><th>상태</th><th></th></tr></thead><tbody>${packs.map((pack) => `<tr><td>${esc(pack.name)}<br><code>${esc(pack.slug)}</code><br>${esc(pack.description || "")}</td><td>${(pack.items || []).map((item) => `<code>${esc(item.libraryId)}${item.ref ? `/${esc(item.ref)}` : ""}</code>${item.queryHint ? ` · ${esc(item.queryHint)}` : ""}`).join("<br>")}</td><td>${pack.enabled ? "활성" : "중지"}</td><td>${capabilities.qualityWrite ? `<button data-edit-pack="${esc(pack.id)}">수정</button> <button class="danger" data-delete-pack="${esc(pack.id)}">삭제</button>` : ""}</td></tr>`).join("")}</tbody></table>`;
    document.querySelectorAll("[data-edit-pack]").forEach((button) => {
      button.onclick = () => {
        const pack = packs.find((item) => item.id === button.dataset.editPack);
        $("#context-pack-id").value = pack.id;
        $("#context-pack-slug").value = pack.slug;
        $("#context-pack-name").value = pack.name;
        $("#context-pack-description").value = pack.description || "";
        $("#context-pack-enabled").checked = pack.enabled;
        $("#context-pack-items").value = (pack.items || []).map((item) => [item.libraryId, item.ref || "", item.queryHint || ""].join("|")).join("\n");
      };
    });
    document.querySelectorAll("[data-delete-pack]").forEach((button) => {
      button.onclick = async () => {
        if (!confirm("Context Pack을 삭제하시겠습니까?")) return;
        await api(`/api/v1/admin/context-packs/${encodeURIComponent(button.dataset.deletePack)}`, { method: "DELETE" });
        refreshContextPacks(capabilities);
      };
    });
    markEmptyTables();
  } catch (error) {
    $("#context-pack-list").textContent = error.message;
  }
}
async function createQualityCase(capabilities) {
  try {
    await api("/api/v1/admin/quality/cases", {
      method: "POST",
      body: JSON.stringify({
        name: $("#quality-name").value,
        libraryId: $("#quality-library").value,
        query: $("#quality-query").value,
        principals: qualityCSV("#quality-principals"),
        relevantSources: qualityCSV("#quality-sources"),
      }),
    });
    showAdmin("검색 품질 평가 사례를 추가했습니다.", true);
    refreshQuality(capabilities);
  } catch (error) {
    showAdmin(error.message, false);
  }
}
async function runQuality(capabilities) {
  try {
    const run = await api("/api/v1/admin/quality/runs", {
      method: "POST",
      body: JSON.stringify({
        topK: Number($("#quality-top-k").value),
        minimumRecall: Number($("#quality-min-recall").value),
        minimumMrr: Number($("#quality-min-mrr").value),
        minimumNdcg: Number($("#quality-min-ndcg").value),
      }),
    });
    showAdmin(
      `품질 평가 ${run.status}: Recall ${run.recallAtK.toFixed(3)}, MRR ${run.mrr.toFixed(3)}, nDCG ${run.ndcgAtK.toFixed(3)}`,
      run.status === "passed",
    );
    refreshQuality(capabilities);
  } catch (error) {
    showAdmin(error.message, false);
  }
}
async function refreshQuality(capabilities = activeCapabilities) {
  if (!capabilities.quality) return;
  try {
    const [cases, runs] = (
      await Promise.all([api("/api/v1/admin/quality/cases"), api("/api/v1/admin/quality/runs")])
    ).map(rows);
    $("#quality-cases").innerHTML =
      `<table><thead><tr><th>이름/Library</th><th>질의</th><th>ACL</th><th>정답 파일</th><th></th></tr></thead><tbody>${cases.map((item) => `<tr><td>${esc(item.name)}<br><code>${esc(item.libraryId)}</code></td><td>${esc(item.query)}</td><td>${item.principals.map(esc).join("<br>")}</td><td>${item.relevantSources.map(esc).join("<br>")}</td><td>${capabilities.qualityWrite ? `<button class="danger" data-delete-quality="${esc(item.id)}">삭제</button>` : ""}</td></tr>`).join("")}</tbody></table>`;
    $("#quality-runs").innerHTML =
      `<table><thead><tr><th>시각/상태</th><th>사례</th><th>Recall@K</th><th>MRR</th><th>nDCG@K</th></tr></thead><tbody>${runs.map((run) => `<tr data-quality-run="${esc(run.id)}"><td>${date(run.createdAt)}<br>${esc(run.status)}</td><td>${run.passedCount}/${run.caseCount}</td><td>${run.recallAtK.toFixed(3)}</td><td>${run.mrr.toFixed(3)}</td><td>${run.ndcgAtK.toFixed(3)}</td></tr>`).join("")}</tbody></table>`;
    document.querySelectorAll("[data-delete-quality]").forEach(
      (button) =>
        (button.onclick = async () => {
          if (!confirm("이 평가 사례를 삭제할까요?")) return;
          try {
            await api(
              `/api/v1/admin/quality/cases/${encodeURIComponent(button.dataset.deleteQuality)}`,
              { method: "DELETE" },
            );
            refreshQuality(capabilities);
          } catch (error) {
            showAdmin(error.message, false);
          }
        }),
    );
    document.querySelectorAll("[data-quality-run]").forEach(
      (row) =>
        (row.onclick = async () => {
          try {
            const results = rows(
              await api(`/api/v1/admin/quality/runs/${encodeURIComponent(row.dataset.qualityRun)}/results`),
            );
            $("#quality-results").innerHTML =
              `<table><thead><tr><th>사례</th><th>검색 결과</th><th>Recall/MRR/nDCG</th><th>시간/오류</th></tr></thead><tbody>${results.map((result) => `<tr><td>${esc(result.caseName)}</td><td>${result.retrievedSources.map(esc).join("<br>")}</td><td>${result.recallAtK.toFixed(3)} / ${result.reciprocalRank.toFixed(3)} / ${result.ndcgAtK.toFixed(3)}</td><td>${result.durationMs} ms<br>${esc(result.errorMessage)}</td></tr>`).join("")}</tbody></table>`;
          } catch (error) {
            showAdmin(error.message, false);
          }
        }),
    );
    markEmptyTables();
  } catch (error) {
    showAdmin(error.message, false);
  }
}
async function refreshBackups(capabilities = activeCapabilities) {
  if (!capabilities.backup) return;
  try {
    const records = rows(await api("/api/v1/admin/backups"));
    $("#backups").innerHTML =
      `<table><thead><tr><th>생성 시각</th><th>유형/상태</th><th>크기</th><th>SHA-256</th><th></th></tr></thead><tbody>${records.map((record) => `<tr><td>${date(record.createdAt)}<br><small>${esc(record.createdBy)}</small></td><td>${esc(record.triggerType)} / ${esc(record.status)}<br><small>${esc(record.errorMessage)}</small></td><td>${Math.ceil((record.sizeBytes || 0) / 1024)} KiB</td><td><code>${esc(record.sha256 || "-")}</code></td><td>${record.status === "completed" ? `<a class="button-link" href="/api/v1/admin/backups/${encodeURIComponent(record.id)}/download">다운로드</a> ${capabilities.backupWrite ? `<button class="danger" data-restore="${esc(record.id)}">복원</button>` : ""}` : ""}</td></tr>`).join("")}</tbody></table>`;
    document.querySelectorAll("[data-restore]").forEach(
      (button) =>
        (button.onclick = async () => {
          const id = button.dataset.restore;
          const confirmation = prompt(
            `복원하려면 정확히 입력하세요: RESTORE ${id}`,
          );
          if (confirmation !== `RESTORE ${id}`) return;
          try {
            await api(
              `/api/v1/admin/backups/${encodeURIComponent(id)}/restore`,
              {
                method: "POST",
                headers: {
                  "X-Restore-Confirmation": confirmation,
                },
              },
            );
            showAdmin(
              "백업 복원이 완료됐고 기존 세션이 폐기되었습니다. 다시 로그인해야 할 수 있습니다.",
              true,
            );
            refreshBackups(capabilities);
          } catch (e) {
            showAdmin(e.message, false);
          }
        }),
    );
    markEmptyTables();
  } catch (e) {
    showAdmin(e.message, false);
  }
}
async function discover() {
  try {
    const sourceType = $("#source-type").value,
      projectKey = $("#project-key").value.trim();
    const x = await api(`/api/v1/admin/sources/${sourceType}/discover`, {
      method: "POST",
      body: JSON.stringify({ projectKey }),
    });
    if (x.projects) {
      $("#discovery").innerHTML =
        `<table><thead><tr><th>Key/ID</th><th>프로젝트</th><th>설명</th></tr></thead><tbody>${x.projects.map((p) => `<tr><td>${esc(p.Key)}</td><td>${esc(p.Name)}</td><td>${esc(p.Description)}</td></tr>`).join("")}</tbody></table>`;
      return;
    }
    discovered = x.repositories || [];
    $("#discovery-actions").hidden = discovered.length === 0;
    $("#discovery").innerHTML =
      `<table><thead><tr><th><span class="sr-only">선택</span></th><th>저장소</th><th>기본 브랜치</th><th></th></tr></thead><tbody>${discovered.map((r, n) => `<tr><td><input type="checkbox" data-discovered="${n}" aria-label="${esc(r.ProjectKey)}/${esc(r.Slug)} 선택" /></td><td>${esc(r.ProjectKey)}/${esc(r.Slug)}<br><small>${esc(r.Description)}</small></td><td>${esc(r.DefaultBranch)}</td><td><button data-register="${n}">등록·색인</button></td></tr>`).join("")}</tbody></table>`;
    markEmptyTables();
    const updateSelection = () => {
      const selected = $$("[data-discovered]:checked").length;
      $("#discovery-selection").textContent = selected ? `${selected}개 선택됨` : "";
    };
    $$("[data-discovered]").forEach((box) => (box.onchange = updateSelection));
    updateSelection();
    $("#select-all-discovered").onclick = () => {
      const target = $$("[data-discovered]:checked").length !== discovered.length;
      $$("[data-discovered]").forEach((box) => (box.checked = target));
      updateSelection();
    };
    $("#register-selected").onclick = () => registerSelected(sourceType);
    document
      .querySelectorAll("[data-register]")
      .forEach(
        (b) =>
          (b.onclick = () =>
            registerRepository(
              sourceType,
              discovered[Number(b.dataset.register)],
            )),
      );
  } catch (e) {
    reportError(e, "소스 탐색");
  }
}
// registerSelected는 탐색 결과를 한 번에 등록합니다. 저장소가 수십 개인 최초
// 구축에서 한 건씩 누르는 부담을 없애고, 실패 건은 따로 보고합니다.
async function registerSelected(sourceType) {
  const selected = $$("[data-discovered]:checked").map((box) => discovered[Number(box.dataset.discovered)]);
  if (!selected.length) {
    toast("등록할 저장소를 선택하세요.", "error");
    return;
  }
  if (!confirm(`${selected.length}개 저장소를 등록하고 초기 색인 작업을 생성할까요?`)) return;
  const button = $("#register-selected");
  button.disabled = true;
  let registered = 0;
  const failures = [];
  for (const repository of selected) {
    try {
      await api("/api/v1/admin/repositories", {
        method: "POST",
        body: JSON.stringify({ sourceType, repository }),
      });
      registered++;
    } catch (error) {
      failures.push(`${repository.ProjectKey}/${repository.Slug}: ${error.message}`);
    }
  }
  button.disabled = false;
  showAdmin(
    failures.length
      ? `${registered}개 등록, ${failures.length}개 실패 — ${failures[0]}`
      : `${registered}개 저장소를 등록하고 초기 색인 작업을 생성했습니다.`,
    failures.length === 0,
  );
  refreshOps(activeCapabilities);
}
async function registerRepository(sourceType, repository) {
  try {
    await api("/api/v1/admin/repositories", {
      method: "POST",
      body: JSON.stringify({ sourceType, repository }),
    });
    showAdmin("저장소를 등록하고 초기 색인 작업을 생성했습니다.", true);
    refreshOps(activeCapabilities);
  } catch (e) {
    reportError(e, "저장소 등록");
  }
}
/* ---------------------------------------------------------------------------
 * MCP 운영 분석
 * 도구 설정과 실제 호출 통계를 한 화면에 둡니다. 예산·Timeout·캐시를 바꾸는
 * 근거(잘림 비율, p95, 빈 응답률)가 같은 표에 있어야 추측 대신 측정으로
 * 조정할 수 있습니다.
 * ------------------------------------------------------------------------- */
let mcpToolCatalog = [];
let mcpStatsByTool = {};
let mcpRecommendations = [];

const percent = (part, total) => (total > 0 ? Math.round((part / total) * 100) : 0);
const kb = (bytes) => `${Math.round((bytes || 0) / 1024)} KB`;

function renderMCPTools(tools, capabilities) {
  if (tools && tools.length) mcpToolCatalog = tools;
  const header = "<tr><th>도구</th><th>상태</th><th>호출</th><th>빈 응답</th><th>오류</th><th>p95 지연</th><th>Timeout</th><th>평균 응답</th><th>예산</th><th>잘림</th><th>Cache</th><th></th></tr>";
  const body = mcpToolCatalog
    .map((tool) => {
      const stat = mcpStatsByTool[tool.name] || {};
      const calls = stat.calls || 0;
      const truncated = percent(stat.truncated || 0, calls);
      return `<tr><td>${esc(tool.name)}<br><small>${esc(tool.description)}</small></td>
<td>${tool.enabled ? "활성" : "<strong>비활성</strong>"}</td>
<td>${calls}</td>
<td>${percent(stat.empty || 0, calls)}%</td>
<td>${percent(stat.errors || 0, calls)}%</td>
<td>${stat.p95LatencyMs || 0} ms</td>
<td>${tool.timeoutMs} ms</td>
<td>${kb(stat.averageResponseBytes)}</td>
<td>${kb(tool.effectiveResponseBytes)}${tool.maxResponseBytes ? "" : " <small>(기본)</small>"}</td>
<td>${truncated >= 20 ? `<strong>${truncated}%</strong>` : `${truncated}%`}</td>
<td>${tool.cacheSeconds} s</td>
<td><button data-tool="${esc(tool.name)}">설정</button></td></tr>`;
    })
    .join("");
  $("#mcp-tools").innerHTML = `<table><thead>${header}</thead><tbody>${body}</tbody></table>`;
  $$("[data-tool]").forEach(
    (button) =>
      (button.hidden = !capabilities.mcpWrite) ||
      (button.onclick = () => openMCPTool(button.dataset.tool, capabilities)),
  );
}

async function refreshMCPAnalytics(capabilities = activeCapabilities) {
  if (!capabilities.mcp) return;
  const window = $("#mcp-window").value;
  try {
    const result = (await api(`/api/v1/admin/mcp/analytics?window=${encodeURIComponent(window)}`)) || {};
    const summary = result.summary || {};
    mcpStatsByTool = {};
    for (const tool of rows(result.tools)) mcpStatsByTool[tool.tool] = tool;
    mcpRecommendations = rows(result.recommendations);
    const cards = [
      ["호출", summary.calls || 0, `세션 ${summary.sessions || 0}개`],
      ["성공", summary.success || 0, `${percent(summary.success, summary.calls)}%`],
      ["빈 응답", summary.empty || 0, `${percent(summary.empty, summary.calls)}% · 색인·ACL 점검 신호`],
      ["오류", summary.errors || 0, `${percent(summary.errors, summary.calls)}%`],
      ["캐시 적중", summary.cacheHits || 0, `${percent(summary.cacheHits, summary.calls)}%`],
      ["예산 초과", summary.truncated || 0, `${percent(summary.truncated, summary.calls)}% 잘림`],
      ["평균 지연", `${Math.round(summary.averageLatencyMs || 0)} ms`, ""],
      ["평균 응답", kb(summary.averageResponseBytes), "에이전트 컨텍스트 소모량"],
    ];
    $("#mcp-summary").innerHTML = cards
      .map(([label, value, hint]) => `<article><span>${esc(label)}</span><strong>${esc(String(value))}</strong><span>${esc(hint)}</span></article>`)
      .join("");
    $("#mcp-recommendations").innerHTML = mcpRecommendations.length
      ? mcpRecommendations
          .map(
            (item, index) =>
              `<div class="advice ${esc(item.severity)}"><div><strong>${esc(item.tool)}</strong> · ${esc(item.message)}</div>${item.field ? `<button class="secondary" data-advice="${index}">설정 열기</button>` : ""}</div>`,
          )
          .join("")
      : '<div class="notice ok">현재 기간에는 조치가 필요한 지표가 없습니다.</div>';
    $$("[data-advice]").forEach(
      (button) =>
        (button.hidden = !capabilities.mcpWrite) ||
        (button.onclick = () => {
          const advice = mcpRecommendations[Number(button.dataset.advice)];
          openMCPTool(advice.tool, capabilities, advice);
        }),
    );
    const unanswered = rows(result.unanswered);
    $("#mcp-unanswered").innerHTML = unanswered.length
      ? `<table><thead><tr><th>도구</th><th>질의</th><th>횟수</th></tr></thead><tbody>${unanswered.map((item) => `<tr><td>${esc(item.tool)}</td><td><code>${esc(item.arguments)}</code></td><td>${item.calls}</td></tr>`).join("")}</tbody></table>`
      : '<p class="field-help">빈 응답으로 끝난 질문이 없습니다.</p>';
    const section = (title, list) =>
      `<h4>${title}</h4><table><tbody>${(list || []).map((item) => `<tr><td>${esc(item.label)}</td><td>${item.calls}</td></tr>`).join("") || "<tr><td>기록 없음</td><td>-</td></tr>"}</tbody></table>`;
    $("#mcp-breakdown").innerHTML =
      section("클라이언트", rows(result.clients)) +
      section("검색 경로", rows(result.retrieval)) +
      section("오류 코드", rows(result.errors)) +
      section("Library", rows(result.libraries));
    const timeline = rows(result.timeline);
    const peak = Math.max(1, ...timeline.map((item) => item.calls));
    $("#mcp-timeline").innerHTML = timeline.length
      ? `<table><tbody>${timeline
          .map(
            (item) =>
              `<tr><td>${esc(item.bucket)}</td><td><span class="spark" style="width:${Math.max(2, Math.round((item.calls / peak) * 240))}px"></span></td><td>${item.calls}건${item.errors ? ` · 오류 ${item.errors}` : ""}${item.empty ? ` · 빈 응답 ${item.empty}` : ""}</td></tr>`,
          )
          .join("")}</tbody></table>`
      : '<p class="field-help">이 기간에 기록된 호출이 없습니다.</p>';
    renderMCPTools(mcpToolCatalog, capabilities);
  } catch (error) {
    reportError(error, "MCP 통계");
  }
}

async function refreshMCPSessions(capabilities = activeCapabilities) {
  if (!capabilities.mcpAudit) return;
  try {
    const result = (await api(`/api/v1/admin/mcp/sessions?window=${encodeURIComponent($("#mcp-window").value)}`)) || {};
    const sessions = rows(result.sessions);
    $("#mcp-sessions").innerHTML = sessions.length
      ? `<table><thead><tr><th>세션</th><th>클라이언트</th><th>호출 흐름</th><th>결과</th><th>소요/응답</th><th></th></tr></thead><tbody>${sessions
          .map(
            (item) =>
              `<tr><td>${esc(item.sessionId)}<br><small>${date(item.lastCallAt)}</small></td>
<td>${esc(item.client || "unknown")}<br><small>${esc(item.userId)}</small></td>
<td>${item.toolChain.map((tool) => esc(tool)).join(" → ") || "-"}</td>
<td>${item.unresolved ? '<span class="state failed">답 없이 종료</span>' : '<span class="state ok">해결</span>'}<br><small>성공 ${item.success} · 빈 ${item.empty} · 오류 ${item.errors}</small></td>
<td>${item.durationMs} ms<br><small>${kb(item.responseBytes)}</small></td>
<td>${item.lastCallId ? `<button class="secondary" data-session-trace="${esc(item.lastCallId)}">마지막 호출 X-ray</button>` : ""}</td></tr>`,
          )
          .join("")}</tbody></table>`
      : '<p class="field-help">이 기간에 기록된 세션이 없습니다.</p>';
    $$("[data-session-trace]").forEach((button) => (button.onclick = () => openCallTrace(button.dataset.sessionTrace)));
  } catch (error) {
    reportError(error, "MCP 세션");
  }
}

// runSelfCheck는 저장된 설정이 아니라 실제 검색 경로를 실행합니다. 각 점검은
// 호출 X-ray와 같은 단계 기록을 반환하므로, 실패했을 때 어디서 멈췄는지 같은
// 화면에서 바로 읽을 수 있습니다.
async function runSelfCheck() {
  const output = $("#selfcheck-result");
  output.innerHTML = '<p class="field-help">실제 검색을 실행하는 중…</p>';
  try {
    const result = await api("/api/v1/admin/mcp/selfcheck", {
      method: "POST",
      body: JSON.stringify({ query: $("#selfcheck-query").value.trim() || "README" }),
    });
    const verdict = { ok: ["ok", "정상"], warn: ["warn", "확인 필요"], fail: ["error", "실패"] }[result.verdict] || ["warn", result.verdict];
    output.innerHTML =
      `<div class="notice ${verdict[0]}">자가 점검 ${esc(verdict[1])} · 질의 "${esc(result.query)}" · ${result.durationMs} ms</div>` +
      `<table><thead><tr><th>점검</th><th>결과</th><th>설명</th><th>단계</th></tr></thead><tbody>${rows(result.checks)
        .map(
          (check) =>
            `<tr><td>${esc(check.name)}</td>
<td><span class="state ${check.status === "fail" ? "error" : check.status}">${esc(check.status)}</span><br><small>${check.durationMs || 0} ms</small></td>
<td>${esc(check.detail || "-")}${check.action ? `<br><small>${esc(check.action)}</small>` : ""}</td>
<td>${rows(check.steps).map((step) => `${esc(step.stage)}${step.target ? `(${esc(step.target)})` : ""} ${step.candidates}→${step.results}`).join("<br>") || "-"}</td></tr>`,
        )
        .join("")}</tbody></table>`;
  } catch (error) {
    output.innerHTML = `<div class="notice error">${esc(error.message)}</div>`;
  }
}

async function refreshMCPCalls(capabilities = activeCapabilities) {
  if (!capabilities.mcpAudit) return;
  try {
    const result = (await api(`/api/v1/admin/mcp/calls?${mcpCallQuery()}`)) || {};
    const items = rows(result.items);
    $("#mcp-calls").innerHTML = items.length
      ? `<p class="field-help">${result.total}건 중 ${items.length}건 표시</p><table><thead><tr><th>시각</th><th>도구</th><th>결과</th><th>질의</th><th>지연/응답</th><th>클라이언트·세션</th><th></th></tr></thead><tbody>${items
          .map(
            (item) =>
              `<tr><td>${date(item.occurredAt)}<br><small>${esc(item.clientIp)}</small></td>
<td>${esc(item.tool)}<br><small>${esc(item.libraryId || "-")}</small></td>
<td><span class="state ${esc(item.outcome)}">${esc(item.outcome)}</span>${item.errorCode ? `<br><small>${esc(item.errorCode)}</small>` : ""}${item.retrievalMode ? `<br><small>${esc(item.retrievalMode)}</small>` : ""}</td>
<td><code>${esc(item.arguments || "-")}</code><br><small>결과 ${item.resultCount}건${item.cacheHit ? " · 캐시" : ""}</small></td>
<td>${item.durationMs} ms<br><small>${kb(item.responseBytes)}${item.truncated ? " · 잘림" : ""}</small></td>
<td>${esc(item.client || "unknown")}<br><small>${esc(item.sessionId || "-")} · ${esc(item.apiKeyPrefix || "session")}</small></td>
<td><button class="secondary" data-trace="${esc(item.id)}">X-ray</button></td></tr>`,
          )
          .join("")}</tbody></table>`
      : '<p class="field-help">조건에 해당하는 호출이 없습니다.</p>';
    $$("[data-trace]").forEach((button) => (button.onclick = () => openCallTrace(button.dataset.trace)));
  } catch (error) {
    reportError(error, "MCP 호출 감사");
  }
}

// openCallTrace는 호출 하나를 단계별로 펼칩니다. 어느 단계가 몇 건을 보고 몇 건을
// 넘겼는지, 그리고 같은 세션에서 그 앞뒤로 무엇을 했는지까지 함께 보여 주어야
// "왜 이 답이 나왔는가"를 추적할 수 있습니다.
async function openCallTrace(id, personal = false) {
  const dialog = $("#mcp-trace-dialog");
  try {
    const base = personal ? "/api/v1/me/calls/" : "/api/v1/admin/mcp/calls/";
    const result = await api(base + encodeURIComponent(id));
    const call = result.call || {};
    $("#mcp-trace-title").textContent = `${call.tool} · ${date(call.occurredAt)}`;
    $("#mcp-trace-summary").className = `notice ${call.outcome === "error" ? "error" : call.outcome === "empty" ? "warn" : "ok"}`;
    $("#mcp-trace-summary").textContent =
      `${call.outcome} · 결과 ${call.resultCount}건 · ${call.durationMs} ms (추적 ${call.tracedMs} ms, 그 외 ${call.untracedMs} ms) · ${kb(call.responseBytes)}${call.truncated ? " (예산 초과로 잘림)" : ""} · 경로 ${call.retrievalMode || "-"}` +
      (call.traceSummary ? ` · ${call.traceSummary}` : "") +
      `\n질의: ${call.arguments || "-"} · 클라이언트 ${call.client || "unknown"} · 세션 ${call.sessionId || "-"} · 요청 ${call.requestId || "-"}`;
    const steps = rows(result.steps);
    const slowest = Math.max(1, ...steps.map((step) => step.durationMs));
    $("#mcp-trace-steps").innerHTML = steps.length
      ? `<table><thead><tr><th>#</th><th>단계</th><th>대상</th><th>상태</th><th>후보→통과</th><th>소요</th><th>설명</th></tr></thead><tbody>${steps
          .map(
            (step) =>
              `<tr><td>${step.sequence}</td><td>${esc(step.stage)}</td><td>${esc(step.target || "-")}</td>
<td><span class="state ${esc(step.status)}">${esc(step.status)}</span></td>
<td>${step.candidates} → ${step.results}${step.candidates > 0 && step.results === 0 ? " <strong>(전부 탈락)</strong>" : ""}</td>
<td><span class="spark" style="width:${Math.max(2, Math.round((step.durationMs / slowest) * 120))}px"></span> ${step.durationMs} ms<br><small>+${step.offsetMs} ms</small></td>
<td>${esc(step.detail || "-")}</td></tr>`,
          )
          .join("")}</tbody></table>`
      : '<p class="field-help">이 호출에는 기록된 단계가 없습니다. 캐시에서 응답했거나 추적 이전 버전에서 기록된 호출입니다.</p>';
    const sequence = rows(result.sessionSequence);
    $("#mcp-trace-sequence").innerHTML = sequence.length
      ? `<table><thead><tr><th>시각</th><th>도구</th><th>결과</th><th>질의</th></tr></thead><tbody>${sequence
          .map(
            (item) =>
              `<tr${item.current ? ' class="active"' : ""}><td>${date(item.occurredAt)}</td><td>${esc(item.tool)}${item.current ? " ←" : ""}</td>
<td><span class="state ${esc(item.outcome)}">${esc(item.outcome)}</span> ${item.resultCount}건 · ${item.durationMs} ms${item.errorCode ? ` · ${esc(item.errorCode)}` : ""}</td>
<td><code>${esc(item.arguments || "-")}</code></td></tr>`,
          )
          .join("")}</tbody></table>`
      : '<p class="field-help">세션 정보가 없는 호출입니다.</p>';
    dialog.showModal();
  } catch (error) {
    reportError(error, "호출 X-ray");
  }
}

function mcpCallQuery() {
  const form = new FormData($("#mcp-calls-filter"));
  const params = new URLSearchParams({ window: $("#mcp-window").value, limit: "100" });
  for (const [key, value] of form.entries()) if (String(value).trim()) params.set(key, String(value).trim());
  return params.toString();
}

// openMCPTool은 도구 하나의 설정과 그 근거가 되는 지표를 함께 보여 줍니다.
// advice가 있으면 권장값을 미리 채워, 관리자가 숫자를 옮겨 적지 않아도 됩니다.
let activeMCPTool = null;
function openMCPTool(name, capabilities, advice) {
  const tool = mcpToolCatalog.find((item) => item.name === name);
  if (!tool) return;
  activeMCPTool = { name, capabilities };
  const stat = mcpStatsByTool[name] || {};
  $("#mcp-tool-title").textContent = `${name} 설정`;
  $("#mcp-tool-description").textContent = tool.description || "";
  $("#mcp-tool-stats").textContent = stat.calls
    ? `최근 ${$("#mcp-window").value}: 호출 ${stat.calls}건 · 빈 응답 ${percent(stat.empty, stat.calls)}% · 오류 ${percent(stat.errors, stat.calls)}% · p50/p95 ${stat.p50LatencyMs}/${stat.p95LatencyMs} ms · 평균 응답 ${kb(stat.averageResponseBytes)} · 잘림 ${percent(stat.truncated, stat.calls)}%`
    : "이 기간에 기록된 호출이 없습니다.";
  $("#mcp-tool-enabled").checked = tool.enabled !== false;
  $("#mcp-tool-timeout").value = tool.timeoutMs || 30000;
  $("#mcp-tool-cache").value = tool.cacheSeconds || 0;
  $("#mcp-tool-budget").value = tool.maxResponseBytes || 0;
  const suggestion = advice || mcpRecommendations.find((item) => item.tool === name && item.field);
  $("#mcp-tool-advice").hidden = !suggestion?.field;
  $("#mcp-tool-advice").className = `advice ${suggestion?.severity || ""}`;
  $("#mcp-tool-advice-text").textContent = suggestion?.message || "";
  $("#mcp-tool-apply").onclick = () => {
    const field = { maxResponseBytes: "#mcp-tool-budget", timeoutMs: "#mcp-tool-timeout", cacheSeconds: "#mcp-tool-cache" }[suggestion?.field];
    if (field) $(field).value = suggestion.value;
  };
  $("#mcp-tool-dialog").showModal();
}

function setupMCPAdmin(capabilities) {
  if (!capabilities.mcp) return;
  // 감사 로그는 사용자 식별자와 질의 원문을 담고 있어, 권한이 없는 역할에게는
  // 화면 자체를 보여 주지 않습니다. 숨기지 않으면 조회 때마다 403 이 납니다.
  $("#mcp-audit-block").hidden = !capabilities.mcpAudit;
  const refreshAll = () => {
    refreshMCPAnalytics(capabilities);
    refreshMCPCalls(capabilities);
    refreshMCPSessions(capabilities);
  };
  $("#refresh-mcp-analytics").onclick = refreshAll;
  $("#mcp-window").onchange = refreshAll;
  // 서버는 mcp-admin·source-admin·search-admin 에게 허용하므로, 화면도 같은 범위로
  // 맞춥니다. 더 좁게 숨기면 권한이 있는 관리자가 버튼을 못 찾습니다.
  $("#run-selfcheck").hidden = !(capabilities.mcpWrite || capabilities.sourceWrite || capabilities.qualityWrite);
  $("#run-selfcheck").onclick = runSelfCheck;
  $("#mcp-calls-filter").onsubmit = (event) => {
    event.preventDefault();
    refreshMCPCalls(capabilities);
  };
  $("#mcp-calls-export").onclick = () => {
    // 브라우저가 세션 쿠키로 직접 내려받게 해서, 큰 CSV를 메모리에 담지 않습니다.
    location.href = `/api/v1/admin/mcp/calls?${mcpCallQuery()}&limit=1000&format=csv`;
  };
  $("#mcp-trace-close").onclick = () => $("#mcp-trace-dialog").close();
  $("#mcp-trace-dismiss").onclick = () => $("#mcp-trace-dialog").close();
  const dialog = $("#mcp-tool-dialog");
  $("#mcp-tool-close").onclick = () => dialog.close();
  $("#mcp-tool-cancel").onclick = () => dialog.close();
  $("#mcp-tool-defaults").onclick = () => {
    $("#mcp-tool-enabled").checked = true;
    $("#mcp-tool-timeout").value = 30000;
    $("#mcp-tool-cache").value = 0;
    $("#mcp-tool-budget").value = 0;
  };
  $("#mcp-tool-form").onsubmit = async (event) => {
    event.preventDefault();
    if (!activeMCPTool) return;
    try {
      await api(`/api/v1/admin/mcp/tools/${encodeURIComponent(activeMCPTool.name)}`, {
        method: "PUT",
        body: JSON.stringify({
          enabled: $("#mcp-tool-enabled").checked,
          timeoutMs: Number($("#mcp-tool-timeout").value),
          cacheSeconds: Number($("#mcp-tool-cache").value),
          maxResponseBytes: Number($("#mcp-tool-budget").value),
        }),
      });
      dialog.close();
      showAdmin(`${activeMCPTool.name} 설정을 저장했습니다.`, true);
      refreshOps(activeMCPTool.capabilities);
      refreshMCPAnalytics(activeMCPTool.capabilities);
    } catch (error) {
      showAdmin(error.message, false);
    }
  };
  refreshAll();
}
// refreshSourceHealth 는 소스 서버 연동 상태(서킷 브레이커)를 보여 줍니다. 검색이
// 비는 이유가 "결과 없음"인지 "지금은 호출을 멈춘 상태"인지 구분할 수 있어야
// 합니다.
async function refreshSourceHealth(capabilities = activeCapabilities) {
  if (!capabilities.source) return;
  const target = $("#source-health");
  try {
    const result = (await api("/api/v1/admin/source-health")) || {};
    const sources = rows(result.sources);
    target.innerHTML = sources.length
      ? `<table><thead><tr><th>소스</th><th>상태</th><th>설명</th><th></th></tr></thead><tbody>${sources
          .map(
            (item) =>
              `<tr><td>${esc(item.source)}</td>
<td><span class="state ${item.healthy ? "ok" : item.state === "open" ? "error" : "warn"}">${esc(item.state)}</span>${item.failures ? `<br><small>연속 실패 ${item.failures}회</small>` : ""}</td>
<td>${esc(item.detail || "")}</td>
<td>${item.healthy ? "" : `<button data-source-reset="${esc(item.source)}">지금 재시도</button>`}</td></tr>`,
          )
          .join("")}</tbody></table>`
      : '<p class="field-help">등록된 소스 연동이 없습니다.</p>';
    $$("[data-source-reset]").forEach(
      (button) =>
        (button.hidden = !capabilities.sourceWrite) ||
        (button.onclick = async () => {
          try {
            await api(`/api/v1/admin/source-health/${encodeURIComponent(button.dataset.sourceReset)}/reset`, { method: "POST" });
            showAdmin(`${button.dataset.sourceReset} 연동을 다시 호출하도록 초기화했습니다.`, true);
            refreshSourceHealth(capabilities);
          } catch (error) {
            showAdmin(error.message, false);
          }
        }),
    );
  } catch (error) {
    target.innerHTML = `<div class="notice error">${esc(error.message)}</div>`;
  }
}

async function refreshOps(capabilities = activeCapabilities) {
  try {
    const tools = capabilities.mcp ? rows(await api("/api/v1/admin/mcp/tools")) : [];
    let [repos, jobs, freshness] = capabilities.source
      ? await Promise.all([
          api("/api/v1/admin/repositories"),
          api("/api/v1/admin/index-jobs"),
          api("/api/v1/admin/freshness"),
        ])
      : [[], [], { repositories: [], staleCount: 0, sloMinutes: 0 }];
    [repos, jobs] = [rows(repos), rows(jobs)];
    freshness = freshness || { repositories: [], staleCount: 0, sloMinutes: 0 };
    renderMCPTools(tools, capabilities);
    $("#repositories").innerHTML =
      `<table><thead><tr><th>소스</th><th>Library ID</th><th>기본 브랜치</th><th>마지막 색인</th><th></th></tr></thead><tbody>${repos.map((r) => `<tr><td>${esc(r.sourceType)}</td><td>${esc(r.libraryId)}</td><td>${esc(r.defaultBranch)}</td><td>${date(r.indexedAt)}</td><td><button class="secondary" data-policy="${esc(r.id)}" data-policy-label="${esc(r.libraryId)}">색인 정책</button> <button data-index="${esc(r.id)}">재색인</button></td></tr>`).join("")}</tbody></table>`;
    $$("[data-policy]").forEach(
      (button) =>
        (button.hidden = !capabilities.sourceWrite) ||
        (button.onclick = () => openIndexPolicy(button.dataset.policy, button.dataset.policyLabel)),
    );
    $("#freshness").innerHTML =
      `<div class="notice ${freshness.staleCount ? "error" : "ok"}">SLO ${freshness.sloMinutes}분 · 지연/미색인 ${freshness.staleCount}/${freshness.repositoryCount}</div><table><thead><tr><th>소스/Library</th><th>Ref/Commit</th><th>마지막 색인</th><th>지연</th><th>상태</th></tr></thead><tbody>${(freshness.repositories || []).map((item) => `<tr><td>${esc(item.sourceType)}<br><code>${esc(item.libraryId)}</code></td><td>${esc(item.ref)}<br><small>${esc(item.commitId || "-")}</small></td><td>${date(item.indexedAt?.Time || item.indexedAt)}</td><td>${item.ageMinutes < 0 ? "-" : `${item.ageMinutes}분`}</td><td><span class="state ${esc(item.status)}">${esc(item.status)}</span></td></tr>`).join("")}</tbody></table>`;
    document.querySelectorAll("[data-index]").forEach(
      (b) =>
        (b.hidden = !capabilities.sourceWrite) ||
        (b.onclick = async () => {
          await api(
            `/api/v1/admin/repositories/${encodeURIComponent(b.dataset.index)}/index`,
            { method: "POST", body: "{}" },
          );
          refreshOps(capabilities);
        }),
    );
    $("#jobs").innerHTML =
      `<table><thead><tr><th>저장소/ref</th><th>상태</th><th>시도</th><th>파일</th><th>오류</th><th></th></tr></thead><tbody>${jobs
        .slice(0, 100)
        .map(
          (j) =>
            `<tr><td>${esc(j.repositoryId)}<br>${esc(j.refName)}</td><td><span class="state ${esc(j.status)}">${esc(j.status)}</span></td><td>${j.attempts}</td><td>${j.filesProcessed}</td><td>${esc(j.error)}</td><td>${j.status === "failed" ? `<button data-retry="${esc(j.id)}">재시도</button>` : ""}</td></tr>`,
        )
        .join("")}</tbody></table>`;
    document.querySelectorAll("[data-retry]").forEach(
      (b) =>
        (b.hidden = !capabilities.sourceWrite) ||
        (b.onclick = async () => {
          await api(
            `/api/v1/admin/index-jobs/${encodeURIComponent(b.dataset.retry)}/retry`,
            { method: "POST" },
          );
          refreshOps(capabilities);
        }),
    );
    markEmptyTables();
    refreshIndexDiagnostics(capabilities);
    scheduleOpsRefresh(jobs, capabilities);
  } catch (e) {
    showAdmin(e.message, false);
  }
}

const indexStateLabels = {
  indexed: ["검색 가능", "ok"],
  partial: ["일부 누락", "warn"],
  indexing: ["색인 중", "warn"],
  queued: ["대기 중", "warn"],
  "source-paused": ["연동 대기", "warn"],
  stalled: ["정체", "error"],
  failed: ["실패", "error"],
  empty: ["색인 0건", "error"],
  "never-run": ["작업 없음", "error"],
};

// refreshIndexDiagnostics는 "왜 미색인인지"를 저장소별로 보여 줍니다. 상태 문자열만
// 보여 주면 대기·정체·정책 불일치·엔드포인트 오류를 구분할 수 없습니다.
async function refreshIndexDiagnostics(capabilities = activeCapabilities) {
  if (!capabilities.source) return;
  try {
    const result = await api("/api/v1/admin/index-diagnostics");
    const queue = result.queue || {};
    const blocked = rows(result.repositories).filter((item) =>
      ["failed", "stalled", "empty", "never-run"].includes(item.state),
    ).length;
    const banner = $("#index-queue");
    banner.className = `notice ${blocked ? "error" : queue.running || queue.pending ? "warn" : "ok"}`;
    banner.textContent = `대기 ${queue.pending || 0} · 실행 중 ${queue.running || 0} · 실패 ${queue.failed || 0} · 조치 필요 저장소 ${blocked}개`;
    $("#index-diagnostics").innerHTML =
      `<table><thead><tr><th>Library</th><th>상태</th><th>내용</th><th>원인과 조치</th><th></th></tr></thead><tbody>${rows(result.repositories)
        .map((item) => {
          const [label, tone] = indexStateLabels[item.state] || [item.state, ""];
          return `<tr><td><code>${esc(item.libraryId)}</code><br><small>${esc(item.sourceType)} · ${esc(item.defaultBranch)}</small></td><td><span class="state ${esc(tone)}">${esc(label)}</span></td><td>청크 ${item.chunks} · 심볼 ${item.symbols}<br><small>${esc(item.commitId ? item.commitId.slice(0, 12) : "-")}</small></td><td>${esc(item.detail)}${item.action ? `<br><small>${esc(item.action)}</small>` : ""}</td><td><button data-index="${esc(item.repositoryId)}">재색인</button></td></tr>`;
        })
        .join("")}</tbody></table>`;
    // 재색인 버튼은 등록 저장소 표와 동일한 핸들러를 사용합니다.
    $$("#index-diagnostics [data-index]").forEach(
      (button) =>
        (button.hidden = !capabilities.sourceWrite) ||
        (button.onclick = async () => {
          try {
            await api(`/api/v1/admin/repositories/${encodeURIComponent(button.dataset.index)}/index`, { method: "POST", body: "{}" });
            toast("재색인 작업을 생성했습니다.", "ok");
            refreshOps(capabilities);
          } catch (error) {
            reportError(error, "재색인");
          }
        }),
    );
    markEmptyTables();
  } catch (error) {
    $("#index-diagnostics").innerHTML = `<div class="notice error">${esc(error.message)}</div>`;
  }
}

// 색인 작업이 대기·실행 중일 때만 화면을 주기적으로 갱신합니다. 작업이 끝나면
// 타이머를 멈춰 유휴 상태에서 불필요한 요청을 만들지 않습니다.
let opsRefreshTimer = null;
function scheduleOpsRefresh(jobs, capabilities) {
  clearTimeout(opsRefreshTimer);
  const running = jobs.some((job) => job.status === "pending" || job.status === "running");
  if (!running || $("#source-admin-section").hidden) return;
  opsRefreshTimer = setTimeout(() => refreshOps(capabilities), 5000);
}

// openIndexPolicy는 저장소별 색인 대상 확장자와 제외 경로를 편집합니다. 기본
// 정책에 없는 언어를 쓰는 저장소가 "완료됐는데 0건"이 되는 상황을 이 화면에서
// 바로 해결할 수 있습니다.
let activeIndexPolicy = null;
async function openIndexPolicy(repositoryId, label) {
  const dialog = $("#index-policy-dialog");
  activeIndexPolicy = repositoryId;
  $("#index-policy-description").textContent = `${label} · 저장 후 즉시 재색인 작업을 생성합니다.`;
  try {
    const policy = await api(`/api/v1/admin/repositories/${encodeURIComponent(repositoryId)}/policy`);
    $("#index-policy-extensions").value = (policy.includeExtensions || []).join(",");
    $("#index-policy-excludes").value = (policy.excludePrefixes || []).join(",");
    $("#index-policy-max-bytes").value = policy.maxFileBytes || 1048576;
    dialog.showModal();
  } catch (error) {
    reportError(error, "색인 정책");
  }
}

function setupIndexPolicyDialog() {
  const dialog = $("#index-policy-dialog");
  $("#index-policy-close").onclick = () => dialog.close();
  $("#index-policy-cancel").onclick = () => dialog.close();
  $("#index-policy-defaults").onclick = async () => {
    // 서버 기본 정책은 등록되지 않은 저장소를 조회하면 그대로 반환됩니다.
    try {
      const defaults = await api("/api/v1/admin/index-policy-defaults");
      $("#index-policy-extensions").value = (defaults.includeExtensions || []).join(",");
      $("#index-policy-excludes").value = (defaults.excludePrefixes || []).join(",");
      $("#index-policy-max-bytes").value = defaults.maxFileBytes || 1048576;
    } catch (error) {
      reportError(error, "기본 정책");
    }
  };
  dialog.querySelector("form").onsubmit = async (event) => {
    event.preventDefault();
    const list = (value) => value.split(",").map((item) => item.trim()).filter(Boolean);
    try {
      await api(`/api/v1/admin/repositories/${encodeURIComponent(activeIndexPolicy)}/policy`, {
        method: "PUT",
        body: JSON.stringify({
          includeExtensions: list($("#index-policy-extensions").value),
          excludePrefixes: list($("#index-policy-excludes").value),
          maxFileBytes: Number($("#index-policy-max-bytes").value),
        }),
      });
      await api(`/api/v1/admin/repositories/${encodeURIComponent(activeIndexPolicy)}/index`, { method: "POST", body: "{}" });
      dialog.close();
      showAdmin("색인 정책을 저장하고 재색인 작업을 생성했습니다.", true);
      refreshOps(activeCapabilities);
    } catch (error) {
      reportError(error, "색인 정책 저장");
    }
  };
}
async function refreshSecurity(capabilities = activeCapabilities) {
  try {
    const [health, rawKeys, rawEvents, rawAudits, rawSecrets, rawDeliveries] = await Promise.all([
      capabilities.status ? api("/api/v1/admin/health") : null,
      capabilities.security ? api("/api/v1/admin/api-keys") : [],
      capabilities.securityEvents ? api("/api/v1/admin/security-events") : [],
      capabilities.audit ? api("/api/v1/admin/audit-logs") : [],
      capabilities.security ? api("/api/v1/admin/secrets") : [],
      capabilities.security || capabilities.securityEvents
        ? api("/api/v1/admin/notification-deliveries")
        : [],
    ]);
    const [keys, events, audits, secrets, deliveries] = [rawKeys, rawEvents, rawAudits, rawSecrets, rawDeliveries].map(rows);
    if (health) {
      $("#system-health").textContent =
        `저장소 ${health.repositories} · 청크 ${health.chunks} · 활성 키 ${health.activeApiKeys} · 대기 ${health.indexJobs.pending} · 실패 ${health.indexJobs.failed} · Trace ${health.observability?.tracingEnabled ? "활성" : "비활성"}`;
      $("#system-health").classList.add("ok");
    }
    $("#admin-keys").innerHTML =
      `<table><thead><tr><th>사용자/이름</th><th>Prefix / Scope</th><th>상태</th><th>마지막 사용</th><th></th></tr></thead><tbody>${keys.map((k) => `<tr><td>${esc(k.username)} / ${esc(k.name)}</td><td>${esc(k.prefix)}<br><small>${esc((k.scopes || []).join(", "))}</small></td><td>${esc(k.status)}</td><td>${date(k.lastUsedAt)}</td><td>${k.status === "active" || k.status === "disabled" ? `<button class="secondary" data-admin-scopes="${esc(k.id)}">Scope 편집</button> ` : ""}${k.status === "active" ? `<button class="danger" data-admin-revoke="${esc(k.id)}">강제 폐기</button>` : ""}</td></tr>`).join("")}</tbody></table>`;
    document.querySelectorAll("[data-admin-scopes]").forEach(
      (button) =>
        (button.onclick = () => {
          const key = keys.find((item) => item.id === button.dataset.adminScopes);
          if (key) openKeyScopeEditor(key, true, () => refreshSecurity(capabilities));
        }),
    );
    document.querySelectorAll("[data-admin-revoke]").forEach(
      (b) =>
        (b.onclick = async () => {
          await api(
            `/api/v1/admin/api-keys/${encodeURIComponent(b.dataset.adminRevoke)}/revoke`,
            { method: "POST" },
          );
          refreshSecurity(capabilities);
        }),
    );
    $("#managed-secrets").innerHTML =
      `<table><thead><tr><th>이름</th><th>Backend</th><th>버전</th><th>상태</th><th>갱신</th><th></th></tr></thead><tbody>${secrets.map((s) => `<tr><td><code>secret://${esc(s.name)}</code></td><td>${esc(s.backend)}</td><td>${s.version}</td><td>${esc(s.status)}</td><td>${date(s.updatedAt)}</td><td>${s.status === "active" ? `<button class="danger" data-secret-disable="${esc(s.name)}">중지</button>` : ""}</td></tr>`).join("")}</tbody></table>`;
    document.querySelectorAll("[data-secret-disable]").forEach(
      (button) =>
        (button.onclick = async () => {
          await api(
            `/api/v1/admin/secrets/${encodeURIComponent(button.dataset.secretDisable)}/disable`,
            { method: "POST" },
          );
          refreshSecurity(capabilities);
        }),
    );
    $("#security-events").innerHTML =
      `<table><thead><tr><th>시간</th><th>저장소/ref</th><th>파일</th><th>탐지/조치</th></tr></thead><tbody>${events.map((x) => `<tr><td>${date(x.occurredAt)}</td><td>${esc(x.repositoryId)} / ${esc(x.refName)}</td><td>${esc(x.filePath)}</td><td>${esc(x.findingType)} / ${esc(x.action)}</td></tr>`).join("")}</tbody></table>`;
    $("#notification-deliveries").innerHTML =
      `<table><thead><tr><th>시간</th><th>사용자/알림</th><th>채널</th><th>상태</th><th>시도</th><th>오류/다음 시도</th><th></th></tr></thead><tbody>${deliveries.map((x) => `<tr><td>${date(x.createdAt)}</td><td>${esc(x.username)}<br>${esc(x.title)}</td><td>${esc(x.channel)}</td><td><span class="state ${esc(x.status)}">${esc(x.status)}</span></td><td>${x.attempts}</td><td>${esc(x.lastError)}${x.status === "failed" ? `<br>${date(x.nextAttemptAt)}` : ""}</td><td>${["failed", "dead"].includes(x.status) && capabilities.security ? `<button data-delivery-retry="${esc(x.id)}">재시도</button>` : ""}</td></tr>`).join("")}</tbody></table>`;
    document.querySelectorAll("[data-delivery-retry]").forEach(
      (button) =>
        (button.onclick = async () => {
          await api(
            `/api/v1/admin/notification-deliveries/${encodeURIComponent(button.dataset.deliveryRetry)}/retry`,
            { method: "POST" },
          );
          refreshSecurity(capabilities);
        }),
    );
    $("#audit-logs").innerHTML =
      `<table><thead><tr><th>시간</th><th>수행자</th><th>행위</th><th>대상</th><th>결과</th></tr></thead><tbody>${audits.map((x) => `<tr><td>${date(x.at)}</td><td>${esc(x.actor)}</td><td>${esc(x.action)}</td><td>${esc(x.resourceType)} / ${esc(x.resourceId)}</td><td>${esc(x.outcome)}</td></tr>`).join("")}</tbody></table>`;
    markEmptyTables();
  } catch (e) {
    showAdmin(e.message, false);
  }
}
// showAdmin은 관리자 화면 상단 배너와 토스트를 함께 갱신합니다. 배너는 마지막
// 결과를 남겨 두고, 토스트는 다른 화면을 보고 있어도 결과를 알려 줍니다.
function showAdmin(text, ok) {
  const banner = $("#admin-result");
  banner.hidden = false;
  banner.textContent = text;
  banner.classList.toggle("ok", Boolean(ok));
  banner.classList.toggle("error", !ok);
  toast(text, ok ? "ok" : "error");
}
function esc(v) {
  return String(v ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
}
function date(v) {
  return v ? new Date(v).toLocaleString() : "-";
}
setupTheme();
setupGuides();
Promise.allSettled([loadBranding(), loadPublicStatus()]).finally(boot);
