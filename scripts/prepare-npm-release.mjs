import { createHash } from "node:crypto";
import { chmod, copyFile, mkdtemp, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";

import { archiveName as platformArchive, binaryName, packageDirectory, platforms } from "./platforms.mjs";

const semanticVersion = /^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;

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
      await setPackageVersion(path.join(packageRoot, "package.json"), version);
    }

    const umbrellaPath = path.join(workspace, "packages", "cli", "package.json");
    const umbrella = JSON.parse(await readFile(umbrellaPath, "utf8"));
    const expected = platforms.map((platform) => `@tokenholic/${packageDirectory(platform)}`).sort();
    if (Object.keys(umbrella.optionalDependencies ?? {}).sort().join("\n") !== expected.join("\n")) {
      // A launcher that cannot install one of its binaries must not publish.
      throw new Error("@tokenholic/cli must depend on exactly the supported platform packages");
    }
    umbrella.version = version;
    umbrella.optionalDependencies = Object.fromEntries(
      Object.keys(umbrella.optionalDependencies).sort().map((name) => [name, version]),
    );
    await writeManifest(umbrellaPath, umbrella);
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
