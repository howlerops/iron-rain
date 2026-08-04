import Foundation

/// The composer's `!` escape: a leading bang runs the rest of the line on the HOST, in the
/// session's workspace, instead of sending it to the agent.
///
/// This mirrors Claude Code, where `!pwd` drops into a one-shot bash line and the output is shown
/// to you but NOT handed to the model. Two things make that worth copying: checking `git status`
/// or `ls` mid-conversation is the single most common reason to leave the app, and doing it through
/// the agent costs a full turn (tokens, latency, and an approval prompt) for something you could
/// have typed.
///
/// The parsing lives here, apart from the view, because every interesting decision it makes is one
/// a user can be BURNED by — a message meant for the agent silently executed as a shell command is
/// unrecoverable in a way a mis-sent prompt is not. Pure input → decision, so it is unit tested
/// (see BangCommandTests) rather than only exercised by typing into a running app.
public enum BangCommand {

    /// What a composer entry turned out to be.
    public enum Parsed: Equatable {
        /// Send this to the agent. The text is the message AFTER unescaping (`\!x` → `!x`).
        case prompt(String)
        /// Run this on the host via the daemon's runner. Never empty, never leading-`!`.
        case shell(String)
        /// Nothing to do — a bare `!` with no command yet. The caller must LEAVE the entry alone
        /// (don't clear it, don't send it) so the user can keep typing the command.
        case nothing
    }

    /// The escape hatch, documented in one place so the UI can quote it back to the user.
    ///
    /// A prompt that legitimately begins with `!` ("!important — never force-push") is written
    /// `\!important …`. Backslash-escaping the history character is the POSIX shell convention for
    /// exactly this problem, so it's the one users already have. Deliberately NOT `!!`: in every
    /// shell `!!` means "the previous command", and re-purposing it as an escape would teach the
    /// wrong thing to the exact audience most likely to type it.
    public static let escapeHint = #"Start with \! to send a message that begins with "!" to the agent."#

    /// Classifies one composer entry.
    ///
    /// Rules, in order:
    ///  1. Leading whitespace/newlines are ignored when looking for the marker. `"  !pwd"` is a
    ///     shell command — a stray space is a typo, not a change of intent, and treating it as one
    ///     would make the feature feel unreliable.
    ///  2. `\!…` at the start is the escape: the backslash is dropped and everything else goes to
    ///     the agent verbatim. Only a LEADING `\!` is special, so `find . \! -name '*.go'` inside a
    ///     message is left exactly as typed.
    ///  3. `!` followed by nothing but whitespace is `.nothing`. Running `/bin/sh -c ""` would post
    ///     an empty transcript row that exited 0, and sending "!" to the agent is meaningless — so
    ///     we do neither and let the user finish typing.
    ///  4. Everything after the `!` is the command, INCLUDING later lines. `sh -c` takes a whole
    ///     script happily, and cutting at the first newline would silently drop the rest of what the
    ///     user typed — the one outcome this parser exists to prevent.
    ///  5. Anything else is a prompt, unchanged.
    public static func parse(_ raw: String) -> Parsed {
        // Only the LEADING whitespace is dropped for marker detection; the command body keeps its
        // interior newlines and indentation (a heredoc or an indented loop must survive intact).
        let lead = raw.drop(while: { $0 == " " || $0 == "\t" || $0 == "\n" || $0 == "\r" })

        if lead.hasPrefix(#"\!"#) {
            // Escaped: this is a message for the agent that happens to start with a bang.
            return .prompt(String(lead.dropFirst()))
        }
        guard lead.hasPrefix("!") else {
            // Not a bang at all. Return the ORIGINAL text, not the whitespace-stripped one — the
            // agent should receive precisely what was typed.
            return .prompt(raw)
        }
        let command = lead.dropFirst().trimmingCharacters(in: .whitespacesAndNewlines)
        return command.isEmpty ? .nothing : .shell(command)
    }

    /// Whether an entry would run as a shell command — for UI that must warn BEFORE the user hits
    /// send (e.g. an observer who isn't allowed to run one).
    public static func isShell(_ raw: String) -> Bool {
        if case .shell = parse(raw) { return true }
        return false
    }

    /// A one-line summary of a finished run, for the transcript row's header.
    public static func resultLabel(ok: Bool, exitCode: Int) -> String {
        ok ? "exit 0" : "exit \(exitCode)"
    }

    /// How much of a command's output the transcript row shows: the LAST `lines` lines, with a
    /// marker when anything was cut.
    ///
    /// The tail, not the head, because that's where a command puts its answer — the error, the
    /// summary line, the final `git status`. It's also what a terminal leaves on screen, so the
    /// truncation matches the mental model people already have. Bounding this matters: the row lives
    /// inside the transcript's own scroll view, and a 5,000-line build log would lay out 5,000 text
    /// lines on every re-render of a conversation you're still using.
    public static func previewOutput(_ output: String, lines: Int = 40) -> String {
        let trimmed = output.hasSuffix("\n") ? String(output.dropLast()) : output
        let all = trimmed.components(separatedBy: "\n")
        guard all.count > lines else { return trimmed }
        let shown = all.suffix(lines).joined(separator: "\n")
        return "…showing the last \(lines) of \(all.count) lines — Send to agent includes more\n" + shown
    }

    /// The message sent when the user explicitly hands a command's output to the agent.
    ///
    /// Built here (not in the view) so the "the agent only sees this because you asked" boundary is
    /// one testable function rather than string-building scattered through a card. The output is
    /// tail-truncated: a build log can be megabytes, and silently blowing the context window is a
    /// worse failure than an explicitly marked cut.
    public static func shareMessage(command: String, output: String, limit: Int = 12_000) -> String {
        var body = output.trimmingCharacters(in: .whitespacesAndNewlines)
        if body.count > limit {
            body = "…(earlier output trimmed)\n" + String(body.suffix(limit))
        }
        if body.isEmpty { body = "(no output)" }
        return "I ran `\(command)` myself. Its output:\n\n```\n\(body)\n```"
    }
}
