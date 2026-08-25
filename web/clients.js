/**
 * MCP client configuration generator.
 *
 * The only input is the MCP endpoint. API-key material is deliberately never
 * accepted: generated files refer to GIT_CTX_API_KEY or to the client's secure
 * input prompt instead. Keeping this module free of DOM and network access also
 * makes the configuration contract straightforward to test in Node.
 */
(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  root.GitCtxClients = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  const environmentVariable = "GIT_CTX_API_KEY";
  const serverName = "git-ctx";
  const headerName = "CONTEXT7_API_KEY";

  function normalizeEndpoint(endpoint) {
    if (typeof endpoint !== "string" || !endpoint.trim()) {
      throw new TypeError("MCP endpoint is required");
    }

    let parsed;
    try {
      parsed = new URL(endpoint.trim());
    } catch {
      throw new TypeError("MCP endpoint must be an absolute HTTP(S) URL");
    }
    if (parsed.protocol !== "https:" && parsed.protocol !== "http:") {
      throw new TypeError("MCP endpoint must use HTTP or HTTPS");
    }
    // Credentials and query strings do not belong in a reusable connection
    // file. Rejecting them prevents an accidentally pasted secret from being
    // copied into every generated client configuration.
    if (parsed.username || parsed.password || parsed.search || parsed.hash) {
      throw new TypeError(
        "MCP endpoint must not contain credentials, a query string, or a fragment",
      );
    }

    let path = parsed.pathname.replace(/\/+$/, "");
    if (!path) path = "/mcp";
    return `${parsed.origin}${path}`;
  }

  function jsonConfig(value) {
    return `${JSON.stringify(value, null, 2)}\n`;
  }

  function configurations(endpoint) {
    const url = normalizeEndpoint(endpoint);
    const claudeEnvironmentReference = `\${${environmentVariable}}`;
    const claudeHTTPServer = {
      type: "http",
      url,
      headers: { [headerName]: claudeEnvironmentReference },
    };

    return [
      {
        id: "codex",
        label: "Codex",
        file: "~/.codex/config.toml",
        description:
          "Codex CLI와 IDE가 공유하는 config.toml입니다. 헤더 값 대신 환경변수 이름만 참조합니다.",
        config:
          `[mcp_servers.${serverName}]\n` +
          `url = ${JSON.stringify(url)}\n` +
          `env_http_headers = { ${headerName} = ${JSON.stringify(environmentVariable)} }\n`,
      },
      {
        id: "claude-code",
        label: "Claude Code",
        file: ".mcp.json",
        description:
          "프로젝트 공유용 .mcp.json입니다. Claude Code가 headers의 환경변수 표현식을 실행 시 확장합니다.",
        config: jsonConfig({
          mcpServers: { [serverName]: claudeHTTPServer },
        }),
      },
      {
        id: "cursor",
        label: "Cursor",
        file: ".cursor/mcp.json",
        description:
          "프로젝트별 Cursor MCP 설정입니다. 키 원문 없이 GIT_CTX_API_KEY 환경변수를 참조합니다.",
        config: jsonConfig({
          mcpServers: {
            [serverName]: {
              url,
              headers: {
                [headerName]: `\${env:${environmentVariable}}`,
              },
            },
          },
        }),
      },
      {
        id: "vscode",
        label: "VS Code",
        file: ".vscode/mcp.json",
        description:
          "VS Code가 최초 연결 시 API 키를 암호 입력으로 요청하고 보안 저장소에 보관합니다.",
        config: jsonConfig({
          inputs: [
            {
              type: "promptString",
              id: "git-ctx-api-key",
              description: "git-ctx MCP API 키",
              password: true,
            },
          ],
          servers: {
            [serverName]: {
              type: "http",
              url,
              headers: { [headerName]: "${input:git-ctx-api-key}" },
            },
          },
        }),
      },
    ];
  }

  function environmentCommands() {
    const placeholder = "<생성 직후 표시된 MCP API 키>";
    return [
      {
        id: "posix",
        label: "macOS / Linux / WSL",
        command: `export ${environmentVariable}='${placeholder}'`,
      },
      {
        id: "powershell",
        label: "Windows PowerShell",
        command: `$env:${environmentVariable} = \"${placeholder}\"`,
      },
    ];
  }

  return Object.freeze({
    environmentVariable,
    configurations,
    forEndpoint: configurations,
    environmentCommands,
  });
});
