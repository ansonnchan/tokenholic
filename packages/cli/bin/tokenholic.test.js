"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const { resolveExecutable, run, targetPackage } = require("./tokenholic.js");

test("selects each supported platform package", () => {
  assert.equal(targetPackage("darwin", "arm64"), "@tokenholic/darwin-arm64");
  assert.equal(targetPackage("darwin", "x64"), "@tokenholic/darwin-x64");
  assert.equal(targetPackage("linux", "arm64"), "@tokenholic/linux-arm64");
  assert.equal(targetPackage("linux", "x64"), "@tokenholic/linux-x64");
  assert.throws(() => targetPackage("linux", "riscv64"), /does not support linux\/riscv64/);
  assert.equal(targetPackage("win32", "arm64"), "@tokenholic/win32-arm64");
  assert.equal(targetPackage("win32", "x64"), "@tokenholic/win32-x64");
  assert.throws(() => targetPackage("freebsd", "x64"), /Supported targets: macOS, Linux, and Windows/);
});

test("resolves the binary beside the platform manifest", () => {
  const executable = resolveExecutable("darwin", "arm64", () =>
    path.join(__dirname, "fixture", "package.json"),
  );
  assert.equal(executable, path.join(__dirname, "fixture", "bin", "tokenholic"));
});

test("resolves the Windows binary by its own name", (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "tokenholic-launcher-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.mkdirSync(path.join(root, "bin"));
  fs.writeFileSync(path.join(root, "bin", "tokenholic.exe"), "synthetic");
  const resolve = () => path.join(root, "package.json");
  assert.equal(resolveExecutable("win32", "x64", resolve), path.join(root, "bin", "tokenholic.exe"));
  // A package holding only the other system's file name is incomplete.
  assert.throws(() => resolveExecutable("linux", "x64", resolve), /is incomplete/);
});

test("forwards arguments without a shell", () => {
  const calls = [];
  const status = run(["sync", "--dry-run"], {
    platform: "darwin",
    arch: "arm64",
    resolve: () => path.join(__dirname, "fixture", "package.json"),
    spawn: (executable, args, options) => {
      calls.push({ executable, args, options });
      return { status: 7 };
    },
  });
  assert.equal(status, 7);
  assert.deepEqual(calls[0].args, ["sync", "--dry-run"]);
  assert.equal(calls[0].options.shell, false);
  assert.equal(calls[0].options.stdio, "inherit");
});

test("reports unsupported targets without spawning", () => {
  let message = "";
  const status = run([], {
    platform: "freebsd",
    arch: "x64",
    stderr: { write: (value) => (message += value) },
    spawn: () => assert.fail("spawn should not run"),
  });
  assert.equal(status, 1);
  assert.match(message, /does not support freebsd\/x64/);
});
