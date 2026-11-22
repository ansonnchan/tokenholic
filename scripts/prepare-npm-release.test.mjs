import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { chmod, mkdir, mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

import { archiveName, binaryName, ccusagePackageName, packageDirectory, packageName, platforms } from "./platforms.mjs";
import { parseChecksums, prepareNpmRelease } from "./prepare-npm-release.mjs";

test("parses strict checksum manifests", () => {
  const hash = "a".repeat(64);
  assert.equal(parseChecksums(`${hash}  archive.tar.gz\n`).get("archive.tar.gz"), hash);
  assert.throws(() => parseChecksums(`${hash} archive.tar.gz\n`), /invalid or duplicate/);
  assert.throws(() => parseChecksums(`${hash}  archive.tar.gz\n${hash}  archive.tar.gz\n`), /invalid or duplicate/);
});

test("rejects invalid release versions before filesystem changes", async () => {
  await assert.rejects(prepareNpmRelease("v1.2.3", "missing", "missing"), /semantic version/);
});

async function syntheticRelease(t, { optionalDependencies, license = "synthetic-license" } = {}) {
  const root = await mkdtemp(path.join(tmpdir(), "tokenholic-npm-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const artifacts = path.join(root, "artifacts");
  const workspace = path.join(root, "workspace");
  await mkdir(artifacts, { recursive: true });
  const sums = [];
  for (const platform of platforms) {
    const directory = packageDirectory(platform);
    const input = path.join(root, `input-${directory}`);
    await mkdir(input);
    const binary = binaryName(platform);
    await writeFile(path.join(input, binary), `synthetic-${directory}`);
    await chmod(path.join(input, binary), 0o755);
    await writeFile(path.join(input, "ccusage.lock.json"), "synthetic-lock");
    const name = archiveName(platform, "1.2.3");
    const archive = path.join(artifacts, name);
    const packed = spawnSync("tar", ["-czf", archive, "-C", input, binary, "ccusage.lock.json"], { shell: false });
    assert.equal(packed.status, 0);
    const hash = createHash("sha256").update(await readFile(archive)).digest("hex");
    sums.push(`${hash}  ${name}`);

    const packageRoot = path.join(workspace, "packages", directory);
    await mkdir(packageRoot, { recursive: true });
    await writeFile(path.join(packageRoot, "package.json"), JSON.stringify({ name: packageName(platform), version: "development" }));
  }
  await writeFile(path.join(artifacts, "SHA256SUMS"), `${sums.join("\n")}\n`);
  if (license !== null) {
    await writeFile(path.join(workspace, "LICENSE"), license);
  }

  const cliDirectory = path.join(workspace, "packages", "cli");
  await mkdir(cliDirectory, { recursive: true });
  await writeFile(path.join(cliDirectory, "package.json"), JSON.stringify({
    name: "@tokenholic/cli",
    version: "development",
    // Deliberately unsorted: preparation orders them.
    optionalDependencies: optionalDependencies ?? Object.fromEntries([
      ...platforms.map((platform) => [packageName(platform), "workspace:*"]).reverse(),
      ...platforms.map((platform) => [ccusagePackageName(platform), "20.0.51"]),
    ]),
  }));
  return { artifacts, workspace, cliDirectory };
}

test("prepares exact platform and umbrella packages", async (t) => {
  const { artifacts, workspace, cliDirectory } = await syntheticRelease(t);
  await prepareNpmRelease("1.2.3", artifacts, workspace);
  const umbrella = JSON.parse(await readFile(path.join(cliDirectory, "package.json"), "utf8"));
  assert.equal(umbrella.version, "1.2.3");
  // The native packages take the release version; the pinned ccusage
  // packages keep the exact version they were reviewed at.
  assert.deepEqual(Object.entries(umbrella.optionalDependencies), [
    ["@ccusage/ccusage-darwin-arm64", "20.0.51"],
    ["@ccusage/ccusage-darwin-x64", "20.0.51"],
    ["@ccusage/ccusage-linux-arm64", "20.0.51"],
    ["@ccusage/ccusage-linux-x64", "20.0.51"],
    ["@ccusage/ccusage-win32-arm64", "20.0.51"],
    ["@ccusage/ccusage-win32-x64", "20.0.51"],
    ["@tokenholic/darwin-arm64", "1.2.3"],
    ["@tokenholic/darwin-x64", "1.2.3"],
    ["@tokenholic/linux-arm64", "1.2.3"],
    ["@tokenholic/linux-x64", "1.2.3"],
    ["@tokenholic/win32-arm64", "1.2.3"],
    ["@tokenholic/win32-x64", "1.2.3"],
  ]);
  for (const platform of platforms) {
    const directory = packageDirectory(platform);
    const binary = path.join(workspace, "packages", directory, "bin", binaryName(platform));
    assert.equal(await readFile(binary, "utf8"), `synthetic-${directory}`);
    assert.notEqual((await stat(binary)).mode & 0o111, 0);
    const manifest = JSON.parse(await readFile(path.join(workspace, "packages", directory, "package.json"), "utf8"));
    assert.equal(manifest.version, "1.2.3");
    assert.equal(await readFile(path.join(workspace, "packages", directory, "LICENSE"), "utf8"), "synthetic-license");
  }
  assert.equal(await readFile(path.join(cliDirectory, "LICENSE"), "utf8"), "synthetic-license");
});

test("refuses a release whose workspace has no license to ship", async (t) => {
  const { artifacts, workspace } = await syntheticRelease(t, { license: null });
  await assert.rejects(prepareNpmRelease("1.2.3", artifacts, workspace), /no LICENSE/);
  for (const platform of platforms) {
    // It stops before any package is touched.
    await assert.rejects(stat(path.join(workspace, "packages", packageDirectory(platform), "bin")), { code: "ENOENT" });
  }
});

test("refuses a launcher that does not depend on every platform package", async (t) => {
  const { artifacts, workspace } = await syntheticRelease(t, {
    optionalDependencies: { "@tokenholic/darwin-arm64": "workspace:*", "@tokenholic/darwin-x64": "workspace:*" },
  });
  await assert.rejects(prepareNpmRelease("1.2.3", artifacts, workspace), /exactly the supported platform packages/);
});

test("refuses a launcher without the pinned ccusage packages", async (t) => {
  const { artifacts, workspace } = await syntheticRelease(t, {
    optionalDependencies: Object.fromEntries(platforms.map((platform) => [packageName(platform), "workspace:*"])),
  });
  await assert.rejects(prepareNpmRelease("1.2.3", artifacts, workspace), /pinned ccusage packages/);
});

test("refuses a ccusage dependency that is a range rather than one version", async (t) => {
  for (const range of ["^20.0.51", "~20.0.51", "20", "latest", ">=20.0.51", "20.0.x", "workspace:*"]) {
    const { artifacts, workspace } = await syntheticRelease(t, {
      optionalDependencies: Object.fromEntries([
        ...platforms.map((platform) => [packageName(platform), "workspace:*"]),
        ...platforms.map((platform, index) => [ccusagePackageName(platform), index === 3 ? range : "20.0.51"]),
      ]),
    });
    await assert.rejects(prepareNpmRelease("1.2.3", artifacts, workspace), /must be pinned to one exact version/, range);
  }
});

test("refuses a release that is missing a platform archive", async (t) => {
  const { artifacts, workspace } = await syntheticRelease(t);
  const sums = (await readFile(path.join(artifacts, "SHA256SUMS"), "utf8")).trim().split("\n");
  await writeFile(path.join(artifacts, "SHA256SUMS"), `${sums.slice(1).join("\n")}\n`);
  await assert.rejects(prepareNpmRelease("1.2.3", artifacts, workspace), /exactly the supported platform archives/);
});
