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
    ["issuerUrl", "Keycloak Issuer URL", "url", ""],
    ["clientId", "Client ID", "text", "git-ctx"],
    ["clientSecret", "Client Secret", "password", ""],
    ["redirectUrl", "Redirect URL", "url", ""],
    ["postLogoutRedirectUrl", "Logout Redirect URL", "url", ""],
    ["scopes", "Scopes (쉼표 구분)", "array", "openid,profile,email,groups"],
    ["usernameClaim", "Username Claim", "text", "preferred_username"],
    ["groupsClaim", "Groups Claim", "text", "groups"],
    [
      "bitbucketUserSlugClaim",
      "Bitbucket User Slug Claim",
      "text",
      "bitbucket_user_slug",
    ],
    ["gitlabUserIdClaim", "GitLab User ID Claim", "text", "gitlab_user_id"],
    ["tlsVerify", "TLS 인증서 검증", "boolean", true],
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
    ["tlsVerify", "TLS 인증서 검증", "boolean", true],
    ["caCertificate", "사내 CA PEM", "textarea", ""],
    ["proxyUrl", "Proxy URL", "url", ""],
    ["timeoutSeconds", "Timeout(초)", "number", 30],
  ],
  gitlab: [
    ["baseUrl", "GitLab Base URL", "url", ""],
    ["token", "Access Token", "password", ""],
    ["webhookSecret", "Webhook Secret", "password", ""],
    ["tlsVerify", "TLS 인증서 검증", "boolean", true],
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
    ["tlsVerify", "TLS 인증서 검증", "boolean", true],
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
    ["tlsVerify", "TLS 인증서 검증", "boolean", true],
    ["caCertificate", "사내 CA PEM", "textarea", ""],
    ["proxyUrl", "Proxy URL", "url", ""],
    ["timeoutSeconds", "Timeout(초)", "number", 30],
  ],
};
const settingDefaults = (category) =>
  Object.fromEntries(
    (integrationSettingFields[category] || []).map(([key, , , value]) => [
      key,
      value,
    ]),
  );
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
  if (!response.ok)
    throw new Error(body.detail || body.title || `HTTP ${response.status}`);
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
  ].includes(category);
  $("#preview-keycloak").hidden = category !== "keycloak";
  $("#setting-fields").innerHTML = fields
    .map(([key, label, type, fallback]) => {
      const current = value[key] ?? fallback;
      if (type === "boolean")
        return `<label><input data-setting-key="${key}" data-setting-type="boolean" type="checkbox" ${current ? "checked" : ""} /> ${esc(label)}</label>`;
      if (type === "textarea")
        return `<label class="wide">${esc(label)}<textarea data-setting-key="${key}" data-setting-type="string" rows="4">${esc(current)}</textarea></label>`;
      if (type.startsWith("select:")) {
        const choices = type.slice(7).split("|");
        return `<label>${esc(label)}<select data-setting-key="${key}" data-setting-type="string">${choices.map((choice) => `<option ${choice === current ? "selected" : ""}>${esc(choice)}</option>`).join("")}</select></label>`;
      }
      const inputType = type === "array" ? "text" : type;
      const shown = Array.isArray(current) ? current.join(",") : current;
      return `<label>${esc(label)}<input data-setting-key="${key}" data-setting-type="${type}" type="${inputType}" value="${esc(shown)}" /></label>`;
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
                : field.value;
        $("#setting-json").value = JSON.stringify(next, null, 2);
      }),
  );
}
function setupAdmin(roles, capabilities) {
  const allowedCategories = GitCtxRoles.categoriesFor(categories, [...roles]);
  $("#settings-admin").hidden = !capabilities.settings;
  if (!capabilities.settings) return;
  $("#category").innerHTML = allowedCategories
    .map((c) => `<option>${c}</option>`)
    .join("");
  $("#category").onchange = () => $("#load-setting").click();
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
  $("#load-setting").click();
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
  $("#save-setting").onclick = async () => {
    try {
      JSON.parse($("#setting-json").value);
      const x = await api(`/api/v1/admin/settings/${$("#category").value}`, {
        method: "PUT",
        headers: { "X-Change-Reason": $("#reason").value },
        body: $("#setting-json").value,
      });
      if ($("#category").value === "keycloak" && bootstrapInfo.required) {
        sessionStorage.removeItem("git_ctx_bootstrap_token");
        bootstrapInfo.required = false;
        showAdmin(
          `버전 ${x.version} 저장 완료. 일회용 관리자 토큰이 폐기되었습니다. Keycloak으로 로그인하세요.`,
          true,
        );
        setTimeout(() => location.reload(), 1200);
        return;
      }
      showAdmin(`버전 ${x.version} 저장 완료`, true);
    } catch (e) {
      showAdmin(e.message, false);
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
    const [health, keys, events, audits] = await Promise.all([
      capabilities.status ? api("/api/v1/admin/health") : null,
      capabilities.security ? api("/api/v1/admin/api-keys") : [],
      capabilities.securityEvents ? api("/api/v1/admin/security-events") : [],
      capabilities.audit ? api("/api/v1/admin/audit-logs") : [],
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
loadBranding().finally(boot);
