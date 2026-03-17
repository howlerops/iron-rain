import { afterEach, describe, expect, mock, test } from "bun:test";

const execSyncMock = mock(() => Buffer.from(""));

mock.module("node:child_process", () => ({
  execSync: execSyncMock,
}));

const {
  autoCommit,
  isGitRepo,
  getChangedFiles,
  stashCreate,
  stashPop,
  getCurrentBranch,
  commitIfChanged,
} = await import("../../git/utils.js");

afterEach(() => {
  execSyncMock.mockReset();
});

describe("git utils", () => {
  test("isGitRepo returns true when git command succeeds", () => {
    execSyncMock.mockReturnValue(Buffer.from("true"));
    expect(isGitRepo()).toBe(true);
  });

  test("getChangedFiles parses diff output", () => {
    execSyncMock.mockReturnValue(Buffer.from("a.ts\nb.ts\n"));
    expect(getChangedFiles()).toEqual(["a.ts", "b.ts"]);
  });

  test("autoCommit returns hash on success", () => {
    execSyncMock
      .mockReturnValueOnce(Buffer.from(""))
      .mockReturnValueOnce(Buffer.from(""))
      .mockReturnValueOnce(Buffer.from("abc123\n"));

    expect(autoCommit("task")).toBe("abc123");
  });

  test("stashCreate returns undefined on failure", () => {
    execSyncMock.mockImplementation(() => {
      throw new Error("fail");
    });

    expect(stashCreate("msg")).toBeUndefined();
  });

  test("stashPop returns false on failure", () => {
    execSyncMock.mockImplementation(() => {
      throw new Error("fail");
    });

    expect(stashPop()).toBe(false);
  });

  test("getCurrentBranch returns empty string on failure", () => {
    execSyncMock.mockImplementation(() => {
      throw new Error("fail");
    });

    expect(getCurrentBranch()).toBe("");
  });

  test("commitIfChanged commits only when status has changes", () => {
    execSyncMock
      .mockReturnValueOnce(Buffer.from(" M a.ts\n"))
      .mockReturnValueOnce(Buffer.from(""))
      .mockReturnValueOnce(Buffer.from(""))
      .mockReturnValueOnce(Buffer.from("def456\n"));

    expect(commitIfChanged("update")).toBe("def456");
  });
});
