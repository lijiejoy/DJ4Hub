"use strict";

const { test } = require("node:test");
const assert = require("node:assert/strict");
const { problemsFor } = require("./check-repository.cjs");

test("blocks publishing credentials, runtime files and executables", () => {
  for (const name of ["dist/app", ".env", "tmp/.env.local", "tasks.json", "private.key", "output/call.wav"]) {
    assert.ok(problemsFor(name, Buffer.from("{}")).length, name);
  }
  for (const text of [
    "-----BEGIN " + "PRIVATE KEY-----",
    "ghp_" + "a".repeat(36),
    "123456789:" + "a".repeat(35),
    "/Users/" + "someone/Library/config",
  ]) {
    assert.ok(problemsFor("source.go", Buffer.from(text)).length);
  }
  assert.ok(problemsFor("service", Buffer.from("cffaedfe", "hex")).includes("compiled executable"));
});

test("keeps source, fake credentials, certificates, examples and image assets", () => {
  for (const [name, text] of [
    ["source_test.go", 'Password: "FAKE-SECRET"'],
    [".env.example", "TOKEN=replace-me"],
    ["public.pem", "-----BEGIN CERTIFICATE-----"],
    ["packaging/macos/service.plist.example", "__HOME__/Library/Logs"],
  ]) {
    assert.deepEqual(problemsFor(name, Buffer.from(text)), []);
  }
  assert.deepEqual(problemsFor("docs/images/icon.png", Buffer.from("89504e470001", "hex")), []);
});
