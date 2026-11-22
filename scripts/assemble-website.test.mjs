import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";

import { apiOrigin, assembleWebsite, basePath, configModule } from "./assemble-website.mjs";

test("accepts only one HTTPS origin for the API", () => {
  assert.equal(apiOrigin("https://api.tokenholic.example"), "https://api.tokenholic.example");
  assert.equal(apiOrigin(" https://api.tokenholic.example/ "), "https://api.tokenholic.example");
  for (const bad of [
    "", "api.tokenholic.example", "http://api.tokenholic.example", "https://api.tokenholic.example/v1",
    "https://api.tokenholic.example?x=1", "https://user:secret@api.tokenholic.example", "https://api.tokenholic.example/#x",
  ]) {
    assert.throws(() => apiOrigin(bad), /API origin/, bad);
  }
});

test("accepts a plain base path or none", () => {
  assert.equal(basePath(), "");
  assert.equal(basePath("/"), "");
  assert.equal(basePath("/tokenholic"), "/tokenholic");
  assert.equal(basePath("/tokenholic/"), "/tokenholic");
  assert.equal(basePath("/a/b-c_d.e"), "/a/b-c_d.e");
  for (const bad of ["tokenholic", "/a b", "/a/../b", "/..", '/"><script>', "//evil.example", "https://evil.example"]) {
    assert.throws(() => basePath(bad), /base path/, bad);
  }
});

test("the written config cannot break out of its string", () => {
  assert.equal(configModule("https://api.tokenholic.example").split("\n")[1], 'export const apiOrigin = "https://api.tokenholic.example";');
});

test("publishes the site without its development files", async (t) => {
  const root = await mkdtemp(path.join(tmpdir(), "tokenholic-web-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const source = path.join(root, "web");
  const output = path.join(root, "out");
  await mkdir(path.join(source, "assets", "js"), { recursive: true });
  await mkdir(path.join(source, "tests"), { recursive: true });
  await mkdir(path.join(source, "u"), { recursive: true });
  await writeFile(path.join(source, "index.html"), "<!doctype html>");
  await writeFile(path.join(source, "404.html"), '<a href="/">Go to the start</a>');
  await writeFile(path.join(source, "u", "index.html"), "<!doctype html>");
  await writeFile(path.join(source, "assets", "js", "config.js"), 'export const apiOrigin = "";\n');
  await writeFile(path.join(source, "tests", "x.test.mjs"), "");
  await writeFile(path.join(source, "README.md"), "");
  await writeFile(path.join(source, "package.json"), "{}");
  await mkdir(output);
  await writeFile(path.join(output, "stale.html"), "left over from an earlier run");

  await assembleWebsite(source, output, "https://api.tokenholic.example");

  assert.deepEqual((await readdir(output)).sort(), [".nojekyll", "404.html", "assets", "index.html", "u"]);
  assert.equal(await readFile(path.join(output, "404.html"), "utf8"), '<a href="/">Go to the start</a>');
  await assembleWebsite(source, output, "https://api.tokenholic.example", "/tokenholic/");
  assert.equal(await readFile(path.join(output, "404.html"), "utf8"), '<a href="/tokenholic/">Go to the start</a>');
  assert.match(await readFile(path.join(output, "assets", "js", "config.js"), "utf8"), /apiOrigin = "https:\/\/api\.tokenholic\.example";/);
  // The checkout keeps its empty default.
  assert.equal(await readFile(path.join(source, "assets", "js", "config.js"), "utf8"), 'export const apiOrigin = "";\n');
  await assert.rejects(assembleWebsite(source, output, "http://api.tokenholic.example"), /API origin/);
});

test("the real site assembles with every page present", async (t) => {
  const output = await mkdtemp(path.join(tmpdir(), "tokenholic-web-real-"));
  t.after(() => rm(output, { recursive: true, force: true }));
  const source = path.join(import.meta.dirname, "..", "web");
  await assembleWebsite(source, output, "https://api.tokenholic.example");
  const entries = await readdir(output);
  for (const page of ["index.html", "404.html", "u", "b", "invite", "assets"]) {
    assert.ok(entries.includes(page), `${page} is published`);
  }
  for (const development of ["tests", "README.md", "package.json"]) {
    assert.ok(!entries.includes(development), `${development} is not published`);
  }
});
