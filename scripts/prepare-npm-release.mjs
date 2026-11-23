import { createHash } from "node:crypto";
import { chmod, copyFile, mkdtemp, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";

import { archiveName as platformArchive, binaryName, ccusagePackageName, packageDirectory, packageName, platforms } from "./platforms.mjs";

const semanticVersion = /^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;

// A dependency version that names one release: digits and dots only.
const exactVersion = /^[0-9]+\.[0-9]+\.[0-9]+$/;

export function parseChecksums(contents) {
  const checksums = new Map();
  for (const line of contents.trim().split("\n")) {
    const match = /^([0-9a-f]{64})  ([A-Za-z0-9._-]+)$/.exec(line);
    if (!match || checksums.has(match[2])) {
      throw new Error("SHA256SUMS contains an invalid or duplicate entry");
    }
    checksums.set(match[2], match[1]);
  }
  return checksums;
}

// npm shows a package's README on its page but cannot follow links into the
// repository, so each relative link is pinned to the tag being released.
// Images point at the file itself; everything else points at its page.
export function npmReadme(readme, repository, version) {
  const page = `${repository}/blob/v${version}/`;
  const file = `${repository.replace("https://github.com/", "https://raw.githubusercontent.com/")}/v${version}/`;
  const pin = (base) => (match, before, target, after) =>
    /^(?:[a-z][a-z0-9+.-]*:|#|\/\/)/i.test(target) ? match : `${before}${base}${target.replace(/^\.?\//, "")}${after}`;
  return readme
    .replace(/(!\[[^\]]*\]\()([^)\s]+)(\))/g, pin(file))
    .replace(/(\bsrc=")([^"]+)(")/g, pin(file))
    .replace(/(\]\()([^)\s]+)(\))/g, pin(page))
    .replace(/(\bhref=")([^"]+)(")/g, pin(page));
}

// A native package is installed for the user by the launcher; its page says
// so and sends a reader there.
export function platformReadme(platform, repository) {
  return [
    `# ${packageName(platform)}`,
    "",
    `The tokenholic program built for ${platform.label}.`,
    "",
    "This package is not meant to be installed on its own. Install",
    "[`@tokenholic/cli`](https://www.npmjs.com/package/@tokenholic/cli), which",
    "picks the package that matches your machine:",
    "",
    "```sh",
    "npx @tokenholic/cli",
    "```",
    "",
    `Source, documentation, and issues: ${repository}`,
    "",
  ].join("\n");
}

function repositoryPage(manifest) {
  const match = /^git\+(https:\/\/github\.com\/[\w.-]+\/[\w.-]+?)\.git$/.exec(manifest.repository?.url ?? "");
  if (!match) {
    throw new Error("@tokenholic/cli must name its GitHub repository so the README links can be pinned");
  }
  return match[1];
}

export async function prepareNpmRelease(version, artifactDirectory, workspaceRoot) {
  if (!semanticVersion.test(version)) {
    throw new Error("npm release version must be semantic version text without a v prefix");
  }
  const artifactRoot = path.resolve(artifactDirectory);
  const workspace = path.resolve(workspaceRoot);
  const checksums = parseChecksums(await readFile(path.join(artifactRoot, "SHA256SUMS"), "utf8"));
  if (checksums.size !== platforms.length) {
    throw new Error("SHA256SUMS must contain exactly the supported platform archives");
  }
  // Every package carries the license text; npm packs a LICENSE file whatever
  // the manifest's "files" says. A release without it must not publish.
  let license;
  try {
    license = await readFile(path.join(workspace, "LICENSE"));
  } catch {
    throw new Error("the workspace has no LICENSE to ship in the npm packages");
  }
  // Every package page shows a README, which npm also packs whatever "files"
  // says: the repository's own for the launcher, a pointer for the rest.
  let readme;
  try {
    readme = await readFile(path.join(workspace, "README.md"), "utf8");
  } catch {
    throw new Error("the workspace has no README.md to ship in the npm packages");
  }
  const umbrellaPath = path.join(workspace, "packages", "cli", "package.json");
  const umbrella = JSON.parse(await readFile(umbrellaPath, "utf8"));
  const repository = repositoryPage(umbrella);
  const temporary = await mkdtemp(path.join(tmpdir(), "tokenholic-npm-"));
  try {
    for (const platform of platforms) {
      const directory = packageDirectory(platform);
      const archiveName = platformArchive(platform, version);
      const archivePath = path.join(artifactRoot, archiveName);
      const expected = checksums.get(archiveName);
      if (!expected) {
        throw new Error(`SHA256SUMS is missing ${archiveName}`);
      }
      const actual = createHash("sha256").update(await readFile(archivePath)).digest("hex");
      if (actual !== expected) {
        throw new Error(`checksum mismatch for ${archiveName}`);
      }

      const extracted = path.join(temporary, directory);
      await mkdir(extracted);
      const result = spawnSync("tar", ["-xzf", archivePath, "-C", extracted], { encoding: "utf8", shell: false });
      if (result.error) {
        throw new Error(`could not execute the archive tool for ${archiveName}`);
      }
      if (result.status !== 0) {
        throw new Error(`could not extract ${archiveName}: ${(result.stderr ?? "").trim()}`);
      }
      const binary = binaryName(platform);
      const names = (await readdir(extracted)).sort();
      if (names.join("\n") !== `ccusage.lock.json\n${binary}`) {
        throw new Error(`${archiveName} contains unexpected files`);
      }

      const packageRoot = path.join(workspace, "packages", directory);
      const binaryDirectory = path.join(packageRoot, "bin");
      await mkdir(binaryDirectory, { recursive: true });
      await copyFile(path.join(extracted, binary), path.join(binaryDirectory, binary));
      await chmod(path.join(binaryDirectory, binary), 0o755);
      await copyFile(path.join(extracted, "ccusage.lock.json"), path.join(packageRoot, "ccusage.lock.json"));
      await writeFile(path.join(packageRoot, "LICENSE"), license);
      await writeFile(path.join(packageRoot, "README.md"), platformReadme(platform, repository), "utf8");
      await setPackageVersion(path.join(packageRoot, "package.json"), version);
    }

    const native = platforms.map((platform) => `@tokenholic/${packageDirectory(platform)}`);
    const pinned = platforms.map(ccusagePackageName);
    const dependencies = umbrella.optionalDependencies ?? {};
    if (Object.keys(dependencies).sort().join("\n") !== [...native, ...pinned].sort().join("\n")) {
      // A launcher that cannot install one of its binaries must not publish.
      throw new Error("@tokenholic/cli must depend on exactly the supported platform packages and the pinned ccusage packages");
    }
    for (const name of pinned) {
      // ccusage is third-party code: one exact release, never a range.
      if (!exactVersion.test(dependencies[name])) {
        throw new Error(`${name} must be pinned to one exact version`);
      }
    }
    umbrella.version = version;
    umbrella.optionalDependencies = Object.fromEntries(
      Object.keys(dependencies).sort().map((name) => [name, native.includes(name) ? version : dependencies[name]]),
    );
    await writeManifest(umbrellaPath, umbrella);
    await writeFile(path.join(workspace, "packages", "cli", "LICENSE"), license);
    await writeFile(path.join(workspace, "packages", "cli", "README.md"), npmReadme(readme, repository, version), "utf8");
  } finally {
    await rm(temporary, { recursive: true, force: true });
  }
}

async function setPackageVersion(manifestPath, version) {
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  manifest.version = version;
  await writeManifest(manifestPath, manifest);
}

async function writeManifest(manifestPath, manifest) {
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`, "utf8");
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const [version, artifactDirectory = "dist", workspaceRoot = "."] = process.argv.slice(2);
  await prepareNpmRelease(version, artifactDirectory, workspaceRoot);
}
