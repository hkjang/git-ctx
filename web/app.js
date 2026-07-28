const $ = (s) => document.querySelector(s);
const categories = [
  "keycloak",
  "bitbucket",
  "gitlab",
  "mcp",
  "search",
  "model",
  "opensearch",
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
    ["pat", "Personal Access Token", "password", ""],
    ["username", "Username (PAT 미사용 시)", "text", ""],
    ["password", "Password", "password", ""],
    ["webhookSecret", "Webhook Secret", "password", ""],
    ["tlsVerify", "TLS 인증서 검증 사용", "boolean", true],
    ["caCertificate", "사내 CA PEM", "textarea", ""],
    ["proxyUrl", "Proxy URL", "url", ""],
    ["timeoutSeconds", "Timeout(초)", "number", 30],
  ],
  gitlab: [
    ["baseUrl", "GitLab Base URL", "url", ""],
    ["token", "Access Token", "password", ""],
    ["webhookSecret", "Webhook Secret", "password", ""],
    ["tlsVerify", "TLS 인증서 검증 사용", "boolean", true],
    ["caCertificate", "사내 CA PEM", "textarea", ""],
    ["proxyUrl", "Proxy URL", "url", ""],
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
  mcp: ["MCP", "Transport, Origin, 호출 제한"],
  search: ["검색", "키워드·벡터 가중치와 결과 수"],
  model: ["모델", "Embedding과 Reranker"],
  opensearch: ["OpenSearch", "BM25 projection과 인증"],
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
    $("#login").hidden = true;
    $("#bootstrap-login").hidden = true;
    $("#recovery-login").hidden = true;
    $("#logout").hidden = true;
    $("#profile-menu").hidden = false;
    $("#quick-nav-button").hidden = false;
    $("#profile-name").textContent = me.Username || "내 프로필";
    $("#profile-avatar").textContent = (me.Username || "U").slice(0, 1).toUpperCase();
    $("#identity").textContent =
      `${me.Username} · 역할: ${(me.Roles || []).join(", ")} · ACL: ${me.ACLPrincipal || "매핑되지 않음(Fail Closed)"}`;
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
}
function configureMCPKeyScopes(roles) {
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
function setupWorkspaceNavigation(hasAdmin) {
  $("#workspace-menu").hidden = false;
  $("#admin-workspace-button").hidden = !hasAdmin;
  setupPersonalNavigation();
  openWorkspaceView = (workspace) => {
    const admin = workspace === "admin" && hasAdmin;
    $("#personal-workspace").hidden = admin;
    $("#admin").hidden = !admin;
    document.querySelectorAll("[data-workspace]").forEach((button) => {
      const active = button.dataset.workspace === (admin ? "admin" : "personal");
      button.classList.toggle("active", active);
      button.setAttribute("aria-current", active ? "page" : "false");
    });
  };
  document.querySelectorAll("[data-workspace]").forEach(
    (button) => (button.onclick = () => openWorkspaceView(button.dataset.workspace)),
  );
  openWorkspaceView("personal");
}
function applyInitialNavigation(hasAdmin) {
  if ((location.pathname.startsWith("/admin") || location.hash === "#admin/keycloak") && hasAdmin) {
    openWorkspaceView("admin");
    document.querySelector('[data-admin-target="settings-admin"]')?.click();
    document.querySelector('[data-setting-tab="keycloak"]')?.click();
    if (location.hash) history.replaceState(null, "", location.pathname + location.search);
  } else if (location.pathname.startsWith("/admin") && !hasAdmin) {
    $("#status").textContent = "관리자 권한이 없어 관리자 화면에 접근할 수 없습니다.";
    $("#status").classList.remove("ok");
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
    const [notifications, repos, usage, calls] = await Promise.all([
      api("/api/v1/me/notifications"),
      api("/api/v1/me/repositories"),
      api("/api/v1/me/usage"),
      api("/api/v1/me/calls"),
    ]);
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
      `<table><thead><tr><th>도구</th><th>결과</th><th>호출</th><th>평균/최대 지연</th></tr></thead><tbody>${usage.map((x) => `<tr><td>${esc(x.tool)}</td><td>${esc(x.outcome)}</td><td>${x.calls}</td><td>${Math.round(x.averageLatencyMs)}/${Math.round(x.maximumLatencyMs)} ms</td></tr>`).join("")}</tbody></table>`;
    $("#my-calls").innerHTML =
      `<table><thead><tr><th>시간</th><th>키</th><th>도구</th><th>Library</th><th>결과/지연</th></tr></thead><tbody>${calls
        .slice(0, 100)
        .map(
          (x) =>
            `<tr><td>${date(x.occurredAt)}</td><td>${esc(x.apiKeyPrefix)}</td><td>${esc(x.tool)}</td><td>${esc(x.libraryId)}</td><td>${esc(x.outcome)} / ${x.durationMs}ms</td></tr>`,
        )
        .join("")}</tbody></table>`;
  } catch (e) {
    console.warn(e);
  }
}
async function loadKeys() {
  const keys = await api("/api/v1/me/api-keys");
  $("#key-list").innerHTML =
    `<table><thead><tr><th>이름</th><th>Prefix / 제한</th><th>상태</th><th>만료</th><th>마지막 사용</th><th></th></tr></thead><tbody>${keys.map((k) => `<tr><td>${esc(k.name)}</td><td>${esc(k.prefix)}<br><small>${esc((k.scopes || []).join(", "))}<br>${esc((k.restrictions?.allowedCidrs || []).join(", "))} ${esc((k.restrictions?.allowedRepositories || []).join(", "))}<br>분/시/일 ${k.restrictions?.ratePerMinute || 0}/${k.restrictions?.ratePerHour || 0}/${k.restrictions?.ratePerDay || 0}</small></td><td>${esc(k.status)}</td><td>${date(k.expiresAt)}</td><td>${date(k.lastUsedAt)}</td><td>${k.status === "active" ? `<button class="secondary" data-disable="${k.id}">중지</button> <button data-rotate="${k.id}">회전</button> <button class="danger" data-revoke="${k.id}">폐기</button>` : k.status === "disabled" ? `<button data-enable="${k.id}">재활성화</button> <button class="danger" data-revoke="${k.id}">폐기</button>` : ""}</td></tr>`).join("")}</tbody></table>`;
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
function renderSettingFields(category, value) {
  const fields = integrationSettingFields[category] || [];
  $("#setting-fields").hidden = fields.length === 0;
  $("#test-connection").hidden = ![
    "keycloak",
    "bitbucket",
    "gitlab",
    "model",
    "opensearch",
    "observability",
    "backup",
    "vault",
    "notifications",
  ].includes(category);
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
        return `<label data-field-key="${key}">${esc(label)}<input data-setting-key="${key}" data-setting-type="password" data-secret-stored="${stored}" type="password" value="${esc(shown)}" autocomplete="new-password" /><small class="field-help">${esc(help)}</small></label>`;
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
              ? Number(field.value)
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
  applyTLSFieldState();
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
    .map((category) => `<button type="button" role="tab" data-setting-tab="${category}">${esc(settingCategoryMeta[category]?.[0] || category)}</button>`)
    .join("");
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
    await loadCurrentSetting(category);
  };
  document.querySelectorAll("[data-setting-tab]").forEach(
    (tab) => (tab.onclick = () => selectCategory(tab.dataset.settingTab)),
  );
  $("#category").onchange = () => selectCategory($("#category").value);
  const loadCurrentSetting = async (category = $("#category").value) => {
    try {
      const x = await api(`/api/v1/admin/settings/${category}`);
      if ($("#category").value !== category) return;
      $("#setting-json").value = JSON.stringify(x.value, null, 2);
      renderSettingFields(category, x.value);
      const maskedCount = (x.maskedFields || []).length;
      $("#setting-load-status").className = "wide notice ok";
      $("#setting-load-status").textContent =
        `저장된 설정 v${x.version}을 불러왔습니다 · ${date(x.updatedAt)} · ${x.updatedBy || "알 수 없는 관리자"}${maskedCount ? ` · 비밀값 ${maskedCount}개 마스킹됨` : ""}`;
      $("#delete-setting").hidden = false;
      if (category === "keycloak") refreshKeycloakStatus();
    } catch (e) {
      if ($("#category").value !== category) return;
      if (e.status !== 404) {
        showAdmin(`설정을 불러오지 못했습니다: ${e.message}`, false);
        return;
      }
      const defaults = settingDefaults(category);
      $("#setting-json").value = JSON.stringify(defaults, null, 2);
      renderSettingFields(category, defaults);
      $("#setting-load-status").className = "wide notice";
      $("#setting-load-status").textContent =
        "저장된 설정이 없습니다. 기본값을 표시합니다.";
      $("#delete-setting").hidden = true;
      if (category === "keycloak") refreshKeycloakStatus();
    }
  };
  $("#test-connection").onclick = async () => {
    const category = $("#category").value;
    try {
      const x = await api(`/api/v1/admin/settings/${category}/test`, {
        method: "POST",
        body: $("#setting-json").value,
      });
      showAdmin(`${x.category} 연결 테스트와 검증에 성공했습니다.`, true);
    } catch (e) {
      showAdmin(e.message, false);
    }
  };
  $("#setting-json").oninput = () => {
    try {
      renderSettingFields(
        $("#category").value,
        JSON.parse($("#setting-json").value),
      );
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
      const x = await api(`/api/v1/admin/settings/${$("#category").value}`, {
        method: "PUT",
        body: $("#setting-json").value,
      });
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
      await loadCurrentSetting($("#category").value);
    } catch (e) {
      showAdmin(`저장하지 못했습니다: ${e.message}`, false);
    } finally {
      button.disabled = false;
      button.textContent = "저장";
    }
  };
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
  $("#refresh-ops").onclick = () => {
    refreshOps(capabilities);
    refreshSecurity(capabilities);
  };
  refreshOps(capabilities);
  refreshSecurity(capabilities);
  refreshBackups(capabilities);
  if (capabilities.quality) {
    $("#refresh-quality").onclick = () => refreshQuality(capabilities);
    refreshQuality(capabilities);
  }
  if (capabilities.qualityWrite) {
    $("#create-quality-case").onclick = () => createQualityCase(capabilities);
    $("#run-quality").onclick = () => runQuality(capabilities);
  }
  setupAdminNavigation(capabilities);
}
function setupAdminNavigation(capabilities) {
  const entries = [
    ["settings-admin", "설정", capabilities.settings],
    ["users-admin-section", "사용자", capabilities.users],
    ["mcp-admin-section", "MCP", capabilities.mcp],
    ["source-admin-section", "소스·색인", capabilities.source],
    ["quality-admin-section", "검색 품질", capabilities.quality],
    ["security-admin-section", "보안·Secret", capabilities.security || capabilities.securityEvents],
    ["audit-admin-section", "감사", capabilities.audit],
    ["database-admin-section", "데이터베이스", capabilities.status],
    ["status-admin-section", "운영 상태", capabilities.status],
    ["backup-admin-section", "백업·복구", capabilities.backup],
  ].filter((entry) => entry[2]);
  $("#admin-menu").innerHTML = entries
    .map(([id, label]) => `<button type="button" data-admin-target="${id}">${label}</button>`)
    .join("");
  const open = (target) => {
    document.querySelectorAll(".admin-panel").forEach((panel) => (panel.hidden = panel.id !== target));
    document.querySelectorAll("[data-admin-target]").forEach((button) => button.classList.toggle("active", button.dataset.adminTarget === target));
    if (target === "database-admin-section") refreshDatabase();
    if (target === "users-admin-section") refreshAdminUsers();
  };
  document.querySelectorAll("[data-admin-target]").forEach(
    (button) => (button.onclick = () => open(button.dataset.adminTarget)),
  );
  if (entries.length) open(entries[0][0]);
}

let adminUserRoles = [];
async function refreshAdminUsers() {
  try {
    const result = await api("/api/v1/admin/users");
    adminUserRoles = result.roles || [];
    $("#admin-user-roles").innerHTML =
      `<legend>플랫폼 역할</legend>${adminUserRoles.map((role) => `<label><input type="checkbox" name="admin-role" value="${esc(role)}" /> ${esc(role)}${role === "platform-admin" ? " (최고관리자)" : ""}</label>`).join("")}`;
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
    const [cases, runs] = await Promise.all([
      api("/api/v1/admin/quality/cases"),
      api("/api/v1/admin/quality/runs"),
    ]);
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
            const results = await api(
              `/api/v1/admin/quality/runs/${encodeURIComponent(row.dataset.qualityRun)}/results`,
            );
            $("#quality-results").innerHTML =
              `<table><thead><tr><th>사례</th><th>검색 결과</th><th>Recall/MRR/nDCG</th><th>시간/오류</th></tr></thead><tbody>${results.map((result) => `<tr><td>${esc(result.caseName)}</td><td>${result.retrievedSources.map(esc).join("<br>")}</td><td>${result.recallAtK.toFixed(3)} / ${result.reciprocalRank.toFixed(3)} / ${result.ndcgAtK.toFixed(3)}</td><td>${result.durationMs} ms<br>${esc(result.errorMessage)}</td></tr>`).join("")}</tbody></table>`;
          } catch (error) {
            showAdmin(error.message, false);
          }
        }),
    );
  } catch (error) {
    showAdmin(error.message, false);
  }
}
async function refreshBackups(capabilities = activeCapabilities) {
  if (!capabilities.backup) return;
  try {
    const records = await api("/api/v1/admin/backups");
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
    $("#discovery").innerHTML =
      `<table><thead><tr><th>저장소</th><th>기본 브랜치</th><th></th></tr></thead><tbody>${discovered.map((r, n) => `<tr><td>${esc(r.ProjectKey)}/${esc(r.Slug)}<br><small>${esc(r.Description)}</small></td><td>${esc(r.DefaultBranch)}</td><td><button data-register="${n}">등록·색인</button></td></tr>`).join("")}</tbody></table>`;
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
    showAdmin(e.message, false);
  }
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
    showAdmin(e.message, false);
  }
}
async function refreshOps(capabilities = activeCapabilities) {
  try {
    const tools = capabilities.mcp ? await api("/api/v1/admin/mcp/tools") : [];
    const [repos, jobs] = capabilities.source
      ? await Promise.all([
          api("/api/v1/admin/repositories"),
          api("/api/v1/admin/index-jobs"),
        ])
      : [[], []];
    $("#mcp-tools").innerHTML =
      `<table><thead><tr><th>도구</th><th>상태</th><th>Timeout</th><th>Cache</th><th></th></tr></thead><tbody>${tools.map((t) => `<tr><td>${esc(t.name)}<br><small>${esc(t.description)}</small></td><td>${t.enabled ? "활성" : "비활성"}</td><td>${t.timeoutMs} ms</td><td>${t.cacheSeconds} s</td><td><button data-tool="${esc(t.name)}" data-enabled="${t.enabled}" data-timeout="${t.timeoutMs}" data-cache="${t.cacheSeconds}">설정</button></td></tr>`).join("")}</tbody></table>`;
    document.querySelectorAll("[data-tool]").forEach(
      (b) =>
        (b.hidden = !capabilities.mcpWrite) ||
        (b.onclick = async () => {
          const enabled = confirm(
              `${b.dataset.tool} 도구를 활성화할까요? 취소를 누르면 비활성화합니다.`,
            ),
            timeout = prompt("Timeout(ms, 100~120000)", b.dataset.timeout),
            cache = prompt("Cache(seconds, 0~86400)", b.dataset.cache);
          if (timeout === null || cache === null) return;
          await api(
            `/api/v1/admin/mcp/tools/${encodeURIComponent(b.dataset.tool)}`,
            {
              method: "PUT",
              body: JSON.stringify({
                enabled,
                timeoutMs: Number(timeout),
                cacheSeconds: Number(cache),
              }),
            },
          );
          refreshOps(capabilities);
        }),
    );
    $("#repositories").innerHTML =
      `<table><thead><tr><th>소스</th><th>Library ID</th><th>기본 브랜치</th><th>마지막 색인</th><th></th></tr></thead><tbody>${repos.map((r) => `<tr><td>${esc(r.sourceType)}</td><td>${esc(r.libraryId)}</td><td>${esc(r.defaultBranch)}</td><td>${date(r.indexedAt)}</td><td><button data-index="${esc(r.id)}">재색인</button></td></tr>`).join("")}</tbody></table>`;
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
  } catch (e) {
    showAdmin(e.message, false);
  }
}
async function refreshSecurity(capabilities = activeCapabilities) {
  try {
    const [health, keys, events, audits, secrets, deliveries] = await Promise.all([
      capabilities.status ? api("/api/v1/admin/health") : null,
      capabilities.security ? api("/api/v1/admin/api-keys") : [],
      capabilities.securityEvents ? api("/api/v1/admin/security-events") : [],
      capabilities.audit ? api("/api/v1/admin/audit-logs") : [],
      capabilities.security ? api("/api/v1/admin/secrets") : [],
      capabilities.security || capabilities.securityEvents
        ? api("/api/v1/admin/notification-deliveries")
        : [],
    ]);
    if (health) {
      $("#system-health").textContent =
        `저장소 ${health.repositories} · 청크 ${health.chunks} · 활성 키 ${health.activeApiKeys} · 대기 ${health.indexJobs.pending} · 실패 ${health.indexJobs.failed} · Trace ${health.observability?.tracingEnabled ? "활성" : "비활성"}`;
      $("#system-health").classList.add("ok");
    }
    $("#admin-keys").innerHTML =
      `<table><thead><tr><th>사용자/이름</th><th>Prefix</th><th>상태</th><th>마지막 사용</th><th></th></tr></thead><tbody>${keys.map((k) => `<tr><td>${esc(k.username)} / ${esc(k.name)}</td><td>${esc(k.prefix)}</td><td>${esc(k.status)}</td><td>${date(k.lastUsedAt)}</td><td>${k.status === "active" ? `<button class="danger" data-admin-revoke="${esc(k.id)}">강제 폐기</button>` : ""}</td></tr>`).join("")}</tbody></table>`;
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
  } catch (e) {
    showAdmin(e.message, false);
  }
}
function showAdmin(text, ok) {
  $("#admin-result").textContent = text;
  $("#admin-result").classList.toggle("ok", ok);
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
Promise.allSettled([loadBranding(), loadPublicStatus()]).finally(boot);
