const assert = require("node:assert/strict");
const fs = require("node:fs");

const html = fs.readFileSync("web/index.html", "utf8");
const script = fs.readFileSync("web/app.js", "utf8");
const styles = fs.readFileSync("web/app.css", "utf8");

for (const id of [
  "admin-menu",
  "workspace-menu",
  "personal-workspace",
  "personal-menu",
  "admin-workspace-button",
  "profile-menu",
  "profile-dropdown",
  "quick-nav-button",
  "quick-nav-dialog",
  "key-scope-dialog",
  "key-scope-form",
  "key-scope-options",
  "keycloak-runtime-status",
  "setting-tabs",
  "setting-load-status",
  "delete-setting",
  "database-status",
  "database-admin-section",
  "admin-database-status",
  "database-form",
  "test-database",
  "migrate-database",
  "login-keycloak",
  "bootstrap-login",
  "recovery-login",
  "admin-entry-link",
  "entry-description",
  "users-admin-section",
  "admin-user-form",
  "admin-users",
  "admin-key-scopes",
  "notification-deliveries",
  "app-sidebar",
  "sidebar-toggle",
  "theme-toggle",
  "help-button",
  "toast-stack",
  "guide-dialog",
  "guide-title",
  "guide-body",
  "guide-diagnose",
  "acl-guide-button",
  "setting-guide-button",
  "setting-search",
  "setting-test-result",
  "validate-setting",
  "keycloak-mapping-card",
  "keycloak-role-mappings",
  "mapping-source-role",
  "mapping-platform-role",
  "claim-bitbucket",
  "claim-gitlab",
  "claim-groups",
  "source-test-card",
  "source-test-form",
  "source-test-result",
  "account-access",
  "setup-status-panel",
  "setup-status-steps",
  "refresh-setup-status",
  "setting-history",
  "setting-history-list",
  "search-diagnostics-form",
  "diagnostics-username",
  "search-diagnostics-result",
  "discovery-actions",
  "select-all-discovered",
  "register-selected",
  "index-policy-dialog",
  "index-policy-form",
  "index-policy-extensions",
  "index-policy-excludes",
  "index-policy-max-bytes",
  "index-diagnostics",
  "index-queue",
  "vector-card",
  "vector-status",
  "rebuild-vectors",
  "refresh-vector-status",
  "refresh-index-diagnostics",
]) {
  assert.match(html, new RegExp(`id=["']${id}["']`), `missing ${id}`);
}
// app.js가 참조하는 모든 요소는 실제로 존재해야 합니다.
const declaredIds = new Set([...html.matchAll(/id="([^"]+)"/g)].map((match) => match[1]));
for (const match of script.matchAll(/\$\("#([a-zA-Z0-9_-]+)"\)/g)) {
  assert.ok(declaredIds.has(match[1]), `app.js references missing element #${match[1]}`);
}
assert.match(script, /data-setting-tab/);
assert.match(script, /data-admin-target/);
assert.match(script, /data-workspace/);
assert.match(script, /data-personal-target/);
assert.match(script, /data-profile-target/);
assert.match(script, /setupQuickNavigation/);
assert.match(script, /applyTLSFieldState/);
assert.match(script, /api\/v1\/public\/status/);
assert.match(script, /api\/v1\/admin\/database\/status/);
assert.match(script, /api\/v1\/admin\/database\/\$\{action\}/);
assert.match(script, /Keycloak Base URL/);
assert.match(script, /loadCurrentSetting/);
assert.match(script, /비밀값.*마스킹됨/);
assert.match(script, /data-secret-stored/);
assert.match(script, /api\/v1\/admin\/users/);
assert.match(script, /configureMCPKeyScopes/);
// 새 UI 기능: 토스트, 가이드 모달, 권한 진단, 설정 검증, 역할 매핑 편집기
assert.match(script, /function toast\(/);
assert.match(script, /function openGuide\(/);
assert.match(script, /api\/v1\/me\/access/);
assert.match(script, /settings\/\$\{category\}\/\$\{kind\}/);
assert.match(script, /realmRoleMappings/);
assert.match(script, /clientRoleMappings/);
assert.match(script, /renderKeycloakMappings/);
assert.match(script, /reportError/);
assert.match(script, /ACL 가이드 열기/);
assert.match(script, /Diagnostics/);
assert.match(html, /<script src="\/guides\.js">/);
// 초기 설정 진행, 사용자 검색 진단, 설정 이력, 빈 상태와 일괄 등록
assert.match(script, /api\/v1\/admin\/setup-status/);
assert.match(script, /api\/v1\/admin\/search-diagnostics/);
assert.match(script, /settings\/\$\{category\}\/versions/);
assert.match(script, /function markEmptyTables/);
assert.match(script, /registerSelected/);
assert.match(script, /api\/v1\/admin\/index-diagnostics/);
assert.match(script, /indexStateLabels/);
assert.match(script, /api\/v1\/tools\/find-file\/test/);
assert.match(script, /api\/v1\/tools\/read-file\/test/);
assert.match(script, /api\/v1\/tools\/directory\/test/);
assert.match(script, /api\/v1\/tools\/file-history\/test/);
assert.match(script, /api\/v1\/tools\/merge-requests\/test/);
assert.match(script, /api\/v1\/tools\/dependents\/test/);
assert.match(script, /api\/v1\/admin\/vector\/status/);
assert.match(script, /api\/v1\/admin\/vector\/rebuild/);
// 새로고침해도 화면이 유지되어야 하므로 위치를 주소에 기록하고 복원한다.
assert.match(script, /function rememberView/);
assert.match(script, /function parseViewHash/);
assert.match(script, /const initialViewHash = location\.hash/);
assert.match(script, /openSettingCategory/);
assert.match(script, /openAdminPanel/);
assert.match(script, /repositories\/\$\{encodeURIComponent\(repositoryId\)\}\/policy/);
assert.match(script, /autoRegisterWebhook/);
assert.match(html, /search-repositories/);
assert.match(html, /search-code/);
assert.match(script, /api\/v1\/tools\/search-code\/test/);
assert.match(script, /openKeyScopeEditor/);
assert.match(script, /api\/v1\/admin\/api-keys\/\$\{encodeURIComponent\(key\.id\)\}\/scopes/);
assert.match(script, /api\/v1\/me\/api-keys\/\$\{encodeURIComponent\(key\.id\)\}\/scopes/);
assert.match(html, /reindex-repository/);
assert.match(script, /const isAdminEntry = location\.pathname === "\/admin"/);
assert.match(script, /!isAdminEntry \|\| !bootstrapInfo\.required/);
assert.match(script, /api\/v1\/recovery\/login/);
assert.match(script, /isRecoveryEntry/);
assert.match(script, /return_to=\$\{encodeURIComponent\(returnTo\)\}/);
assert.match(html, /Keycloak SSO 로그인/);
assert.match(html, /최고관리자는 모든 관리자 설정과 운영 기능/);
assert.doesNotMatch(html, /변경 사유|id=["']load-setting["']|id=["']rollback-setting["']/);
const keycloakFields = script.slice(script.indexOf("keycloak: ["), script.indexOf("bitbucket: ["));
for (const required of ["baseUrl", "realm", "clientId", "clientSecret"]) assert.match(keycloakFields, new RegExp(`"${required}"`));
for (const excluded of ["issuerUrl", "redirectUrl", "scopes", "Claim", "Role 매핑", "tlsVerify", "caCertificate", "proxyUrl", "timeoutSeconds"]) assert.doesNotMatch(keycloakFields, new RegExp(excluded));
assert.match(script, /location\.pathname\.startsWith\("\/admin"\)/);
for (const category of [
  "keycloak",
  "bitbucket",
  "gitlab",
  "confluence",
  "jira",
  "mcp",
  "search",
  "model",
  "opensearch",
  "index",
  "vector",
  "security",
  "vault",
  "observability",
  "backup",
  "logging",
  "notifications",
  "operations",
  "retention",
  "ui",
]) {
  assert.match(script, new RegExp(`\\n\\s{2}${category}: \\[`), `missing dedicated ${category} settings form`);
}
assert.match(styles, /\[hidden\]\s*\{[^}]*display:\s*none\s*!important/s);
assert.match(script, /maintenanceMode/);
assert.match(script, /서비스 재기동 후 반영/);
assert.match(html, /class=["']status-row["']/);
console.log("admin UI structure tests passed");
