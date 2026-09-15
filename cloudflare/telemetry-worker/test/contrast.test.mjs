import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

// Text has to be readable in BOTH themes, measured rather than eyeballed.
//
// The gold palette is tuned to sit on near-black. Reusing those tokens as text on a light card
// measured 1.27:1 — the filter chips, the Apply button and the active range pill were all very
// nearly invisible in light mode, and it shipped because it was only ever looked at in dark.
//
// So the accent exists as two tokens: --gold for fills, --accent-fg/--accent-strong for text, each
// with a per-theme value. This asserts the pairs the page actually renders, at the WCAG AA
// threshold for body text.

const src = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), "..", "src", "index.js"),
  "utf8"
);

/** WCAG 2.1 relative luminance. */
function luminance(hex) {
  let h = hex.replace("#", "");
  if (h.length === 3) h = [...h].map((c) => c + c).join("");
  const [r, g, b] = [0, 2, 4]
    .map((i) => parseInt(h.slice(i, i + 2), 16) / 255)
    .map((c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(a, b) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

/**
 * Reads a token's value for a theme straight out of the stylesheet, so the test tracks the CSS
 * rather than a copy of it. Light/dark overrides live in prefers-color-scheme blocks; the last
 * definition for a theme wins, exactly as the cascade would resolve it.
 */
function token(name, theme) {
  // Resolve the cascade for ONE theme: drop the other theme's media block entirely, then read the
  // :root blocks that remain, in source order.
  //
  // The first version of this matched /:root\{/ globally, which also matches the :root nested inside
  // a prefers-color-scheme block — so resolving "dark" quietly mixed in the light overrides and
  // reported a contrast figure for a pair that never renders together. It failed a passing colour,
  // which is the good direction for a test to be wrong in, but wrong either way.
  const other = theme === "dark" ? "light" : "dark";
  const css = src.replace(
    new RegExp(`@media \\(prefers-color-scheme:${other}\\)\\{:root\\{[^}]*\\}\\}`, "g"),
    ""
  );
  const base = {};
  for (const b of css.matchAll(/:root\{([^}]*)\}/g)) {
    for (const m of b[1].matchAll(/(--[a-z-]+)\s*:\s*([^;]+)/g)) base[m[1]] = m[2].trim();
  }
  const v = base[name];
  assert.ok(v, `token ${name} is not defined for the ${theme} theme`);
  assert.match(v, /^#[0-9a-f]{3,8}$/i, `token ${name} (${theme}) is ${v}, not a hex colour`);
  return v;
}

const AA = 4.5;

// Each entry is text-on-surface, named by where it appears so a failure says what to look at.
const PAIRS = [
  ["--accent-fg", "--card", "filter chips, the warning panel, inline code"],
  ["--accent-strong", "--bg", "the IRON RAIN wordmark and the headline figure"],
  ["--muted-med", "--bg", "the standfirst and every bar's value"],
  ["--muted", "--bg", "section labels and the footer"],
  ["--fg", "--bg", "body text"],
  ["--fg", "--card", "text inside panels and tables"],
  ["--bad", "--card", "failure counts in tables"],
];

for (const theme of ["light", "dark"]) {
  for (const [fg, bg, where] of PAIRS) {
    test(`${theme}: ${fg} on ${bg} is readable — ${where}`, () => {
      const c = contrast(token(fg, theme), token(bg, theme));
      assert.ok(
        c >= AA,
        `${fg} on ${bg} in ${theme} is ${c.toFixed(2)}:1, below the ${AA}:1 needed for body text. ` +
          `This affects: ${where}. Gold tuned for a near-black ground does not survive being moved ` +
          `onto a light card — each theme needs its own value.`
      );
    });
  }
}

// The button is the one place text sits on a filled gold pill rather than on a surface.
test("the Apply button's label is readable on its gold fill", () => {
  const ink = "#130e00"; // the .go rule's literal colour
  for (const theme of ["light", "dark"]) {
    const c = contrast(ink, token("--gold", theme));
    assert.ok(c >= AA, `Apply label is ${c.toFixed(2)}:1 on --gold in ${theme}`);
  }
});

// The gradient is decorative now; if it ever carries text again, this test should be extended.
test("the accent tokens are distinct from the fill token", () => {
  for (const theme of ["light", "dark"]) {
    assert.notEqual(
      token("--accent-fg", theme),
      token("--gold", theme),
      `--accent-fg equals --gold in ${theme}. They serve different jobs: one is a fill on a dark ` +
        `ground, the other is text on whatever surface the theme provides. Collapsing them is what ` +
        `produced 1.27:1 chips.`
    );
  }
});

// ---- two failures that produced no error at all -------------------------------------------------

test("no CSS class is defined twice", () => {
  // The toolbar was first called .bar, which the horizontal bar-chart rows already used. Defined
  // later in the sheet, the chart rule won, and the toolbar rendered with a narrow/wide/narrow grid
  // at every width. Nothing threw; the class simply meant two things, and three rounds of layout
  // fixes went nowhere because none of them were the problem.
  const css = src.slice(src.indexOf("const CSS = `"), src.indexOf("function shellHead"));
  const counts = new Map();
  for (const m of css.matchAll(/^\.([a-z][a-z0-9-]*)\{/gm)) {
    counts.set(m[1], (counts.get(m[1]) || 0) + 1);
  }
  const dupes = [...counts].filter(([, n]) => n > 1).map(([c]) => "." + c);
  assert.deepEqual(
    dupes,
    [],
    `defined more than once: ${dupes.join(", ")}. Whichever comes last silently wins, so one of the ` +
      `two things the class names renders with the other's rules.`
  );
});

test("the stylesheet contains no backtick", () => {
  // CSS lives in a template literal. A backtick anywhere inside it — including in a comment, which
  // is where one was written while explaining the bug above — terminates the literal, and the rest
  // of the stylesheet is parsed as JavaScript. The module then fails to load at all.
  const css = src.slice(src.indexOf("const CSS = `") + "const CSS = `".length);
  const body = css.slice(0, css.indexOf("\n`;"));
  assert.ok(
    !body.includes("`"),
    "a backtick inside the CSS template literal ends it early; the stylesheet after that point " +
      "becomes JavaScript and the whole Worker stops loading"
  );
});
