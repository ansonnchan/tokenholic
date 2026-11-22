#!/usr/bin/env node
// Assembles the static website for publishing. The site has no build step:
// this copies web/ as it is, leaves out the files that exist only for
// development, and writes the one value a deployment owns, the API origin
// the pages read from.
//
// Usage: node scripts/assemble-website.mjs <api-origin> <output-directory> [base-path]
//
// base-path is where the site is served under its host, such as /tokenholic
// for a GitHub Pages project site; omit it for a site at the root.

import { cp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

// Files under web/ that are not part of the published site.
const DEVELOPMENT_ONLY = new Set(["tests", "README.md", "package.json"]);

// apiOrigin accepts exactly one HTTPS origin, so a mistyped variable fails
// the publish instead of shipping pages that read from somewhere else.
export function apiOrigin(value) {
  let parsed;
  try {
    parsed = new URL(String(value).trim());
  } catch {
    throw new Error("API origin must be an HTTPS origin such as https://api.example.com");
  }
  if (parsed.protocol !== "https:" || parsed.username || parsed.password || parsed.search || parsed.hash ||
      (parsed.pathname !== "/" && parsed.pathname !== "")) {
    throw new Error("API origin must be an HTTPS origin without a path, query, or credentials");
  }
  return parsed.origin;
}

export function configModule(origin) {
  return `// Written when the site was published. See web/README.md.\nexport const apiOrigin = ${JSON.stringify(origin)};\n`;
}

// basePath accepts "" or one or more plain path segments with a leading
// slash and no trailing one.
export function basePath(value = "") {
  const trimmed = String(value).trim().replace(/\/+$/, "");
  if (trimmed !== "" && !/^(\/[A-Za-z0-9._-]+)+$/.test(trimmed) || /\/\.\.?(\/|$)/.test(trimmed)) {
    throw new Error("base path must look like /name, or be empty for a site at the root");
  }
  return trimmed;
}

export async function assembleWebsite(source, output, origin, base = "") {
  const validated = apiOrigin(origin);
  const root = basePath(base);
  await rm(output, { recursive: true, force: true });
  await mkdir(output, { recursive: true });
  await cp(source, output, {
    recursive: true,
    filter: (entry) => {
      const relative = path.relative(source, entry);
      return relative === "" || !DEVELOPMENT_ONLY.has(relative.split(path.sep)[0]);
    },
  });
  await writeFile(path.join(output, "assets", "js", "config.js"), configModule(validated));
  // The not-found page is served at any depth, so its one link home cannot
  // be relative and must know where the site's root is.
  const notFound = path.join(output, "404.html");
  const page = await readFile(notFound, "utf8");
  if (!page.includes('href="/"')) throw new Error("404.html has no root link to rewrite");
  await writeFile(notFound, page.replaceAll('href="/"', `href="${root}/"`));
  // GitHub Pages runs Jekyll unless told not to; these files need none.
  await writeFile(path.join(output, ".nojekyll"), "");
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [origin, output, base] = process.argv.slice(2);
  if (!origin || !output) {
    console.error("usage: assemble-website.mjs <api-origin> <output-directory> [base-path]");
    process.exit(2);
  }
  const source = path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "web");
  try {
    await assembleWebsite(source, path.resolve(output), origin, base);
    console.log(`Assembled the website in ${output}`);
  } catch (error) {
    console.error(`Could not assemble the website: ${error.message}`);
    process.exit(1);
  }
}
