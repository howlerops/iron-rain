import { describe, expect, it } from "bun:test";
import { PluginManager } from "../../plugins/manager.js";

describe("PluginManager markdown hooks", () => {
  it("registers hooks from markdown rules via loadMarkdownHooks", () => {
    const mgr = new PluginManager();
    mgr.loadMarkdownHooks([
      `
\`\`\`hooks
beforeDispatch: echo "linting"
\`\`\`
`,
    ]);

    // The hook should be registered on the emitter
    expect(mgr.emitter.listenerCount("beforeDispatch")).toBe(1);
  });

  it("registers quality gates from markdown rules", () => {
    const mgr = new PluginManager();
    mgr.loadMarkdownHooks([
      `
## Quality Gates
lint: bun run lint
typecheck: tsc --noEmit
`,
    ]);

    expect(mgr.qualityGates.length).toBe(2);
    expect(mgr.qualityGates[0].type).toBe("lint");
    expect(mgr.qualityGates[1].type).toBe("typecheck");
  });

  it("registers quality gates as hook handlers on their trigger events", () => {
    const mgr = new PluginManager();
    mgr.loadMarkdownHooks([
      `
## Quality Gates
lint: bun run lint
test: bun test
`,
    ]);

    // lint triggers on beforeDispatch, test triggers on afterDispatch
    expect(mgr.emitter.listenerCount("beforeDispatch")).toBe(1);
    expect(mgr.emitter.listenerCount("afterDispatch")).toBe(1);
  });

  it("chains multiple hooks for the same event", () => {
    const mgr = new PluginManager();
    mgr.loadMarkdownHooks([
      `
\`\`\`hooks
beforeDispatch: echo "first"
beforeDispatch: echo "second"
\`\`\`
`,
    ]);

    // Two hooks with same event get chained with &&
    // This results in one shell hook with "echo first && echo second"
    expect(mgr.emitter.listenerCount("beforeDispatch")).toBe(1);
  });

  it("handles empty rules array", () => {
    const mgr = new PluginManager();
    mgr.loadMarkdownHooks([]);
    expect(mgr.qualityGates.length).toBe(0);
  });

  it("handles rules with no hooks", () => {
    const mgr = new PluginManager();
    mgr.loadMarkdownHooks(["# Just a README\n\nNo hooks here."]);
    expect(mgr.qualityGates.length).toBe(0);
    expect(mgr.emitter.listenerCount("beforeDispatch")).toBe(0);
  });

  it("combines config hooks with markdown hooks", async () => {
    const mgr = new PluginManager({
      hooks: { onCommit: "echo config-hook" },
    });

    // Config hooks are loaded in loadAll, but markdown hooks via loadMarkdownHooks
    mgr.loadMarkdownHooks([
      `
\`\`\`hooks
beforeDispatch: echo "md-hook"
\`\`\`
`,
    ]);

    expect(mgr.emitter.listenerCount("beforeDispatch")).toBe(1);
    // Config hooks haven't been registered yet (that happens in loadAll)
    expect(mgr.qualityGates.length).toBe(0);
  });
});
