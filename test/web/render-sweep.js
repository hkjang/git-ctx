// Load the console in a real browser and open every administrator screen.
//
// The other tests in this directory read web/app.js and web/index.html as text.
// They cannot see a screen that renders empty, and that is exactly what shipped
// once: two endpoints the console called were never routed, so two cards stayed
// blank and no test noticed. This starts the built server, signs in the way an
// operator does, clicks each entry in the administrator menu, and fails on a
// screen that renders nothing or logs an error.
//
// It needs a built binary and a Chrome; both are skipped over rather than
// failing when they are absent, so the ordinary `node test/web/*.test.js` run
// on a developer machine stays dependency-free.
//
//   go build -tags sqlite_fts5 -o /tmp/git-ctx ./cmd/git-ctx
//   GIT_CTX_BINARY=/tmp/git-ctx node test/web/render-sweep.js

const test = require("node:test");
const assert = require("node:assert/strict");
const { spawn, spawnSync } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const crypto = require("node:crypto");

const BINARY = process.env.GIT_CTX_BINARY || "";
const CHROME = [process.env.CHROME_PATH, "/usr/bin/google-chrome", "/usr/bin/chromium-browser",
  "/usr/bin/chromium", "/snap/bin/chromium"].find((candidate) => candidate && fs.existsSync(candidate));

const missing = !BINARY || !fs.existsSync(BINARY) ? "GIT_CTX_BINARY is not set to a built server"
  : !CHROME ? "no Chrome or Chromium was found" : "";

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function waitFor(what, attempt, tries = 60, gap = 500) {
  for (let index = 0; index < tries; index += 1) {
    try {
      const value = await attempt();
      if (value) return value;
    } catch {
      // not ready yet
    }
    await sleep(gap);
  }
  throw new Error(`timed out waiting for ${what}`);
}

// A minimal DevTools client: one socket, replies matched by id, events kept.
function connect(url) {
  const socket = new WebSocket(url);
  const pending = new Map();
  const events = [];
  let next = 0;
  const ready = new Promise((resolve, reject) => {
    socket.addEventListener("open", () => resolve());
    socket.addEventListener("error", (event) => reject(new Error(`devtools socket failed: ${event.message || ""}`)));
  });
  socket.addEventListener("message", (event) => {
    const message = JSON.parse(event.data);
    if (message.id && pending.has(message.id)) {
      const { resolve } = pending.get(message.id);
      pending.delete(message.id);
      resolve(message);
      return;
    }
    if (message.method) events.push(message);
  });
  const send = (method, params = {}, sessionId) => {
    next += 1;
    const id = next;
    const payload = { id, method, params };
    if (sessionId) payload.sessionId = sessionId;
    socket.send(JSON.stringify(payload));
    return new Promise((resolve) => pending.set(id, { resolve }));
  };
  return { ready, send, events, close: () => socket.close() };
}

// node:test treats any present skip option as a reason, including an empty
// string, so the absence of a reason has to be undefined rather than "".
test("every administrator screen renders without an error", { skip: missing || undefined }, async () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "git-ctx-render-"));
  // The sweep has only ever run against an empty installation, so every screen
  // was checked in the one state where it has nothing to draw. GIT_CTX_SWEEP_DB
  // points it at a database that already holds an estate.
  const seeded = process.env.GIT_CTX_SWEEP_DB || "";
  const databasePath = seeded || path.join(directory, "render.db");
  // The server takes its port from a stored setting rather than the
  // environment, so it always comes up on 4747. Anything already listening
  // there is somebody's own instance; borrowing it would be rude and would
  // make the result meaningless.
  const port = 4747;
  const base = `http://127.0.0.1:${port}`;
  const occupied = await fetch(base + "/", { signal: AbortSignal.timeout(500) })
    .then(() => true).catch(() => false);
  assert.equal(occupied, false,
    `something is already listening on ${port}; stop it before running this sweep`);
  const server = spawn(BINARY, [], {
    cwd: directory,
    env: {
      ...process.env,
      GIT_CTX_DB_DSN: `file:${databasePath}?_foreign_keys=on&_busy_timeout=5000`,
      GIT_CTX_RECOVERY_KEY: crypto.randomBytes(48).toString("base64"),
    },
    stdio: "ignore",
  });
  let browser;
  let client;
  try {
    await waitFor("the server to listen", async () => (await fetch(`${base}/`)).ok);

    const profile = path.join(directory, "chrome");
    browser = spawn(CHROME, ["--headless=new", "--remote-debugging-port=0", "--no-sandbox", "--disable-gpu",
      "--disable-dev-shm-usage", "--remote-allow-origins=*", `--user-data-dir=${profile}`, "about:blank"],
      { stdio: ["ignore", "ignore", "pipe"] });
    // Chrome reports the port it chose on stderr.
    let endpoint = "";
    browser.stderr.on("data", (chunk) => {
      const match = /ws:\/\/[^\s]+/.exec(String(chunk));
      if (match) endpoint = match[0];
    });
    await waitFor("the browser to expose devtools", () => endpoint);

    client = connect(endpoint);
    await client.ready;
    const target = (await client.send("Target.createTarget", { url: "about:blank" })).result.targetId;
    const session = (await client.send("Target.attachToTarget", { targetId: target, flatten: true })).result.sessionId;
    for (const domain of ["Page", "Runtime", "Log"]) await client.send(`${domain}.enable`, {}, session);

    const evaluate = async (expression, awaitPromise = false) => {
      const answer = await client.send("Runtime.evaluate",
        { expression, awaitPromise, returnByValue: true }, session);
      return answer.result?.result?.value;
    };

    await client.send("Page.navigate", { url: `${base}/` }, session);
    await sleep(1500);
    const token = fs.readFileSync(path.join(directory, "backups", "bootstrap-admin.token"), "utf8").trim();
    const status = await evaluate(`fetch('/api/v1/bootstrap/login',{method:'POST',` +
      `headers:{'Content-Type':'application/json'},body:JSON.stringify({token:${JSON.stringify(token)}})})` +
      `.then(r => r.status)`, true);
    assert.equal(status, 200, "the console could not sign in");
    await client.send("Page.reload", {}, session);
    await sleep(2500);

    const entries = JSON.parse(await evaluate(
      `JSON.stringify(Array.from(document.querySelectorAll('[data-admin-target]')).map(b => ({` +
      `target: b.dataset.adminTarget, category: b.dataset.adminCategory || '', label: b.innerText.trim()})))`) || "[]");
    assert.ok(entries.length >= 8, `the administrator menu rendered ${entries.length} entries`);

    for (const entry of entries) {
      client.events.length = 0;
      const clicked = await evaluate(
        `(() => { const button = Array.from(document.querySelectorAll('[data-admin-target]'))` +
        `.find(b => b.dataset.adminTarget === ${JSON.stringify(entry.target)} && ` +
        `(b.dataset.adminCategory || '') === ${JSON.stringify(entry.category)});` +
        ` if (!button) return 'missing'; button.click(); return 'clicked'; })()`);
      assert.equal(clicked, "clicked", `the ${entry.label} entry disappeared`);
      await sleep(1200);
      const panel = JSON.parse(await evaluate(
        `(() => { const panel = document.querySelector('.admin-panel:not([hidden])');` +
        ` return JSON.stringify({id: panel ? panel.id : '', length: panel ? panel.innerText.trim().length : 0}); })()`) || "{}");
      const failures = client.events.filter((event) =>
        event.method === "Runtime.exceptionThrown" ||
        (event.method === "Runtime.consoleAPICalled" && event.params?.type === "error"));
      assert.equal(failures.length, 0,
        `${entry.label} logged ${failures.length} error(s): ${JSON.stringify(failures[0]?.params || {}).slice(0, 300)}`);
      assert.ok(panel.length > 40, `${entry.label} rendered an empty panel (${panel.id})`);
      const text = await evaluate(
        `(() => { const panel = document.querySelector('.admin-panel:not([hidden])');` +
        ` return panel ? panel.innerText : ''; })()`) || "";
      for (const token of ["undefined", "NaN", "[object Object]", "null null", "Invalid Date"]) {
        const at = text.indexOf(token);
        if (at >= 0) {
          assert.fail(`${entry.label} rendered ${token}: ${JSON.stringify(text.slice(Math.max(0, at - 90), at + 60))}`);
        }
      }
    }
  } finally {
    client?.close();
    browser?.kill();
    server.kill();
    spawnSync("sh", ["-c", `rm -rf ${JSON.stringify(directory)}`]);
  }
});
