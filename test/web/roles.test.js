const assert = require("node:assert/strict");
const { capabilitiesFor, categoriesFor } = require("../../web/roles.js");

const categories = [
  "keycloak",
  "bitbucket",
  "gitlab",
  "mcp",
  "search",
  "model",
  "opensearch",
  "vector",
  "vault",
  "index",
  "security",
  "ui",
];
assert.deepEqual(categoriesFor(categories, ["source-admin"]), [
  "bitbucket",
  "gitlab",
  "index",
]);
assert.deepEqual(categoriesFor(categories, ["search-admin"]), [
  "search",
  "model",
  "opensearch",
  "vector",
]);
assert.deepEqual(categoriesFor(categories, ["security-admin"]), [
  "vault",
  "security",
]);
assert.deepEqual(categoriesFor(categories, ["auditor"]), []);
assert.deepEqual(categoriesFor(categories, ["platform-admin"]), categories);
assert.deepEqual(capabilitiesFor(["readonly-operator"]), {
  platform: false,
  users: false,
  settings: false,
  source: true,
  sourceWrite: false,
  mcp: true,
  mcpWrite: false,
  mcpAudit: false,
  status: true,
  security: false,
  securityEvents: true,
  audit: false,
  backup: true,
  backupWrite: false,
  quality: true,
  qualityWrite: false,
});
assert.equal(capabilitiesFor(["security-admin"]).security, true);
assert.equal(capabilitiesFor(["security-admin"]).audit, true);
assert.equal(capabilitiesFor(["auditor"]).audit, true);
assert.equal(capabilitiesFor(["mcp-admin"]).mcpWrite, true);
// 운영 조회 역할은 집계 화면만 보고, 질의 원문이 담긴 감사 로그는 보지 못합니다.
assert.equal(capabilitiesFor(["readonly-operator"]).mcpAudit, false);
assert.equal(capabilitiesFor(["auditor"]).mcpAudit, true);
assert.equal(capabilitiesFor(["mcp-admin"]).mcpAudit, true);
assert.equal(capabilitiesFor(["platform-admin"]).mcpAudit, true);
assert.equal(capabilitiesFor(["source-admin"]).sourceWrite, true);
assert.equal(capabilitiesFor(["developer"]).settings, false);
assert.equal(capabilitiesFor(["platform-admin"]).backupWrite, true);
assert.equal(capabilitiesFor(["platform-admin"]).qualityWrite, true);
assert.equal(capabilitiesFor(["platform-admin"]).users, true);
console.log("role capability tests passed");
