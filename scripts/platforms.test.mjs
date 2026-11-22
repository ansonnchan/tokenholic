import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import { createRequire } from "node:module";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { archiveName, packageDirectory, packageName, platforms } from "./platforms.mjs";

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

test("the umbrella package depends on exactly the shipped platforms", async () => {
  const manifest = JSON.parse(await readFile(path.join(repository, "packages", "cli", "package.json"), "utf8"));
  assert.deepEqual(Object.keys(manifest.optionalDependencies).sort(), platforms.map(packageName).sort());
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
  }
});

test("archive names follow the release tool's convention", () => {
  assert.deepEqual(platforms.map((platform) => archiveName(platform, "1.2.3")), [
    "tokenholic_1.2.3_darwin_arm64.tar.gz",
    "tokenholic_1.2.3_darwin_x64.tar.gz",
    "tokenholic_1.2.3_linux_arm64.tar.gz",
    "tokenholic_1.2.3_linux_x64.tar.gz",
  ]);
});
