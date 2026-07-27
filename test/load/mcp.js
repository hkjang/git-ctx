import http from "k6/http";
import { check, sleep } from "k6";

export const options = {
  scenarios: {
    concurrent_mcp: {
      executor: "constant-vus",
      vus: 50,
      duration: __ENV.DURATION || "2m",
    },
  },
  thresholds: {
    http_req_failed: ["rate<0.01"],
    http_req_duration: ["p(95)<3000"],
    checks: ["rate>0.99"],
  },
};

const endpoint = `${__ENV.GIT_CTX_URL}/mcp`;
const headers = {
  "Content-Type": "application/json",
  Accept: "application/json, text/event-stream",
  "MCP-Protocol-Version": "2025-06-18",
  CONTEXT7_API_KEY: __ENV.GIT_CTX_API_KEY,
};

export default function () {
  const resolve = http.post(endpoint, JSON.stringify({
    jsonrpc: "2.0", id: `${__VU}-resolve`, method: "tools/call",
    params: {name: "resolve-library-id", arguments: {
      libraryName: __ENV.LIBRARY_NAME || "demo",
      query: __ENV.QUERY || "API usage and implementation example",
    }},
  }), {headers});
  check(resolve, {
    "resolve HTTP 200": r => r.status === 200,
    "resolve MCP success": r => r.json("result.isError") === false,
  });

  const query = http.post(endpoint, JSON.stringify({
    jsonrpc: "2.0", id: `${__VU}-query`, method: "tools/call",
    params: {name: "query-docs", arguments: {
      libraryId: __ENV.LIBRARY_ID || "/kcb/demo/main",
      query: __ENV.QUERY || "API usage and implementation example",
    }},
  }), {headers});
  check(query, {
    "query HTTP 200": r => r.status === 200,
    "query MCP success": r => r.json("result.isError") === false,
    "query has text": r => String(r.json("result.content.0.text") || "").length > 0,
  });
  sleep(0.2);
}
