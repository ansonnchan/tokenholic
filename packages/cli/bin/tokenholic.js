#!/usr/bin/env node
"use strict";

const fs = require("node:fs");
const path = require("node:path");
const childProcess = require("node:child_process");

const packageByTarget = Object.freeze({
  "darwin-arm64": "@tokenholic/darwin-arm64",
  "darwin-x64": "@tokenholic/darwin-x64",
  "linux-arm64": "@tokenholic/linux-arm64",
  "linux-x64": "@tokenholic/linux-x64",
  "win32-arm64": "@tokenholic/win32-arm64",
  "win32-x64": "@tokenholic/win32-x64",
});

function targetPackage(platform, arch) {
  const packageName = packageByTarget[`${platform}-${arch}`];
  if (!packageName) {
    throw new Error(
      `tokenholic does not support ${platform}/${arch}. Supported targets: macOS, Linux, and Windows, each on arm64 and x64.`,
    );
  }
  return packageName;
}

function resolveExecutable(platform, arch, resolver = require.resolve) {
  const packageName = targetPackage(platform, arch);
  let manifest;
  try {
    manifest = resolver(`${packageName}/package.json`);
  } catch {
    throw new Error(
      `The native package ${packageName} is missing. Reinstall @tokenholic/cli for this platform.`,
    );
  }
  // Windows runs a program only when its name says it is one.
  const binary = platform === "win32" ? "tokenholic.exe" : "tokenholic";
  const executable = path.join(path.dirname(manifest), "bin", binary);
  if (!fs.existsSync(executable)) {
    throw new Error(`The native package ${packageName} is incomplete. Reinstall @tokenholic/cli.`);
  }
  return executable;
}

function run(argv, options = {}) {
  const platform = options.platform ?? process.platform;
  const arch = options.arch ?? process.arch;
  const stderr = options.stderr ?? process.stderr;
  const spawn = options.spawn ?? childProcess.spawnSync;
  try {
    const executable = resolveExecutable(platform, arch, options.resolve);
    const result = spawn(executable, argv, {
      env: process.env,
      stdio: "inherit",
      shell: false,
    });
    if (result.error) {
      throw result.error;
    }
    if (result.signal) {
      stderr.write(`tokenholic stopped after signal ${result.signal}.\n`);
      return 1;
    }
    return result.status ?? 1;
  } catch (error) {
    stderr.write(`${error.message}\n`);
    return 1;
  }
}

if (require.main === module) {
  process.exitCode = run(process.argv.slice(2));
}

module.exports = { packageByTarget, resolveExecutable, run, targetPackage };
