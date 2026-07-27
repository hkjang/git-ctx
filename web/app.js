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
  "permissions",
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
    ["issuerUrl", "OIDC Issuer URL (자동 생성 또는 직접 입력)", "url", ""],
    ["clientId", "Client ID", "text", "git-ctx"],
    ["clientSecret", "Client Secret", "password", ""],
    ["redirectUrl", "Redirect URL", "url", ""],
    ["postLogoutRedirectUrl", "Logout Redirect URL", "url", ""],
    ["scopes", "Scopes (쉼표 구분)", "array", "openid,profile,email,groups"],
    ["usernameClaim", "Username Claim", "text", "preferred_username"],
    ["groupsClaim", "Groups Claim", "text", "groups"],
    ["emailClaim", "Email Claim", "text", "email"],
    [
      "bitbucketUserSlugClaim",
      "Bitbucket User Slug Claim",
      "text",
      "bitbucket_user_slug",
    ],
    ["gitlabUserIdClaim", "GitLab User ID Claim", "text", "gitlab_user_id"],
    ["realmRoleMappings", "Realm Role 매핑 (JSON)", "json", {}],
    ["clientRoleMappings", "Client Role 매핑 (JSON)", "json", {}],
    ["bitbucketGroupMappings", "Bitbucket Group 매핑 (JSON)", "json", {}],
    ["allowedClockSkewSeconds", "Token Clock Skew(초)", "number", 30],
    ["tlsVerify", "TLS 인증서 검증 사용", "boolean", true],
    ["caCertificate", "사내 CA PEM", "textarea", ""],
    ["proxyUrl", "Proxy URL", "url", ""],
    ["timeoutSeconds", "Timeout(초)", "number", 15],
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
};
const settingCategoryMeta = {
  keycloak: ["Keycloak SSO", "OIDC 로그인, Claim과 역할 매핑"],
  bitbucket: ["Bitbucket", "Bitbucket Server 6.9.1 연결과 Webhook"],
  gitlab: ["GitLab", "GitLab API v4 연결과 Webhook"],
  mcp: ["MCP", "Transport, Origin, 호출 제한"],
  search: ["검색", "키워드·벡터 가중치와 결과 수"],
  model: ["모델", "Embedding과 Reranker"],
  opensearch: ["OpenSearch", "BM25 projection과 인증"],
  index: ["색인", "Polling과 기본 색인 정책"],
  permissions: ["권한", "역할과 저장소 정책"],
  security: ["보안", "신뢰 프록시와 키 정책"],
  vault: ["Vault", "KV v2 Secret backend"],
  notifications: ["알림", "이메일·Webhook·메신저"],
  logging: ["로깅", "레벨·마스킹·보존"],
  observability: ["관측성", "OpenTelemetry Export"],
  backup: ["백업", "주기·경로·보존"],
  retention: ["보존", "문서·감사 데이터 보존"],
  operations: ["운영", "점검 모드·재시도"],
  ui: ["UI", "서비스명·로고·공지"],
};
const settingDefaults = (category) => {
  const defaults = Object.fromEntries(
    (integrationSettingFields[category] || []).map(([key, , , value]) => [
      key,
      value,
    ]),
  );
  if (category === "keycloak") {
    defaults.redirectUrl ||= `${location.origin}/auth/callback`;
    defaults.postLogoutRedirectUrl ||= `${location.origin}/`;
  }
  return defaults;
};
let bootstrapInfo = { required: false, tokenFile: "" };
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
    };
    if (bootstrapInfo.required) $("#login").textContent = "최초 관리자 설정";
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
  if (!bootstrapInfo.required) return (location.href = "/auth/login");
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
$("#logout").onclick = async () => {
  const result = await api("/auth/logout", { method: "POST" });
  if (result?.logoutUrl) {
    location.href = result.logoutUrl;
  } else {
    location.reload();
  }
};
async function boot() {
  try {
    const me = await api("/api/v1/me");
    $("#status").textContent = "Keycloak 인증이 완료되었습니다.";
    $("#status").classList.add("ok");
    $("#login").hidden = true;
    $("#logout").hidden = false;
    $("#account").hidden = false;
    $("#keys").hidden = false;
    $("#activity").hidden = false;
    $("#identity").textContent =
      `${me.Username} · 역할: ${(me.Roles || []).join(", ")} · ACL: ${me.ACLPrincipal || "매핑되지 않음(Fail Closed)"}`;
    $("#profile-version").textContent = `서비스 버전 v${me.Version || "unknown"}`;
    const roles = new Set(me.Roles || []);
    const capabilities = GitCtxRoles.capabilitiesFor(me.Roles || []);
    if (Object.values(capabilities).some(Boolean)) {
      $("#admin").hidden = false;
      setupAdmin(roles, capabilities);
      setupOps(capabilities);
    }
    loadKeys();
    loadActivity();
  } catch (e) {
    $("#status").textContent =
      "Keycloak으로 로그인하면 개인 MCP 키와 관리자 기능을 사용할 수 있습니다.";
  }
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
      const inputType = type === "array" ? "text" : type;
      const shown = Array.isArray(current) ? current.join(",") : current;
      return `<label data-field-key="${key}">${esc(label)}<input data-setting-key="${key}" data-setting-type="${type}" type="${inputType}" value="${esc(shown)}" /></label>`;
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
                : field.value;
          $("#setting-json").value = JSON.stringify(next, null, 2);
          field.setCustomValidity("");
          if (field.dataset.settingKey === "tlsVerify") applyTLSFieldState();
        } catch {
          field.setCustomValidity("올바른 JSON을 입력하세요.");
        }
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
  if (!capabilities.settings) return;
  $("#category").innerHTML = allowedCategories
    .map((c) => `<option>${c}</option>`)
    .join("");
  $("#setting-tabs").innerHTML = allowedCategories
    .map((category) => `<button type="button" role="tab" data-setting-tab="${category}">${esc(settingCategoryMeta[category]?.[0] || category)}</button>`)
    .join("");
  const selectCategory = (category) => {
    $("#category").value = category;
    document.querySelectorAll("[data-setting-tab]").forEach((tab) => {
      const active = tab.dataset.settingTab === category;
      tab.classList.toggle("active", active);
      tab.setAttribute("aria-selected", String(active));
    });
    const meta = settingCategoryMeta[category] || [category, "동적 운영 설정"];
    $("#setting-context").innerHTML = `<strong>${esc(meta[0])}</strong><span>${esc(meta[1])}</span>`;
    $("#login-keycloak").hidden = category !== "keycloak";
    $("#load-setting").click();
  };
  document.querySelectorAll("[data-setting-tab]").forEach(
    (tab) => (tab.onclick = () => selectCategory(tab.dataset.settingTab)),
  );
  $("#category").onchange = () => selectCategory($("#category").value);
  $("#load-setting").onclick = async () => {
    try {
      const x = await api(`/api/v1/admin/settings/${$("#category").value}`);
      $("#setting-json").value = JSON.stringify(x.value, null, 2);
      renderSettingFields($("#category").value, x.value);
      showAdmin(
        `버전 ${x.version}을 불러왔습니다. 마스킹 값은 새 Secret으로 교체하지 않으면 기존 값이 보존됩니다.`,
        true,
      );
    } catch (e) {
      if (e.status && e.status !== 404) {
        showAdmin(`설정을 불러오지 못했습니다: ${e.message}`, false);
        return;
      }
      const defaults = settingDefaults($("#category").value);
      $("#setting-json").value = JSON.stringify(defaults, null, 2);
      renderSettingFields($("#category").value, defaults);
      showAdmin(
        "아직 저장되지 않은 영역입니다. 기본 템플릿을 불러왔습니다.",
        true,
      );
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
    location.href = "/auth/login?return_to=/";
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
        headers: { "X-Change-Reason": $("#reason").value },
        body: $("#setting-json").value,
      });
      if ($("#category").value === "keycloak" && bootstrapInfo.required) {
        showAdmin(
          `버전 ${x.version} 저장 완료. 이제 “Keycloak 로그인 시험”으로 platform-admin 로그인을 완료하세요. 성공할 때까지 최초 관리자 복구 세션은 유지됩니다.`,
          true,
        );
      } else showAdmin(`버전 ${x.version} 저장 완료`, true);
      $("#reason").value = "";
    } catch (e) {
      showAdmin(`저장하지 못했습니다: ${e.message}`, false);
    } finally {
      button.disabled = false;
      button.textContent = "저장";
    }
  };
  $("#rollback-setting").onclick = async () => {
    const target = prompt("복구할 설정 버전 번호");
    if (target === null) return;
    const reason = prompt("복구 사유");
    if (!reason) return;
    try {
      const x = await api(
        `/api/v1/admin/settings/${$("#category").value}/rollback`,
        {
          method: "POST",
          body: JSON.stringify({ targetVersion: Number(target), reason }),
        },
      );
      showAdmin(
        `버전 ${x.restoredFrom}의 값으로 새 버전 ${x.version}을 생성했습니다.`,
        true,
      );
      $("#load-setting").click();
    } catch (e) {
      showAdmin(e.message, false);
    }
  };
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
            reason: form.get("reason"),
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
  };
  document.querySelectorAll("[data-admin-target]").forEach(
    (button) => (button.onclick = () => open(button.dataset.adminTarget)),
  );
  if (entries.length) open(entries[0][0]);
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
    body.reason = $("#database-reason").value.trim();
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
          const reason = prompt("복원 사유를 입력하세요.");
          if (!reason) return;
          try {
            await api(
              `/api/v1/admin/backups/${encodeURIComponent(id)}/restore`,
              {
                method: "POST",
                headers: {
                  "X-Restore-Confirmation": confirmation,
                  "X-Change-Reason": reason,
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
    const [health, keys, events, audits, secrets] = await Promise.all([
      capabilities.status ? api("/api/v1/admin/health") : null,
      capabilities.security ? api("/api/v1/admin/api-keys") : [],
      capabilities.securityEvents ? api("/api/v1/admin/security-events") : [],
      capabilities.audit ? api("/api/v1/admin/audit-logs") : [],
      capabilities.security ? api("/api/v1/admin/secrets") : [],
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
          const reason = prompt("강제 폐기 사유");
          if (!reason) return;
          await api(
            `/api/v1/admin/api-keys/${encodeURIComponent(b.dataset.adminRevoke)}/revoke`,
            { method: "POST", headers: { "X-Revoke-Reason": reason } },
          );
          refreshSecurity(capabilities);
        }),
    );
    $("#managed-secrets").innerHTML =
      `<table><thead><tr><th>이름</th><th>Backend</th><th>버전</th><th>상태</th><th>갱신</th><th></th></tr></thead><tbody>${secrets.map((s) => `<tr><td><code>secret://${esc(s.name)}</code></td><td>${esc(s.backend)}</td><td>${s.version}</td><td>${esc(s.status)}</td><td>${date(s.updatedAt)}</td><td>${s.status === "active" ? `<button class="danger" data-secret-disable="${esc(s.name)}">중지</button>` : ""}</td></tr>`).join("")}</tbody></table>`;
    document.querySelectorAll("[data-secret-disable]").forEach(
      (button) =>
        (button.onclick = async () => {
          const reason = prompt("비밀정보 중지 사유");
          if (!reason) return;
          await api(
            `/api/v1/admin/secrets/${encodeURIComponent(button.dataset.secretDisable)}/disable`,
            { method: "POST", headers: { "X-Change-Reason": reason } },
          );
          refreshSecurity(capabilities);
        }),
    );
    $("#security-events").innerHTML =
      `<table><thead><tr><th>시간</th><th>저장소/ref</th><th>파일</th><th>탐지/조치</th></tr></thead><tbody>${events.map((x) => `<tr><td>${date(x.occurredAt)}</td><td>${esc(x.repositoryId)} / ${esc(x.refName)}</td><td>${esc(x.filePath)}</td><td>${esc(x.findingType)} / ${esc(x.action)}</td></tr>`).join("")}</tbody></table>`;
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
