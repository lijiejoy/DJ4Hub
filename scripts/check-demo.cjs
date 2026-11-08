#!/usr/bin/env node
"use strict";

const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const net = require("node:net");
const { spawn } = require("node:child_process");
const { once } = require("node:events");

async function main() {
  const root = path.resolve(__dirname, "..");
  const binary = path.join(root, "dist/dj4ghub-macos");
  if (!fs.existsSync(binary)) throw new Error("Build the service with make build first.");
  const socket = net.createServer().listen(0, "127.0.0.1");
  await once(socket, "listening");
  const port = socket.address().port;
  await new Promise(resolve => socket.close(resolve));
  const home = fs.mkdtempSync(path.join(os.tmpdir(), "dj4hub-demo-check-"));
  const child = spawn(binary, ["-demo", "-listen", `127.0.0.1:${port}`], {
    env: { ...process.env, HOME: home }, stdio: "ignore",
  });
  const exited = once(child, "exit");
  try {
    const base = `http://127.0.0.1:${port}`;
    let ready = false;
    for (let attempt = 0; attempt < 40; attempt++) {
      try {
        const response = await fetch(base + "/api/health", { signal: AbortSignal.timeout(1000) });
        const data = await response.json();
        if (data.ok && data.demo) { ready = true; break; }
      } catch {}
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    if (!ready) throw new Error("Isolated demo did not become ready.");
    const html = await (await fetch(base)).text();
    for (const id of ["overview", "sms", "calls", "esim", "network", "radio", "tasks", "notify", "at"]) {
      if (!html.includes(`id="${id}"`)) throw new Error(`Missing embedded view: ${id}`);
    }
    for (const endpoint of ["/app.js", "/style.css", "/app-icon.png", "/api/health",
      "/api/sip/status", "/api/ussd/status", "/api/radio/operators", "/api/tasks"]) {
      const response = await fetch(base + endpoint, { signal: AbortSignal.timeout(3000) });
      if (!response.ok) throw new Error(`${endpoint}: HTTP ${response.status}`);
      await response.arrayBuffer();
    }
    // Demo returns fixed candidates; a real idle modem uses state=idle.
    const scan = await (await fetch(base + "/api/radio/operators")).json();
    if (scan.demo !== true || scan.state !== "done") throw new Error("Demo scan fixture is missing.");
    const blocked = await fetch(base + "/api/health", { headers: { Origin: "https://example.invalid" } });
    if (blocked.status !== 403) throw new Error("Origin policy is missing.");
    console.log("PASS: isolated demo, nine embedded views, assets, read-only APIs and origin protection.");
  } finally {
    child.kill("SIGTERM");
    const timer = setTimeout(() => child.kill("SIGKILL"), 5000);
    try { await exited; } finally {
      clearTimeout(timer);
      fs.rmSync(home, { recursive: true, force: true });
    }
  }
}

main().catch(error => { console.error(error.message); process.exitCode = 1; });
