import XCTest
@testable import OculusUI

/// Tests for the composer's `!` escape (BangCommand).
///
/// Every case here is one where getting it wrong LOSES something the user typed: a prompt executed
/// as a shell command, a shell command sent to the agent as prose, or a multi-line command silently
/// truncated at its first newline. The parser is pure input → decision precisely so those outcomes
/// are asserted here rather than discovered by typing into a running app.
final class BangCommandTests: XCTestCase {

    // MARK: - the happy path

    func testLeadingBangRunsTheRestAsACommand() {
        XCTAssertEqual(BangCommand.parse("!pwd"), .shell("pwd"))
        XCTAssertEqual(BangCommand.parse("!git status --short"), .shell("git status --short"))
    }

    /// A space after the marker is how plenty of people type it. Treating "! ls" as a PROMPT would
    /// send the agent a message that reads like a typo and cost a whole turn.
    func testSpaceAfterTheBangIsStillACommand() {
        XCTAssertEqual(BangCommand.parse("! ls -la"), .shell("ls -la"))
    }

    /// A stray leading space is a typo, not a change of intent — the feature has to work the same
    /// way every time or people stop trusting it.
    func testLeadingWhitespaceDoesNotDisableTheEscape() {
        XCTAssertEqual(BangCommand.parse("  !pwd"), .shell("pwd"))
        XCTAssertEqual(BangCommand.parse("\t!pwd"), .shell("pwd"))
        XCTAssertEqual(BangCommand.parse("\n!pwd"), .shell("pwd"))
    }

    func testTrailingWhitespaceIsTrimmedFromTheCommand() {
        XCTAssertEqual(BangCommand.parse("!pwd   \n"), .shell("pwd"))
    }

    // MARK: - a bare marker

    /// "!" on its own must do NOTHING: running `sh -c ""` posts an empty row that exited 0, and
    /// sending "!" to the agent is meaningless. The caller keeps the text so the user can finish.
    func testBareBangIsNeitherRunNorSent() {
        XCTAssertEqual(BangCommand.parse("!"), .nothing)
        XCTAssertEqual(BangCommand.parse("!   "), .nothing)
        XCTAssertEqual(BangCommand.parse("  !\n"), .nothing)
    }

    // MARK: - the escape hatch

    /// The reason the escape exists: a message that legitimately opens with a bang. Without `\!`,
    /// "!important — never force-push" is handed to /bin/sh.
    func testBackslashEscapeSendsABangMessageToTheAgent() {
        XCTAssertEqual(BangCommand.parse(#"\!important — never force-push"#),
                       .prompt("!important — never force-push"))
    }

    /// Only the escape's own backslash is consumed; the message keeps everything else verbatim.
    func testEscapeConsumesExactlyOneBackslash() {
        XCTAssertEqual(BangCommand.parse(#"\!!"#), .prompt("!!"))
    }

    /// A backslash-bang INSIDE a message is ordinary text (it's valid shell, and it appears in real
    /// find/test invocations people paste into prompts). Rewriting it would corrupt the message.
    func testInteriorBackslashBangIsUntouched() {
        let text = #"run: find . \! -name '*.go'"#
        XCTAssertEqual(BangCommand.parse(text), .prompt(text))
    }

    /// `!!` is NOT an escape. In every shell it means "the previous command", so treating it as one
    /// would teach exactly the wrong reflex — it runs as a command like any other bang line.
    func testDoubleBangIsACommandNotAnEscape() {
        XCTAssertEqual(BangCommand.parse("!!"), .shell("!"))
    }

    // MARK: - ordinary prompts are untouched

    func testOrdinaryPromptPassesThroughUnchanged() {
        let text = "Fix the failing test in runner_test.go"
        XCTAssertEqual(BangCommand.parse(text), .prompt(text))
    }

    /// A prompt is returned EXACTLY as typed, including its leading whitespace — the parser
    /// normalizes only for marker detection, never for delivery.
    func testPromptKeepsItsOriginalWhitespace() {
        XCTAssertEqual(BangCommand.parse("  hello"), .prompt("  hello"))
        XCTAssertEqual(BangCommand.parse(""), .prompt(""))
    }

    /// A bang that isn't first is just punctuation.
    func testNonLeadingBangIsNotAnEscape() {
        XCTAssertEqual(BangCommand.parse("wow!ls"), .prompt("wow!ls"))
        XCTAssertEqual(BangCommand.parse("Ship it!"), .prompt("Ship it!"))
    }

    /// Slash and dollar commands belong to the agent's own palette and must not be intercepted.
    func testSlashCommandsAreStillPrompts() {
        XCTAssertEqual(BangCommand.parse("/compact"), .prompt("/compact"))
        XCTAssertEqual(BangCommand.parse("$review"), .prompt("$review"))
    }

    // MARK: - multi-line

    /// `sh -c` takes a whole script, so the command is everything after the marker. Cutting at the
    /// first newline would silently drop the rest of what the user typed while still reporting
    /// success — the one failure this parser exists to prevent.
    func testMultiLineCommandKeepsEveryLine() {
        let entry = "!for f in *.go\ndo echo $f\ndone"
        XCTAssertEqual(BangCommand.parse(entry), .shell("for f in *.go\ndo echo $f\ndone"))
    }

    /// Interior indentation is part of the script (heredocs and blocks depend on it) and survives;
    /// only the outer edges are trimmed.
    func testMultiLineCommandKeepsInteriorIndentation() {
        XCTAssertEqual(BangCommand.parse("!if true; then\n    echo hi\nfi"),
                       .shell("if true; then\n    echo hi\nfi"))
    }

    /// A multi-line message whose first line is escaped stays one message to the agent.
    func testEscapedMultiLinePromptStaysAPrompt() {
        XCTAssertEqual(BangCommand.parse("\\!note:\nsecond line"), .prompt("!note:\nsecond line"))
    }

    // MARK: - the isShell convenience

    func testIsShellAgreesWithParse() {
        XCTAssertTrue(BangCommand.isShell("!ls"))
        XCTAssertFalse(BangCommand.isShell("!"))
        XCTAssertFalse(BangCommand.isShell(#"\!ls"#))
        XCTAssertFalse(BangCommand.isShell("ls"))
    }

    // MARK: - handing output to the agent (explicit only)

    /// The share text names the human as the actor. The agent did not run this and must not be led
    /// to believe it did — a model that thinks it ran the command will reason about state it never
    /// observed.
    func testShareMessageAttributesTheRunToTheUser() {
        let msg = BangCommand.shareMessage(command: "git status", output: "clean")
        XCTAssertTrue(msg.contains("I ran `git status` myself"))
        XCTAssertTrue(msg.contains("clean"))
    }

    /// A build log can be megabytes. Truncation is explicit and keeps the TAIL — the part with the
    /// error — because silently blowing the context window is a worse failure than a marked cut.
    func testShareMessageTruncatesLongOutputFromTheFront() {
        let output = String(repeating: "a", count: 500) + "THE-ERROR"
        let msg = BangCommand.shareMessage(command: "make", output: output, limit: 100)
        XCTAssertTrue(msg.contains("earlier output trimmed"))
        XCTAssertTrue(msg.contains("THE-ERROR"))
    }

    /// A command with no output still produces a coherent message rather than an empty code fence.
    func testShareMessageHandlesEmptyOutput() {
        XCTAssertTrue(BangCommand.shareMessage(command: "true", output: "   \n").contains("(no output)"))
    }

    // MARK: - what the row shows

    /// Short output is shown whole, with no marker and no trailing blank line.
    func testPreviewShowsShortOutputVerbatim() {
        XCTAssertEqual(BangCommand.previewOutput("one\ntwo\n"), "one\ntwo")
        XCTAssertEqual(BangCommand.previewOutput(""), "")
    }

    /// Long output keeps the TAIL — the error and the summary line live at the end, and that's also
    /// what a terminal leaves on screen.
    func testPreviewKeepsTheTailAndSaysSo() {
        let output = (1...100).map(String.init).joined(separator: "\n")
        let preview = BangCommand.previewOutput(output, lines: 10)
        XCTAssertTrue(preview.hasPrefix("…showing the last 10 of 100 lines"))
        XCTAssertTrue(preview.hasSuffix("\n100"))
        XCTAssertFalse(preview.contains("\n50\n"))
    }

    // MARK: - result label

    func testResultLabel() {
        XCTAssertEqual(BangCommand.resultLabel(ok: true, exitCode: 0), "exit 0")
        XCTAssertEqual(BangCommand.resultLabel(ok: false, exitCode: 127), "exit 127")
    }
}
