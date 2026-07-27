(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  root.GitCtxRoles = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  const categoryRoles = {
    bitbucket: "source-admin",
    gitlab: "source-admin",
    index: "source-admin",
    mcp: "mcp-admin",
    search: "search-admin",
    model: "search-admin",
    permissions: "security-admin",
    security: "security-admin",
  };

  function capabilitiesFor(roleValues) {
    const roles = new Set(roleValues || []);
    const has = (role) => roles.has("platform-admin") || roles.has(role);
    return {
      platform: roles.has("platform-admin"),
      settings:
        roles.has("platform-admin") ||
        ["source-admin", "mcp-admin", "search-admin", "security-admin"].some(
          (role) => roles.has(role),
        ),
      source: has("source-admin") || roles.has("readonly-operator"),
      sourceWrite: has("source-admin"),
      mcp: has("mcp-admin") || roles.has("readonly-operator"),
      mcpWrite: has("mcp-admin"),
      status: has("readonly-operator"),
      security: has("security-admin"),
      securityEvents: has("security-admin") || roles.has("readonly-operator"),
      audit: has("security-admin") || roles.has("auditor"),
      backup: has("readonly-operator"),
      backupWrite: roles.has("platform-admin"),
    };
  }

  function categoriesFor(allCategories, roleValues) {
    const roles = new Set(roleValues || []);
    if (roles.has("platform-admin")) return [...allCategories];
    return allCategories.filter(
      (category) =>
        categoryRoles[category] && roles.has(categoryRoles[category]),
    );
  }

  return { capabilitiesFor, categoriesFor };
});
