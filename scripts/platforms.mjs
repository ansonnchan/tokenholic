// The platforms a release ships, in publish order. Release archives are named
// tokenholic_<version>_<os>_<cpu>.tar.gz by cmd/release, and each one fills
// the npm package @tokenholic/<os>-<cpu>. `os` and `cpu` are Node's names
// (process.platform and process.arch), which npm also uses to pick the one
// optional package an install needs.
export const platforms = Object.freeze([
  Object.freeze({ os: "darwin", cpu: "arm64", label: "macOS arm64" }),
  Object.freeze({ os: "darwin", cpu: "x64", label: "macOS x64" }),
  Object.freeze({ os: "linux", cpu: "arm64", label: "Linux arm64" }),
  Object.freeze({ os: "linux", cpu: "x64", label: "Linux x64" }),
]);

export function packageDirectory(platform) {
  return `${platform.os}-${platform.cpu}`;
}

export function packageName(platform) {
  return `@tokenholic/${packageDirectory(platform)}`;
}

export function archiveName(platform, version) {
  return `tokenholic_${version}_${platform.os}_${platform.cpu}.tar.gz`;
}
