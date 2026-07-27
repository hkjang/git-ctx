const assert = require("node:assert/strict");
const fs = require("node:fs");

const html = fs.readFileSync("web/index.html", "utf8");
const script = fs.readFileSync("web/app.js", "utf8");

for (const id of [
  "admin-menu",
  "setting-tabs",
  "database-status",
  "database-admin-section",
  "admin-database-status",
  "database-form",
  "test-database",
  "migrate-database",
  "login-keycloak",
]) {
  assert.match(html, new RegExp(`id=["']${id}["']`), `missing ${id}`);
}
assert.match(script, /data-setting-tab/);
assert.match(script, /data-admin-target/);
assert.match(script, /applyTLSFieldState/);
assert.match(script, /api\/v1\/public\/status/);
assert.match(script, /api\/v1\/admin\/database\/status/);
assert.match(script, /api\/v1\/admin\/database\/\$\{action\}/);
assert.match(script, /Keycloak Base URL/);
assert.match(script, /Realm Role 매핑/);
console.log("admin UI structure tests passed");
