import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import { createRequire } from "node:module";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { archiveName, binaryName, ccusagePackageName, packageDirectory, packageName, platforms } from "./platforms.mjs";

const repository = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const require = createRequire(import.meta.url);

// The platform list is repeated where a published file cannot import it: the
// launcher, the umbrella manifest, and each native package. These tests are
// what keeps the copies equal.

test("the launcher selects exactly the shipped platforms", () => {
  const { packageByTarget } = require("../packages/cli/bin/tokenholic.js");
  assert.deepEqual(
    Object.entries(packageByTarget).sort(),
    platforms.map((platform) => [packageDirectory(platform), packageName(platform)]).sort(),
  );
});

test("the umbrella package depends on exactly the shipped platforms and their pinned ccusage", async () => {
  const manifest = JSON.parse(await readFile(path.join(repository, "packages", "cli", "package.json"), "utf8"));
  assert.deepEqual(
    Object.keys(manifest.optionalDependencies).sort(),
    [...platforms.map(packageName), ...platforms.map(ccusagePackageName)].sort(),
  );
});

// ccusage is third-party code that reads local agent records. Four files
// name the release tokenholic runs: the lock, the launcher's manifest, the
// lockfile npm installs from, and the launcher's own table. They must agree,
// and the Go contract test holds the compiled digests to the same lock.
test("every copy of the ccusage pin agrees with ccusage.lock.json", async () => {
  const lock = JSON.parse(await readFile(path.join(repository, "ccusage.lock.json"), "utf8"));
  const manifest = JSON.parse(await readFile(path.join(repository, "packages", "cli", "package.json"), "utf8"));
  const lockfile = await readFile(path.join(repository, "pnpm-lock.yaml"), "utf8");
  const { ccusageByTarget } = require("../packages/cli/bin/tokenholic.js");

  assert.match(lock.version, /^[0-9]+\.[0-9]+\.[0-9]+$/);
  assert.deepEqual(Object.keys(lock.platforms).sort(), platforms.map(packageDirectory).sort());
  assert.deepEqual(
    Object.entries(ccusageByTarget).sort(),
    platforms.map((platform) => [packageDirectory(platform), ccusagePackageName(platform)]).sort(),
  );
  for (const platform of platforms) {
    const name = ccusagePackageName(platform);
    const pinned = lock.platforms[packageDirectory(platform)];
    assert.equal(pinned.package, name);
    // One exact release, never a range.
    assert.equal(manifest.optionalDependencies[name], lock.version);
    assert.match(pinned.sha256, /^[0-9a-f]{64}$/);
    const entry = `'${name}@${lock.version}':\n    resolution: {integrity: ${pinned.integrity}}\n`;
    assert.ok(lockfile.includes(entry), `pnpm-lock.yaml does not pin ${name} to the lock's integrity`);
  }
  // Only the pinned release may be resolvable at all.
  assert.equal(lockfile.match(/'@ccusage\/[^']+@[^']+':\n    resolution:/g).length, platforms.length);
});

test("every shipped platform has a matching native package and nothing else does", async () => {
  const directories = (await readdir(path.join(repository, "packages"), { withFileTypes: true }))
    .filter((entry) => entry.isDirectory() && entry.name !== "cli")
    .map((entry) => entry.name);
  assert.deepEqual(directories.sort(), platforms.map(packageDirectory).sort());
  for (const platform of platforms) {
    const manifest = JSON.parse(
      await readFile(path.join(repository, "packages", packageDirectory(platform), "package.json"), "utf8"),
    );
    assert.equal(manifest.name, packageName(platform));
    assert.deepEqual(manifest.os, [platform.os]);
    assert.deepEqual(manifest.cpu, [platform.cpu]);
    assert.ok(manifest.description.includes(platform.label), manifest.description);
    assert.deepEqual(manifest.files.sort(), [`bin/${binaryName(platform)}`, "ccusage.lock.json"]);
  }
});

test("archive names follow the release tool's convention", () => {
  assert.deepEqual(platforms.map((platform) => archiveName(platform, "1.2.3")), [
    "tokenholic_1.2.3_darwin_arm64.tar.gz",
    "tokenholic_1.2.3_darwin_x64.tar.gz",
    "tokenholic_1.2.3_linux_arm64.tar.gz",
    "tokenholic_1.2.3_linux_x64.tar.gz",
    "tokenholic_1.2.3_windows_arm64.tar.gz",
    "tokenholic_1.2.3_windows_x64.tar.gz",
  ]);
});
