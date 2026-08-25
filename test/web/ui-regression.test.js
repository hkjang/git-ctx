const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");

const html = fs.readFileSync("web/index.html", "utf8");
const script = fs.readFileSync("web/app.js", "utf8");
const styles = fs.readFileSync("web/app.css", "utf8");

function section(source, start, end) {
  const from = source.indexOf(start);
  assert.notEqual(from, -1, `missing section start: ${start}`);
  const to = source.indexOf(end, from + start.length);
  assert.notEqual(to, -1, `missing section end: ${end}`);
  return source.slice(from, to);
}

function requireIDs(ids) {
  for (const id of ids) {
    assert.match(html, new RegExp(`id=["']${id}["']`), `missing #${id}`);
  }
}

test("personal home, profile menu, and quick navigation stay complete and keyboard accessible", () => {
  requireIDs([
    "home",
    "dashboard-greeting",
    "personal-stats",
    "onboarding-progress",
    "personal-checklist",
    "dashboard-repositories",
    "dashboard-calls",
    "profile-toggle",
    "profile-dropdown",
    "quick-nav-button",
    "quick-nav-dialog",
    "quick-nav-query",
    "quick-nav-results",
  ]);

  assert.match(html, /id="profile-dropdown"[^>]*role="menu"/);
  for (const target of ["home", "account", "connections", "keys", "knowledge", "activity"]) {
    assert.match(
      html,
      new RegExp(`role=["']menuitem["'][^>]*data-profile-target=["']${target}["']`),
      `profile menu is missing ${target}`,
    );
    assert.match(
      html,
      new RegExp(`data-personal-target=["']${target}["']`),
      `personal navigation is missing ${target}`,
    );
  }
  assert.match(html, /id="quick-nav-query"[^>]*role="combobox"/);
  assert.match(html, /id="quick-nav-query"[^>]*aria-controls="quick-nav-results"/);
  assert.match(html, /id="quick-nav-results"[^>]*role="listbox"/);

  const personal = section(script, "function setupPersonalNavigation()", "function navigatePersonal");
  assert.match(script, /personal:\s*"home"/);
  assert.match(personal, /openPersonalView\("home"\)/);
  assert.match(script, /function renderPersonalDashboard\(/);

  const profile = section(script, "function setupProfileMenu()", "function setupQuickNavigation");
  for (const key of ["Escape", "ArrowDown", "ArrowUp", "Home", "End"]) {
    assert.match(profile, new RegExp(`event\\.key[^\\n]*["']${key}["']`), `profile menu does not handle ${key}`);
  }
  assert.match(profile, /toggle\.focus\(\)/);

  const quick = section(script, "function setupQuickNavigation", "function renderPersonalDashboard");
  for (const label of ["내 홈", "프로필", "MCP 연결", "API 키 관리", "코드 지식 검색", "내 활동·저장소"]) {
    assert.match(quick, new RegExp(label), `quick navigation is missing ${label}`);
  }
  assert.match(quick, /role="option"/);
  assert.match(quick, /aria-activedescendant/);
  for (const key of ["ArrowDown", "ArrowUp", "Enter"]) {
    assert.match(quick, new RegExp(`["']${key}["']`), `quick navigation does not handle ${key}`);
  }
});

test("MCP client configurations are integrated as secret-free copyable tabs", () => {
  requireIDs([
    "endpoint",
    "copy-endpoint",
    "mcp-client-tabs",
    "mcp-client-title",
    "mcp-client-file",
    "mcp-client-description",
    "mcp-client-config",
    "copy-client-config",
    "mcp-env-command",
    "copy-env-command",
  ]);
  const clientsTag = html.indexOf('<script src="/clients.js">');
  const appTag = html.indexOf('<script src="/app.js">');
  assert.ok(clientsTag >= 0 && clientsTag < appTag, "clients.js must load before app.js");

  const connections = section(script, "function setupClientConnections()", '$("#login").onclick');
  assert.match(connections, /GitCtxClients\.configurations\(endpoint\)/);
  assert.doesNotMatch(
    connections,
    /GitCtxClients\.configurations\([^)]*,/,
    "the client generator must never receive secret material",
  );
  assert.match(connections, /GitCtxClients\.environmentCommands\(\)/);
  assert.match(connections, /role="tab"/);
  assert.match(connections, /\.tabIndex\s*=\s*selected\s*\?\s*0\s*:\s*-1/);
  for (const key of ["ArrowLeft", "ArrowRight", "Home", "End"]) {
    assert.match(connections, new RegExp(`["']${key}["']`), `MCP client tabs do not handle ${key}`);
  }
  assert.match(connections, /copyText\(active\.config/);
  assert.match(connections, /copyText\(command\.command/);
});

test("API-key scope presets default to the two Context7-compatible tools", () => {
  const keyForm = section(html, '<form id="key-form"', "</form>");
  for (const preset of ["context7", "code", "all"]) {
    assert.match(keyForm, new RegExp(`data-scope-preset=["']${preset}["']`));
  }
  assert.match(keyForm, /id="scope-selection-count"/);

  const checked = [...keyForm.matchAll(/<input[^>]*name="scope"[^>]*value="([a-z0-9-]+)"[^>]*checked/g)]
    .map((match) => match[1]);
  assert.deepEqual(checked, ["resolve-library-id", "query-docs"]);

  const presets = section(script, "const keyScopePresets", '$("#key-form").onsubmit');
  assert.match(presets, /context7:\s*\["resolve-library-id",\s*"query-docs"\]/);
  assert.match(presets, /name === "all"[^\n]*grantableMCPScopes\(\)/);
  assert.match(presets, /\$\$\("\[data-scope-preset\]"\)/);
  assert.match(presets, /updateKeyScopeCount\(\)/);
  assert.match(script, /applyKeyScopePreset\("context7"\)/);
});

test("a newly issued API key is one-time, focused, and directly copyable", () => {
  assert.match(html, /id="secret"[^>]*role="status"[^>]*aria-live="assertive"[^>]*tabindex="-1"/);
  const showSecret = section(script, "function showSecret(secret)", "// 외부 연결 테스트");
  assert.match(showSecret, /id="copy-secret-key"/);
  assert.match(showSecret, /currentOneTimeSecret\(\)/);
  assert.match(showSecret, /copyText\(value,/);
  assert.match(showSecret, /환경변수 명령 복사/);
  assert.match(showSecret, /navigatePersonal\("connections"\)/);
  assert.match(showSecret, /\$\("#secret"\)\.focus\(\)/);
  assert.doesNotMatch(showSecret, /(?:localStorage|sessionStorage)\.setItem/);
  assert.match(script, /function clearOneTimeSecret\(\)/);
  assert.match(script, /visibleOneTimeSecret\s*=\s*null/);
  assert.match(script, /panel\.replaceChildren\(\)/);
  assert.match(script, /target\s*!==\s*"keys"\) clearOneTimeSecret\(\)/);
  assert.match(script, /if \(admin\) clearOneTimeSecret\(\)/);
  const copyFallback = section(script, "async function copyText", "function downloadText");
  assert.match(copyFallback, /finally\s*\{/);
  assert.match(copyFallback, /field\.value\s*=\s*""/);
  assert.match(copyFallback, /field\.remove\(\)/);
});

test("knowledge tools can be filtered and results copied, downloaded, or cleared", () => {
  requireIDs([
    "knowledge-tool-query",
    "knowledge-tool-count",
    "knowledge-tools",
    "knowledge-result-title",
    "knowledge-result-meta",
    "knowledge-result",
    "copy-knowledge-result",
    "download-knowledge-result",
    "clear-knowledge-result",
  ]);
  for (const filter of ["all", "discover", "explore", "analyze", "context"]) {
    assert.match(html, new RegExp(`data-tool-filter=["']${filter}["']`));
  }
  assert.match(html, /data-tool-category=/);
  assert.match(html, /data-tool-search=/);

  const knowledge = section(script, "function setupKnowledgeSearch()", "let currentRoleSet");
  assert.match(knowledge, /const filterTools\s*=\s*\(\)\s*=>/);
  assert.match(knowledge, /card\.hidden\s*=/);
  assert.match(knowledge, /knowledge-tool-count/);
  assert.match(knowledge, /\[data-tool-filter\]/);
  assert.match(knowledge, /copyText\(output\.textContent/);
  assert.match(knowledge, /downloadText\(`git-ctx-\$\{slug\}\.md`,\s*output\.textContent\)/);
  assert.match(knowledge, /clear-knowledge-result/);
  assert.match(knowledge, /output\.textContent\s*=\s*"검색 결과가 여기에 표시됩니다\."/);
});

test("settings editor visibly tracks dirty state and protects unsaved edits", () => {
  assert.match(html, /id="setting-dirty-state"[^>]*aria-live="polite"/);
  assert.match(
    script,
    /function\s+(?:set|mark)SettingDirty\s*\(/,
    "expected a single dirty-state helper used by generated fields and raw JSON",
  );
  assert.match(script, /beforeunload/, "page exit must warn while settings are dirty");
  assert.match(
    script,
    /(?:confirmDiscard|confirmSetting|settingDirty)[\s\S]{0,1000}confirm\(/,
    "changing tabs must explicitly confirm discarding dirty settings",
  );
  const visualClass = script.match(
    /(?:state|dirtyState)\.classList\.toggle\(["']([^"']+)["'],\s*settingDirty\)/,
  );
  assert.ok(visualClass, "dirty-state renderer must toggle a visual state class");
  assert.ok(
    new RegExp(`\\.dirty-state\\.${visualClass[1]}(?:\\s|\\{|,)`).test(styles),
    `missing CSS for the dirty-state class .${visualClass[1]}`,
  );
});

test("overlapping setting loads cannot overwrite a newer selection", () => {
  const load = section(script, "loadCurrentSetting = async (category", "// 연결 테스트와 검증 결과");
  assert.match(
    script,
    /(?:settingLoad(?:Generation|Sequence|Request|Token)|settingLoadController|AbortController)/,
    "expected a load generation/token or AbortController",
  );
  assert.match(
    load,
    /(?:AbortController|\.abort\(|(?:generation|sequence|request|token|loadId)[\s\S]{0,240}(?:!==|===)[\s\S]{0,120}return)/i,
    "loadCurrentSetting must reject a response superseded by a newer request",
  );
});

test("settings tabs implement roving tabindex and keyboard navigation", () => {
  assert.match(html, /id="setting-tabs"[^>]*role="tablist"/);
  const setup = section(script, "function setupAdmin(roles, capabilities)", "async function refreshKeycloakStatus");
  assert.match(setup, /role="tab"/);
  assert.match(setup, /\.tabIndex\s*=\s*active\s*\?\s*0\s*:\s*-1/);
  assert.match(setup, /\.onkeydown\s*=/);
  for (const key of ["ArrowLeft", "ArrowRight", "Home", "End"]) {
    assert.match(setup, new RegExp(`["']${key}["']`), `settings tabs do not handle ${key}`);
  }
  assert.match(setup, /\.focus\(\)/);
});

test("vector status performs an explicit embedding probe and renders its outcome", () => {
  requireIDs(["vector-card", "vector-status", "refresh-vector-status", "test-embedding-model"]);
  const vector = section(script, "async function refreshVectorStatus(", "function renderSettingResult");
  assert.match(vector, /\/api\/v1\/admin\/vector\/status\$\{probe\s*\?\s*"\?probe=true"\s*:\s*""\}/);
  assert.match(script, /test-embedding-model[\s\S]{0,180}refreshVectorStatus\(true\)/);
  assert.match(script, /refresh-vector-status[\s\S]{0,120}refreshVectorStatus\(false\)/);
  assert.match(vector, /status\.embeddingProbe/);
  assert.match(vector, /probeResult\.ok/);
  assert.match(vector, /probeResult\.dimensions/);
  assert.match(vector, /probeResult\.latencyMs/);
  assert.match(vector, /probeResult\.stage/);
  assert.match(vector, /probeResult\.error/);
  assert.match(vector, /probe-failed/);
  assert.match(styles, /\.result-list\s+\.probe-failed/);
});

test("PostgreSQL migration controls are destructive, initially disabled, and platform-admin gated", () => {
  requireIDs([
    "database-migration",
    "database-form",
    "database-dsn",
    "database-confirm",
    "test-database",
    "migrate-database",
    "database-action-result",
  ]);
  assert.match(html, /id="database-dsn"[^>]*type="password"[^>]*required/);
  assert.match(html, /id="database-confirm"[^>]*placeholder="MIGRATE TO POSTGRES"/);
  assert.match(html, /id="migrate-database"[^>]*class="danger"[^>]*disabled/);

  const ops = section(script, "function setupOps(capabilities)", "function setupAdminNavigation");
  assert.match(ops, /database-migration[^\n]*!capabilities\.platform/);
});

test("PostgreSQL migration unlocks only after testing the exact same DSN", () => {
  const action = section(script, "async function runDatabaseAction(action)", "const qualityCSV");
  const statePattern = /(?:testedDatabaseDSN|databaseTestedDSN|databaseMigrationReady|databaseTestState)/;
  assert.ok(
    statePattern.test(script),
    "a successful connection test must be remembered only for the tested DSN",
  );
  assert.ok(
    /action\s*===\s*"migrate"[\s\S]{0,1000}(?:testedDatabaseDSN|databaseTestedDSN|databaseMigrationReady|databaseTestState)/.test(action),
    "migrate must reject an untested or changed DSN before the API call",
  );
  assert.ok(
    /database-dsn[\s\S]{0,500}(?:oninput|addEventListener\(["']input)/.test(script),
    "editing the DSN must invalidate the previous connection test",
  );
});

test("PostgreSQL migration checks the exact confirmation phrase before calling the API", () => {
  const gate = section(script, "function updateDatabaseMigrationGate()", "async function runDatabaseAction");
  const action = section(script, "async function runDatabaseAction(action)", "const qualityCSV");
  assert.ok(
    /database-confirm[\s\S]{0,160}===\s*"MIGRATE TO POSTGRES"/.test(gate),
    "the migration button must stay disabled unless the exact phrase is entered",
  );
  assert.match(gate, /button\.disabled\s*=\s*!allowed/);
  assert.match(action, /body\.confirm\s*=\s*\$\("#database-confirm"\)\.value\.trim\(\)/);
});
