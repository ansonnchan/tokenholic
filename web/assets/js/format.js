// Number formatting for the website. This is a port of internal/numfmt and
// follows the same rules; both run the vectors in
// internal/numfmt/testdata/vectors.json, so they cannot drift apart.
//
// Like the Go package, it formats known values only. What an unknown value
// looks like is decided in values.js, so a missing number is never rendered
// as zero here.

const UNITS = ["", "K", "M", "B", "T"];
const MICROS_PER_CENT = 10_000n;

// parseDecimal reads the non-negative decimal strings the API uses for sums
// that may exceed 64 bits. Anything else is null.
export function parseDecimal(text) {
  return typeof text === "string" && /^[0-9]+$/.test(text) ? BigInt(text) : null;
}

function group(digits) {
  return digits.replace(/\B(?=(\d{3})+$)/g, ",");
}

// tokens renders a count compactly with at most three significant digits and
// no trailing zeros: 987, 1K, 111K, 1.35M, 2.1B, 4.27T. Rounding is half up
// and never yields 1000 of a unit; 999,950 is 1M.
export function tokens(count) {
  const digits = count.toString();
  if (digits.length <= 3) return digits;
  const unit = Math.min(Math.floor((digits.length - 1) / 3), UNITS.length - 1);
  const divisor = 1000n ** BigInt(unit);
  const decimals = Math.max(0, 3 - (digits.length - 3 * unit));
  const scale = 10n ** BigInt(decimals);
  const scaled = (count * scale + divisor / 2n) / divisor;
  if (unit < UNITS.length - 1 && scaled >= 1000n * scale) {
    return "1" + UNITS[unit + 1];
  }
  const text = scaled.toString();
  if (decimals === 0) return group(text) + UNITS[unit];
  const whole = text.slice(0, text.length - decimals);
  const fraction = text.slice(text.length - decimals).replace(/0+$/, "");
  return whole + (fraction ? "." + fraction : "") + UNITS[unit];
}

// usd renders micro-USD as dollars and cents with grouping: $1,180.00. A
// non-zero amount below one cent is "<$0.01" so it never reads as free. The
// caller adds the EST label.
export function usd(micros) {
  if (micros > 0n && micros < MICROS_PER_CENT) return "<$0.01";
  const cents = (micros + MICROS_PER_CENT / 2n) / MICROS_PER_CENT;
  return "$" + group((cents / 100n).toString()) + "." + (cents % 100n).toString().padStart(2, "0");
}

// percent renders basis points. Fractions are cut, not rounded, to one
// decimal, so a share just under a threshold never displays as having
// reached it: 9499 is "94.9%".
export function percent(basisPoints) {
  const points = Math.max(0, Math.trunc(basisPoints));
  const whole = Math.floor(points / 100);
  const tenths = Math.floor((points % 100) / 10);
  if (tenths > 0) return `${whole}.${tenths}%`;
  if (whole === 0 && points > 0) return "<0.1%";
  return `${whole}%`;
}
