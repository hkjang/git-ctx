const assert = require("node:assert/strict");
const { capabilitiesFor, categoriesFor } = require("../../web/roles.js");

const categories = [
  "keycloak",
  "bitbucket",
  "gitlab",
  "mcp",
  "search",
  "model",
  "index",
  "permissions",
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
]);
assert.deepEqual(categoriesFor(categories, ["auditor"]), []);
assert.deepEqual(categoriesFor(categories, ["platform-admin"]), categories);
assert.deepEqual(capabilitiesFor(["readonly-operator"]), {
  platform: false,
  settings: false,
  source: true,
  sourceWrite: false,
  mcp: true,
  mcpWrite: false,
  status: true,
  security: false,
  securityEvents: true,
  audit: false,
});
assert.equal(capabilitiesFor(["security-admin"]).security, true);
assert.equal(capabilitiesFor(["security-admin"]).audit, true);
assert.equal(capabilitiesFor(["auditor"]).audit, true);
assert.equal(capabilitiesFor(["mcp-admin"]).mcpWrite, true);
assert.equal(capabilitiesFor(["source-admin"]).sourceWrite, true);
assert.equal(capabilitiesFor(["developer"]).settings, false);
console.log("role capability tests passed");
