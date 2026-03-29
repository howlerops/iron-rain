import { describe, expect, it } from "bun:test";
import {
  parseAllMarkdownHooks,
  parseMarkdownHooks,
} from "../../rules/hook-parser.js";

describe("parseMarkdownHooks", () => {
  describe("fenced code blocks", () => {
    it("parses ```hooks fenced code block", () => {
      const md = `
# My Project

\`\`\`hooks
beforeDispatch: bun run lint
afterDispatch: bun run typecheck
\`\`\`
`;
      const result = parseMarkdownHooks(md);
      expect(result.hooks).toEqual([
        { event: "beforeDispatch", command: "bun run lint" },
        { event: "afterDispatch", command: "bun run typecheck" },
      ]);
    });

    it("parses multiple fenced code blocks", () => {
      const md = `
\`\`\`hooks
beforeDispatch: bun run lint
\`\`\`

Some text.

\`\`\`hooks
onCommit: bun run test
\`\`\`
`;
      const result = parseMarkdownHooks(md);
      expect(result.hooks.length).toBe(2);
      expect(result.hooks[0].event).toBe("beforeDispatch");
      expect(result.hooks[1].event).toBe("onCommit");
    });

    it("ignores invalid hook event names in fenced blocks", () => {
      const md = `
\`\`\`hooks
invalidEvent: some command
beforeDispatch: bun run lint
\`\`\`
`;
      const result = parseMarkdownHooks(md);
      expect(result.hooks.length).toBe(1);
      expect(result.hooks[0].event).toBe("beforeDispatch");
    });

    it("handles comments and blank lines in fenced blocks", () => {
      const md = `
\`\`\`hooks
// This is a comment
beforeDispatch: bun run lint

# Another comment
afterDispatch: bun test
\`\`\`
`;
      const result = parseMarkdownHooks(md);
      expect(result.hooks.length).toBe(2);
    });
  });

  describe("section-based hooks", () => {
    it("parses ## Hooks section", () => {
      const md = `
# Project Config

## Hooks

beforeDispatch: bun run lint
afterDispatch: bun run typecheck

## Other Section

Some other content.
`;
      const result = parseMarkdownHooks(md);
      expect(result.hooks).toEqual([
        { event: "beforeDispatch", command: "bun run lint" },
        { event: "afterDispatch", command: "bun run typecheck" },
      ]);
    });

    it("parses # Hook (singular) heading", () => {
      const md = `
# Hook

onCommit: bun run test
`;
      const result = parseMarkdownHooks(md);
      expect(result.hooks.length).toBe(1);
      expect(result.hooks[0].event).toBe("onCommit");
    });

    it("parses ### Hooks (h3 heading)", () => {
      const md = `
### Hooks
onSessionStart: echo "started"
`;
      const result = parseMarkdownHooks(md);
      expect(result.hooks.length).toBe(1);
    });

    it("handles bullet list items in hooks section", () => {
      const md = `
## Hooks
- beforeDispatch: bun run lint
- afterDispatch: bun run typecheck
`;
      const result = parseMarkdownHooks(md);
      expect(result.hooks.length).toBe(2);
    });
  });

  describe("quality gates section", () => {
    it("parses ## Quality Gates section", () => {
      const md = `
## Quality Gates

lint: bunx biome check .
typecheck: bun run tsc --noEmit
test: bun test
`;
      const result = parseMarkdownHooks(md);
      expect(result.qualityGates.length).toBe(3);
      expect(result.qualityGates[0]).toEqual({
        type: "lint",
        command: "bunx biome check .",
        trigger: "beforeDispatch",
        label: "lint: bunx biome check .",
      });
      expect(result.qualityGates[1]).toEqual({
        type: "typecheck",
        command: "bun run tsc --noEmit",
        trigger: "beforeDispatch",
        label: "typecheck: bun run tsc --noEmit",
      });
      expect(result.qualityGates[2]).toEqual({
        type: "test",
        command: "bun test",
        trigger: "afterDispatch",
        label: "test: bun test",
      });
    });

    it("parses bullet list quality gates", () => {
      const md = `
## Quality Gates
- lint: eslint .
- format: prettier --check .
`;
      const result = parseMarkdownHooks(md);
      expect(result.qualityGates.length).toBe(2);
      expect(result.qualityGates[0].type).toBe("lint");
      expect(result.qualityGates[1].type).toBe("format");
    });
  });

  describe("inline quality gate extraction", () => {
    it("extracts lint: command directives", () => {
      const md = `
# Project

- lint: bunx biome check .
- typecheck: tsc --noEmit
`;
      const result = parseMarkdownHooks(md);
      expect(result.qualityGates.length).toBe(2);
    });

    it("extracts 'always run' patterns", () => {
      const md =
        "Always run `bun run lint` before dispatch and run `bun test` after changes.";
      const result = parseMarkdownHooks(md);
      expect(result.qualityGates.length).toBe(2);
      expect(result.qualityGates[0].command).toBe("bun run lint");
      expect(result.qualityGates[0].trigger).toBe("beforeDispatch");
      expect(result.qualityGates[1].command).toBe("bun test");
      expect(result.qualityGates[1].trigger).toBe("afterDispatch");
    });

    it("extracts 'run before commit' patterns", () => {
      const md = "Run `bun run lint` before commit.";
      const result = parseMarkdownHooks(md);
      expect(result.qualityGates.length).toBe(1);
      expect(result.qualityGates[0].trigger).toBe("onCommit");
    });
  });

  describe("deduplication", () => {
    it("deduplicates identical hooks", () => {
      const md = `
\`\`\`hooks
beforeDispatch: bun run lint
\`\`\`

## Hooks
beforeDispatch: bun run lint
`;
      const result = parseMarkdownHooks(md);
      expect(result.hooks.length).toBe(1);
    });

    it("deduplicates identical quality gates", () => {
      const md = `
## Quality Gates
lint: bun run lint

- lint: bun run lint
`;
      const result = parseMarkdownHooks(md);
      expect(result.qualityGates.length).toBe(1);
    });
  });

  describe("edge cases", () => {
    it("returns empty result for content with no hooks", () => {
      const md = "# Just a README\n\nSome normal content.";
      const result = parseMarkdownHooks(md);
      expect(result.hooks).toEqual([]);
      expect(result.qualityGates).toEqual([]);
    });

    it("handles empty string", () => {
      const result = parseMarkdownHooks("");
      expect(result.hooks).toEqual([]);
      expect(result.qualityGates).toEqual([]);
    });

    it("handles malformed fenced block (no closing)", () => {
      const md = `
\`\`\`hooks
beforeDispatch: bun run lint
`;
      const result = parseMarkdownHooks(md);
      expect(result.hooks).toEqual([]);
    });

    it("strips backticks from commands", () => {
      const md = `
## Hooks
beforeDispatch: \`bun run lint\`
`;
      const result = parseMarkdownHooks(md);
      expect(result.hooks.length).toBe(1);
      expect(result.hooks[0].command).toBe("bun run lint");
    });
  });
});

describe("parseAllMarkdownHooks", () => {
  it("combines hooks from multiple markdown files", () => {
    const files = [
      `
\`\`\`hooks
beforeDispatch: bun run lint
\`\`\`
`,
      `
## Hooks
afterDispatch: bun run typecheck
`,
    ];
    const result = parseAllMarkdownHooks(files);
    expect(result.hooks.length).toBe(2);
    expect(result.hooks[0].event).toBe("beforeDispatch");
    expect(result.hooks[1].event).toBe("afterDispatch");
  });

  it("deduplicates across files", () => {
    const files = [
      "## Hooks\nbeforeDispatch: bun run lint",
      "## Hooks\nbeforeDispatch: bun run lint",
    ];
    const result = parseAllMarkdownHooks(files);
    expect(result.hooks.length).toBe(1);
  });

  it("combines quality gates from multiple files", () => {
    const files = [
      "## Quality Gates\nlint: eslint .",
      "## Quality Gates\ntest: bun test",
    ];
    const result = parseAllMarkdownHooks(files);
    expect(result.qualityGates.length).toBe(2);
  });

  it("handles empty input", () => {
    const result = parseAllMarkdownHooks([]);
    expect(result.hooks).toEqual([]);
    expect(result.qualityGates).toEqual([]);
  });
});
