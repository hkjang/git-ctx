const assert = require("node:assert/strict");
const clients = require("../../web/clients.js");

assert.equal(globalThis.GitCtxClients, clients, "browser global and CommonJS API differ");
assert.equal(clients.environmentVariable, "GIT_CTX_API_KEY");
assert.equal(clients.configurations.length, 1, "the generator must accept only an endpoint");
assert.equal(clients.environmentCommands.length, 0, "shell commands must not accept a secret");

const entries = clients.configurations("https://git-ctx.company.local/");
assert.deepEqual(
  entries.map((entry) => entry.id),
  ["codex", "claude-code", "cursor", "vscode"],
);
assert.equal(new Set(entries.map((entry) => entry.id)).size, entries.length);
for (const entry of entries) {
  assert.deepEqual(
    Object.keys(entry).sort(),
    ["config", "description", "file", "id", "label"],
    `${entry.id} has an unexpected public shape`,
  );
  for (const field of ["id", "label", "file", "description", "config"]) {
    assert.equal(typeof entry[field], "string");
    assert.ok(entry[field].length > 0, `${entry.id}.${field} is empty`);
  }
  assert.match(entry.config, /https:\/\/git-ctx\.company\.local\/mcp/);
  assert.doesNotMatch(entry.config, /bctx_(?:live|test)_/i);
}

const codex = entries.find((entry) => entry.id === "codex");
assert.equal(codex.file, "~/.codex/config.toml");
assert.match(codex.config, /\[mcp_servers\.git-ctx\]/);
assert.match(codex.config, /env_http_headers\s*=\s*\{/);
assert.match(codex.config, /CONTEXT7_API_KEY\s*=\s*"GIT_CTX_API_KEY"/);
assert.doesNotMatch(codex.config, /\$\{GIT_CTX_API_KEY\}/);

const claude = entries.find((entry) => entry.id === "claude-code");
assert.equal(claude.file, ".mcp.json");
assert.deepEqual(JSON.parse(claude.config).mcpServers["git-ctx"], {
  type: "http",
  url: "https://git-ctx.company.local/mcp",
  headers: { CONTEXT7_API_KEY: "${GIT_CTX_API_KEY}" },
});

const cursor = entries.find((entry) => entry.id === "cursor");
assert.equal(cursor.file, ".cursor/mcp.json");
assert.deepEqual(JSON.parse(cursor.config).mcpServers["git-ctx"], {
  url: "https://git-ctx.company.local/mcp",
  headers: { CONTEXT7_API_KEY: "${env:GIT_CTX_API_KEY}" },
});

const vscode = entries.find((entry) => entry.id === "vscode");
const vscodeConfig = JSON.parse(vscode.config);
assert.equal(vscode.file, ".vscode/mcp.json");
assert.deepEqual(vscodeConfig.inputs, [
  {
    type: "promptString",
    id: "git-ctx-api-key",
    description: "git-ctx MCP API 키",
    password: true,
  },
]);
assert.deepEqual(vscodeConfig.servers["git-ctx"], {
  type: "http",
  url: "https://git-ctx.company.local/mcp",
  headers: { CONTEXT7_API_KEY: "${input:git-ctx-api-key}" },
});

const customEndpoint = clients.forEndpoint("http://localhost:4747/custom/mcp/");
assert.match(customEndpoint[0].config, /http:\/\/localhost:4747\/custom\/mcp/);
assert.doesNotMatch(customEndpoint[0].config, /custom\/mcp\//);

for (const invalid of [
  "",
  "git-ctx.company.local/mcp",
  "file:///tmp/mcp",
  "https://user:secret@git-ctx.company.local/mcp",
  "https://git-ctx.company.local/mcp?api_key=secret",
  "https://git-ctx.company.local/mcp#secret",
]) {
  assert.throws(() => clients.configurations(invalid), TypeError, invalid);
}

const commands = clients.environmentCommands();
assert.deepEqual(commands.map((command) => command.id), ["posix", "powershell"]);
for (const command of commands) {
  assert.deepEqual(Object.keys(command).sort(), ["command", "id", "label"]);
  assert.match(command.command, /GIT_CTX_API_KEY/);
  assert.match(command.command, /생성 직후 표시된 MCP API 키/);
  assert.doesNotMatch(command.command, /bctx_(?:live|test)_/i);
}

console.log("MCP client configuration tests passed");
