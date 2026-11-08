#!/usr/bin/env node
"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { execFileSync } = require("node:child_process");

const forbidden = /(?:^|\/)(?:dist|output|node_modules|\.playwright-cli|\.zwork|recordings)(?:\/|$)|(?:^|\/)(?:\.env(?:\..*)?|sip-password|mcp-token|sip-config\.json|notifications\.json|radio-prefs\.json|tasks\.json)$|\.(?:p12|pfx|key|log|pid|har|pcap|sqlite3?)$/i;
const secrets = [
  /-----BEGIN (?:RSA |EC |OPENSSH |ENCRYPTED )?PRIVATE KEY-----/,
  /\bgh[pousr]_[A-Za-z0-9]{36,}\b/,
  /\bgithub_pat_[A-Za-z0-9_]{60,}\b/,
  /\bAKIA[0-9A-Z]{16}\b/,
  /\b[0-9]{8,12}:[A-Za-z0-9_-]{35}\b/,
];
const localPath = /\/Users\/(?!Shared\/|example\/|USERNAME\/)[A-Za-z0-9._-]+\//;
const executableMagic = new Set(["cffaedfe", "cefaedfe", "feedface", "feedfacf", "cafebabe", "bebafeca", "7f454c46"]);

function problemsFor(filename, bytes) {
  const problems = [];
  if (filename !== ".env.example" && forbidden.test(filename)) problems.push("runtime data or credential file");
  if (bytes.length > 20 * 1024 * 1024) problems.push("file exceeds 20 MiB");
  if (executableMagic.has(bytes.subarray(0, 4).toString("hex"))) problems.push("compiled executable");
  if (!bytes.subarray(0, 8192).includes(0)) {
    const text = bytes.toString("utf8");
    if (secrets.some(pattern => pattern.test(text))) problems.push("possible credential or private key");
    if (localPath.test(text)) problems.push("personal macOS home path");
  }
  return problems;
}

function main() {
  const root = execFileSync("git", ["rev-parse", "--show-toplevel"], { encoding: "utf8" }).trim();
  const names = execFileSync("git", ["ls-files", "--cached", "--others", "--exclude-standard", "-z"], {
    cwd: root, encoding: "utf8", maxBuffer: 16 * 1024 * 1024,
  }).split("\0").filter(Boolean);
  let checked = 0;
  let failures = 0;
  for (const name of new Set(names)) {
    const absolute = path.join(root, name);
    if (!fs.existsSync(absolute)) continue; // A deletion may not yet be staged.
    const stat = fs.lstatSync(absolute);
    if (stat.isDirectory()) continue; // Git submodule.
    if (stat.isSymbolicLink()) {
      console.error(`${name}: inspect symlink before publishing`);
      failures++;
      continue;
    }
    checked++;
    const problems = problemsFor(name, fs.readFileSync(absolute));
    if (problems.length) {
      console.error(`${name}: ${problems.join(", ")}`);
      failures++;
    }
  }
  console.log(`Repository check: ${checked} candidate files; ${failures} issues.`);
  if (failures) process.exitCode = 1;
}

module.exports = { problemsFor };
if (require.main === module) main();
