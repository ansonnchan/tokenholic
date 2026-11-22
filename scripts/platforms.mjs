// The platforms a release ships, in publish order. Each release archive,
// tokenholic_<version>_<archiveOS>_<cpu>.tar.gz from cmd/release, fills the
// npm package @tokenholic/<os>-<cpu>. `os` and `cpu` are Node's names
// (process.platform and process.arch), which npm also uses to pick the one
// optional package an install needs. `archiveOS` is Go's name for the same
// system and differs only on Windows.
export const platforms = Object.freeze([
  Object.freeze({ os: "darwin", cpu: "arm64", archiveOS: "darwin", label: "macOS arm64" }),
  Object.freeze({ os: "darwin", cpu: "x64", archiveOS: "darwin", label: "macOS x64" }),
  Object.freeze({ os: "linux", cpu: "arm64", archiveOS: "linux", label: "Linux arm64" }),
  Object.freeze({ os: "linux", cpu: "x64", archiveOS: "linux", label: "Linux x64" }),
  Object.freeze({ os: "win32", cpu: "arm64", archiveOS: "windows", label: "Windows arm64" }),
  Object.freeze({ os: "win32", cpu: "x64", archiveOS: "windows", label: "Windows x64" }),
]);

export function packageDirectory(platform) {
  return `${platform.os}-${platform.cpu}`;
}

export function packageName(platform) {
  return `@tokenholic/${packageDirectory(platform)}`;
}

// The pinned ccusage package for the same platform. ccusage.lock.json holds
// its version, npm integrity, and the digest of the executable inside it.
export function ccusagePackageName(platform) {
  return `@ccusage/ccusage-${packageDirectory(platform)}`;
}

export function archiveName(platform, version) {
  return `tokenholic_${version}_${platform.archiveOS}_${platform.cpu}.tar.gz`;
}

// The executable's file name inside the archive and the package.
export function binaryName(platform) {
  return platform.os === "win32" ? "tokenholic.exe" : "tokenholic";
}
