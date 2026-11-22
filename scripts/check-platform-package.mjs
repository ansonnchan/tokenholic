import { access, readFile } from "node:fs/promises";
import { constants } from "node:fs";
import path from "node:path";

const [directory, expectedOS, expectedCPU] = process.argv.slice(2);
if (!directory || !expectedOS || !expectedCPU) {
  throw new Error("usage: check-platform-package <directory> <os> <cpu>");
}

// Windows runs a program only when its name says it is one.
const binaryName = expectedOS === "win32" ? "tokenholic.exe" : "tokenholic";
const manifest = JSON.parse(await readFile(path.join(directory, "package.json"), "utf8"));
if (
  manifest.os?.length !== 1 || manifest.os[0] !== expectedOS ||
  manifest.cpu?.length !== 1 || manifest.cpu[0] !== expectedCPU ||
  manifest.files?.length !== 2 ||
  !manifest.files.includes(`bin/${binaryName}`) || !manifest.files.includes("ccusage.lock.json")
) {
  throw new Error(`platform package metadata does not match ${expectedOS}/${expectedCPU}`);
}

const binary = path.join(directory, "bin", binaryName);
try {
  await access(binary, constants.X_OK);
} catch (error) {
  if (error.code !== "ENOENT") {
    throw error;
  }
}
