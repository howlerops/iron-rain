import solidPlugin from "@opentui/solid/bun-plugin";

await Bun.build({
  entrypoints: ["src/index.ts"],
  outdir: "dist",
  target: "node",
  format: "esm",
  plugins: [solidPlugin],
  external: [
    "@opentui/core",
    "@opentui/solid",
    "solid-js",
    "@howlerops/iron-rain",
    "bun:sqlite",
  ],
});

// Generate type declarations
const tsc = Bun.spawn(["npx", "tsc", "--emitDeclarationOnly"], {
  cwd: import.meta.dir,
  stdout: "inherit",
  stderr: "inherit",
});
const exitCode = await tsc.exited;
if (exitCode !== 0) {
  process.exit(exitCode);
}
