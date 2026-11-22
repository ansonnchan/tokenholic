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

// The pinned ccusage build for each platform. tokenholic runs it to count
// agents it has no reader of its own for. The version is fixed in
// package.json and the executable's digest is compiled into tokenholic,
// which refuses to run any other file.
const ccusageByTarget = Object.freeze({
  "darwin-arm64": "@ccusage/ccusage-darwin-arm64",
  "darwin-x64": "@ccusage/ccusage-darwin-x64",
  "linux-arm64": "@ccusage/ccusage-linux-arm64",
  "linux-x64": "@ccusage/ccusage-linux-x64",
  "win32-arm64": "@ccusage/ccusage-win32-arm64",
  "win32-x64": "@ccusage/ccusage-win32-x64",
});

// The variable that tells tokenholic where that executable was installed.
const ccusagePathVariable = "TOKENHOLIC_CCUSAGE_PATH";

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

// resolveCcusage returns where the pinned ccusage executable was installed,
// or undefined when it was not. Its absence is not an error here: tokenholic
// reports the agents it then cannot count.
function resolveCcusage(platform, arch, resolver = require.resolve) {
  const packageName = ccusageByTarget[`${platform}-${arch}`];
  if (!packageName) {
    return undefined;
  }
  try {
    const manifest = resolver(`${packageName}/package.json`);
    const executable = path.join(path.dirname(manifest), "bin", platform === "win32" ? "ccusage.exe" : "ccusage");
    if (platform !== "win32" && (fs.statSync(executable).mode & 0o111) === 0) {
      // Some installers drop the mode bits the package was published with.
      fs.chmodSync(executable, 0o755);
    }
    return fs.existsSync(executable) ? executable : undefined;
  } catch {
    return undefined;
  }
}

// childEnvironment is the caller's environment plus the ccusage location.
// A location the caller already set is left alone.
function childEnvironment(environment, ccusage) {
  if (!ccusage || environment[ccusagePathVariable]) {
    return environment;
  }
  return { ...environment, [ccusagePathVariable]: ccusage };
}

function run(argv, options = {}) {
  const platform = options.platform ?? process.platform;
  const arch = options.arch ?? process.arch;
  const stderr = options.stderr ?? process.stderr;
  const spawn = options.spawn ?? childProcess.spawnSync;
  try {
    const executable = resolveExecutable(platform, arch, options.resolve);
    const result = spawn(executable, argv, {
      env: childEnvironment(options.env ?? process.env, resolveCcusage(platform, arch, options.resolve)),
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

module.exports = {
  ccusageByTarget,
  ccusagePathVariable,
  childEnvironment,
  packageByTarget,
  resolveCcusage,
  resolveExecutable,
  run,
  targetPackage,
};
