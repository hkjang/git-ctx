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
]) {
  assert.match(html, new RegExp(`id=["']${id}["']`), `missing ${id}`);
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
