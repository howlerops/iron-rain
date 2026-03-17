import { describe, expect, it } from "bun:test";
import { buildReviewPrompt, parseReviewResponse } from "../../review/reviewer.js";

describe("buildReviewPrompt", () => {
  it("returns no-changes message for empty diff", () => {
    expect(buildReviewPrompt("")).toBe("No changes to review.");
    expect(buildReviewPrompt("   ")).toBe("No changes to review.");
  });

  it("includes diff in review prompt", () => {
    const diff = "+const x = 1;\n-const x = 2;";
    const prompt = buildReviewPrompt(diff);
    expect(prompt).toContain("senior code reviewer");
    expect(prompt).toContain(diff);
  });

  it("truncates very large diffs", () => {
    const largeDiff = "x".repeat(60000);
    const prompt = buildReviewPrompt(largeDiff);
    expect(prompt).toContain("[...diff truncated...]");
    expect(prompt.length).toBeLessThan(largeDiff.length);
  });
});

describe("parseReviewResponse", () => {
  it("extracts issues from formatted response", () => {
    const response = `## Summary
Found 2 issues.

## Issues
- **[CRITICAL]** auth.ts:42 — SQL injection vulnerability
- **[WARNING]** utils.ts:10 — Unused variable`;

    const result = parseReviewResponse(response);
    expect(result.summary).toContain("Found 2 issues");
    expect(result.issues.length).toBe(2);
    expect(result.issues[0].severity).toBe("critical");
    expect(result.issues[0].file).toBe("auth.ts");
    expect(result.issues[0].line).toBe(42);
    expect(result.issues[1].severity).toBe("warning");
  });

  it("handles response with no issues", () => {
    const response = `## Summary
No issues found.`;

    const result = parseReviewResponse(response);
    expect(result.issues.length).toBe(0);
  });

  it("handles suggestion severity", () => {
    const response = `- **[SUGGESTION]** Consider adding error handling`;
    const result = parseReviewResponse(response);
    expect(result.issues.length).toBe(1);
    expect(result.issues[0].severity).toBe("suggestion");
  });
});
