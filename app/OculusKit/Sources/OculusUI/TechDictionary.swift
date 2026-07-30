import Foundation
#if os(macOS)
import AppKit
#else
import UIKit
#endif

/// A technical vocabulary taught to the system spell checker, so autocorrect stops rewriting the
/// words you actually use.
///
/// The prompt box keeps autocorrect on deliberately — most of what you type is prose, and losing
/// real typo-fixing would be a downgrade. The problem is narrower: the system dictionary has never
/// heard of `mcp`, `jira`, `opencode` or `worktree`, so it silently rewrites them into whatever is
/// closest, and you find out after you've hit send.
///
/// Teaching the words fixes the cause rather than the symptom. A learned word is one the corrector
/// stops touching, which is exactly the behaviour of "Learn Spelling" in any Mac text field.
///
/// One honest caveat, surfaced in the UI: learned words go into your SYSTEM dictionary, so they
/// apply in Mail and Notes too, not just here. That's why removal is offered and why the seeded set
/// is deliberately conservative — terms a developer actually types, not every acronym in existence.
public enum TechDictionary {
    /// Words seeded on first run. Kept to vocabulary that is (a) genuinely common in this context and
    /// (b) currently mangled — adding words the dictionary already knows would be noise in the
    /// user's system dictionary for no benefit.
    public static let seeded: [String] = [
        // The tools this app drives
        "mcp", "opencode", "claude", "anthropic", "codex", "gemini", "aider", "goose",
        "llm", "llms", "rag", "embeddings", "tokenizer", "prompt", "prompts",
        // Trackers and services
        "jira", "linear", "slack", "github", "gitlab", "sentry", "datadog", "cloudflare",
        // Version control
        "repo", "repos", "monorepo", "worktree", "worktrees", "rebase", "cherrypick",
        "upstream", "downstream", "changelog", "gitignore", "submodule",
        // Runtimes and packaging
        "npm", "npx", "pnpm", "yarn", "uvx", "pipx", "venv", "dockerfile", "kubectl",
        "kubernetes", "nginx", "systemd", "launchd", "daemon", "daemons",
        // Data and protocols
        "postgres", "postgresql", "sqlite", "redis", "graphql", "grpc", "webhook", "webhooks",
        "oauth", "jwt", "json", "jsonl", "yaml", "toml", "uuid", "regex", "cron", "websocket",
        "stdout", "stderr", "stdin", "localhost", "tls", "ssh",
        // Apple platform
        "swiftui", "uikit", "appkit", "xcode", "xcodebuild", "testflight", "apns", "keychain",
        "codesign", "notarize", "plist",
        // Everyday engineering words the dictionary still fights
        "async", "await", "enum", "struct", "func", "init", "params", "env", "envs",
        "config", "configs", "auth", "middleware", "backend", "frontend", "devops",
        "refactor", "refactored", "refactoring", "linting", "linter", "debounce",
        "idempotent", "serializable", "backoff", "throughput", "observability",
    ]

    private static let customKey = "oculus.dictionary.custom"
    private static let seededKey = "oculus.dictionary.seededVersion"
    /// Bumping this re-seeds on next launch, for when the list above gains terms.
    private static let seedVersion = 1

    /// Words the user added themselves, newest last.
    public static var custom: [String] {
        get { UserDefaults.standard.stringArray(forKey: customKey) ?? [] }
        set { UserDefaults.standard.set(newValue, forKey: customKey) }
    }

    /// Teaches the seeded list once per seed version. Cheap enough to call on every launch.
    public static func seedIfNeeded() {
        guard UserDefaults.standard.integer(forKey: seededKey) < seedVersion else { return }
        for word in seeded { learn(word) }
        UserDefaults.standard.set(seedVersion, forKey: seededKey)
    }

    /// Re-teaches the user's own words. The system dictionary is durable, but a restored Mac or a
    /// new device won't have them, so re-asserting on launch keeps the two in step.
    public static func applyCustom() {
        for word in custom { learn(word) }
    }

    /// Adds a word and teaches it. Returns false when it's empty or already present.
    @discardableResult
    public static func add(_ raw: String) -> Bool {
        let word = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !word.isEmpty, !word.contains(" ") else { return false }
        var words = custom
        guard !words.contains(where: { $0.caseInsensitiveCompare(word) == .orderedSame }) else { return false }
        words.append(word)
        custom = words
        learn(word)
        return true
    }

    /// Removes a word the user added and un-teaches it.
    public static func remove(_ word: String) {
        custom = custom.filter { $0.caseInsensitiveCompare(word) != .orderedSame }
        unlearn(word)
    }

    /// Un-teaches every seeded word. Offered because these live in the SYSTEM dictionary — a user who
    /// doesn't want `worktree` accepted in Mail should be able to take it back.
    public static func forgetSeeded() {
        for word in seeded { unlearn(word) }
        UserDefaults.standard.set(0, forKey: seededKey)
    }

    /// Whether the corrector already accepts a word — used to show what's actually in effect rather
    /// than what we believe we asked for.
    public static func isKnown(_ word: String) -> Bool {
        #if os(macOS)
        return NSSpellChecker.shared.hasLearnedWord(word)
        #else
        return UITextChecker.hasLearnedWord(word)
        #endif
    }

    private static func learn(_ word: String) {
        #if os(macOS)
        // Guard first: learnWord is idempotent but does disk work, and this runs over ~100 words.
        if !NSSpellChecker.shared.hasLearnedWord(word) { NSSpellChecker.shared.learnWord(word) }
        #else
        if !UITextChecker.hasLearnedWord(word) { UITextChecker.learnWord(word) }
        #endif
    }

    private static func unlearn(_ word: String) {
        #if os(macOS)
        if NSSpellChecker.shared.hasLearnedWord(word) { NSSpellChecker.shared.unlearnWord(word) }
        #else
        if UITextChecker.hasLearnedWord(word) { UITextChecker.unlearnWord(word) }
        #endif
    }
}
