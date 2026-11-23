import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { chmod, mkdir, mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

import { archiveName, binaryName, ccusagePackageName, packageDirectory, packageName, platforms } from "./platforms.mjs";
import { npmReadme, parseChecksums, platformReadme, prepareNpmRelease } from "./prepare-npm-release.mjs";

const repository = "https://github.com/example/tokenholic";

test("parses strict checksum manifests", () => {
  const hash = "a".repeat(64);
  assert.equal(parseChecksums(`${hash}  archive.tar.gz\n`).get("archive.tar.gz"), hash);
  assert.throws(() => parseChecksums(`${hash} archive.tar.gz\n`), /invalid or duplicate/);
  assert.throws(() => parseChecksums(`${hash}  archive.tar.gz\n${hash}  archive.tar.gz\n`), /invalid or duplicate/);
});

test("rejects invalid release versions before filesystem changes", async () => {
  await assert.rejects(prepareNpmRelease("v1.2.3", "missing", "missing"), /semantic version/);
});

async function syntheticRelease(t, { optionalDependencies, license = "synthetic-license", readme = "See [the guide](docs/guide.md).\n", repositoryURL = `git+${repository}.git` } = {}) {
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
  if (readme !== null) {
    await writeFile(path.join(workspace, "README.md"), readme);
  }

  const cliDirectory = path.join(workspace, "packages", "cli");
  await mkdir(cliDirectory, { recursive: true });
  await writeFile(path.join(cliDirectory, "package.json"), JSON.stringify({
    name: "@tokenholic/cli",
    version: "development",
    repository: { type: "git", url: repositoryURL },
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
    assert.equal(
      await readFile(path.join(workspace, "packages", directory, "README.md"), "utf8"),
      platformReadme(platform, repository),
    );
  }
  assert.equal(await readFile(path.join(cliDirectory, "LICENSE"), "utf8"), "synthetic-license");
  // The launcher's page carries the repository's README, pinned to the tag.
  assert.equal(
    await readFile(path.join(cliDirectory, "README.md"), "utf8"),
    `See [the guide](${repository}/blob/v1.2.3/docs/guide.md).\n`,
  );
});

test("pins a README's relative links to the released tag", () => {
  const page = `${repository}/blob/v1.2.3`;
  const file = "https://raw.githubusercontent.com/example/tokenholic/v1.2.3";
  for (const [name, input, want] of [
    ["link", "[guide](docs/guide.md)", `[guide](${page}/docs/guide.md)`],
    ["dotted link", "[guide](./docs/guide.md)", `[guide](${page}/docs/guide.md)`],
    ["rooted link", "[guide](/docs/guide.md)", `[guide](${page}/docs/guide.md)`],
    ["image", "![shot](docs/shot.png)", `![shot](${file}/docs/shot.png)`],
    ["image tag", '<img src="docs/shot.png" width="10">', `<img src="${file}/docs/shot.png" width="10">`],
    ["anchor tag", '<a href="LICENSE">MIT</a>', `<a href="${page}/LICENSE">MIT</a>`],
    [
      "badge that links into the repository",
      "[![badge](https://img.example/b.svg)](LICENSE)",
      `[![badge](https://img.example/b.svg)](${page}/LICENSE)`,
    ],
    ["image inside a link", "[![shot](a.png)](b.md)", `[![shot](${file}/a.png)](${page}/b.md)`],
    ["absolute link", "[site](https://example.com/a)", "[site](https://example.com/a)"],
    ["absolute image tag", '<img src="https://example.com/a.png">', '<img src="https://example.com/a.png">'],
    ["section link", "[Privacy](#privacy)", "[Privacy](#privacy)"],
    ["mail link", "[mail](mailto:someone@example.com)", "[mail](mailto:someone@example.com)"],
    ["plain text", "Run `tokenholic sync` (again).", "Run `tokenholic sync` (again)."],
  ]) {
    assert.equal(npmReadme(input, repository, "1.2.3"), want, name);
  }
});

test("the repository README leaves npm no relative link to follow", async () => {
  const source = await readFile(new URL("../README.md", import.meta.url), "utf8");
  const pinned = npmReadme(source, repository, "1.2.3");
  const targets = [
    ...[...pinned.matchAll(/\]\(([^)\s]+)\)/g)].map((match) => match[1]),
    ...[...pinned.matchAll(/\b(?:src|href)="([^"]+)"/g)].map((match) => match[1]),
  ];
  assert.notEqual(targets.length, 0);
  for (const target of targets) {
    assert.match(target, /^(?:https:\/\/|#)/, target);
  }
  // A reference-style definition would slip past the rewrite.
  assert.doesNotMatch(pinned, /^\[[^\]]+\]:\s/m);
});

test("refuses a release whose workspace has no README to ship", async (t) => {
  const { artifacts, workspace } = await syntheticRelease(t, { readme: null });
  await assert.rejects(prepareNpmRelease("1.2.3", artifacts, workspace), /no README\.md/);
  for (const platform of platforms) {
    await assert.rejects(stat(path.join(workspace, "packages", packageDirectory(platform), "bin")), { code: "ENOENT" });
  }
});

test("refuses a launcher that does not name the repository its links are pinned to", async (t) => {
  for (const repositoryURL of ["", "https://example.com/tokenholic.git", "git+ssh://git@github.com/example/tokenholic.git"]) {
    const { artifacts, workspace } = await syntheticRelease(t, { repositoryURL });
    await assert.rejects(prepareNpmRelease("1.2.3", artifacts, workspace), /must name its GitHub repository/, repositoryURL);
  }
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
