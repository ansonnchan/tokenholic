import { readFile } from "node:fs/promises";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";

import { packageDirectory, platforms } from "./platforms.mjs";

// Every native package goes out before the launcher that depends on them.
const packageDirectories = [...platforms.map(packageDirectory), "cli"];

export async function publishNpmRelease(workspaceRoot, runner = spawnSync, output = process.stdout) {
  for (const directory of packageDirectories) {
    const packageRoot = path.join(path.resolve(workspaceRoot), "packages", directory);
    const manifest = JSON.parse(await readFile(path.join(packageRoot, "package.json"), "utf8"));
    const specification = `${manifest.name}@${manifest.version}`;
    const lookup = runner("npm", ["view", specification, "version", "--json"], {
      encoding: "utf8",
      shell: false,
    });
    if (lookup.status === 0) {
      if (JSON.parse(lookup.stdout) !== manifest.version) {
        throw new Error(`npm returned an unexpected version for ${manifest.name}`);
      }
      output.write(`already published ${specification}\n`);
      continue;
    }
    if (!lookup.stderr?.includes("E404")) {
      throw new Error(`could not query npm for ${manifest.name}`);
    }
    const publish = runner("npm", ["publish", packageRoot, "--access", "public", "--provenance"], {
      encoding: "utf8",
      shell: false,
      stdio: "inherit",
    });
    if (publish.status !== 0) {
      throw new Error(`could not publish ${manifest.name}`);
    }
    output.write(`published ${specification}\n`);
  }
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  await publishNpmRelease(process.argv[2] ?? ".");
}
