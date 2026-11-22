import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

import { parseDecimal, percent, tokens, usd } from "../assets/js/format.js";

// The same vectors the Go formatter is tested against.
const vectors = JSON.parse(
  readFileSync(new URL("../../internal/numfmt/testdata/vectors.json", import.meta.url), "utf8"),
);

test("tokens matches the Go formatter", () => {
  assert.ok(vectors.tokens.length > 0);
  for (const { in: input, want } of vectors.tokens) {
    assert.equal(tokens(parseDecimal(input)), want, `tokens(${input})`);
  }
});

test("usd matches the Go formatter", () => {
  assert.ok(vectors.usd_micros.length > 0);
  for (const { in: input, want } of vectors.usd_micros) {
    assert.equal(usd(parseDecimal(input)), want, `usd(${input})`);
  }
});

test("percent matches the Go formatter", () => {
  assert.ok(vectors.percent_basis_points.length > 0);
  for (const { in: input, want } of vectors.percent_basis_points) {
    assert.equal(percent(input), want, `percent(${input})`);
  }
});

test("parseDecimal rejects anything but digits", () => {
  for (const invalid of [...vectors.invalid_decimals, null, undefined, 12, 1.5, {}]) {
    assert.equal(parseDecimal(invalid), null, `parseDecimal(${JSON.stringify(invalid)})`);
  }
  assert.equal(parseDecimal("0"), 0n);
});
