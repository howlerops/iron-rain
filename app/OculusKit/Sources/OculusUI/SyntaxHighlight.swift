import SwiftUI
#if canImport(AppKit)
import AppKit
#endif

/// Lightweight, dependency-free syntax highlighting for the built-in editor. A regex tokenizer
/// covers the common languages (keywords, strings, comments, numbers, types) — enough for a
/// native, readable code surface. It emits `NSRange` tokens once so the same result drives both
/// the AppKit editor and the SwiftUI diff renderer. (A tree-sitter backend can replace
/// `tokens(_:language:)` later behind this same interface.)
enum CodeToken: Equatable {
    case plain, keyword, string, comment, number, type
}

/// A code color theme, independent of the app's gold/mono chrome — editors carry their own
/// syntax palette. Light ≈ GitHub, dark ≈ One Dark.
struct CodeTheme {
    let plain: Color
    let keyword: Color
    let string: Color
    let comment: Color
    let number: Color
    let type: Color
    let background: Color
    /// Rainbow bracket colors, cycled by nesting depth (VSCode's default palette).
    let bracketColors: [Color]

    func color(_ t: CodeToken) -> Color {
        switch t {
        case .plain: return plain
        case .keyword: return keyword
        case .string: return string
        case .comment: return comment
        case .number: return number
        case .type: return type
        }
    }

    /// Bracket color for a given nesting depth (cycles through bracketColors).
    func bracketColor(_ depth: Int) -> Color {
        bracketColors.isEmpty ? plain : bracketColors[((depth % bracketColors.count) + bracketColors.count) % bracketColors.count]
    }

    static func current(_ scheme: ColorScheme) -> CodeTheme {
        scheme == .dark ? .dark : .light
    }

    static let dark = CodeTheme(
        plain: Color(hex: 0xABB2BF), keyword: Color(hex: 0xC678DD), string: Color(hex: 0x98C379),
        comment: Color(hex: 0x5C6370), number: Color(hex: 0xD19A66), type: Color(hex: 0xE5C07B),
        background: Color(hex: 0x1C1C1C),
        bracketColors: [Color(hex: 0xFFD700), Color(hex: 0xDA70D6), Color(hex: 0x179FFF)])
    static let light = CodeTheme(
        plain: Color(hex: 0x24292E), keyword: Color(hex: 0xD73A49), string: Color(hex: 0x22863A),
        comment: Color(hex: 0x6A737D), number: Color(hex: 0x005CC5), type: Color(hex: 0x6F42C1),
        background: Color(hex: 0xFBFBFB),
        bracketColors: [Color(hex: 0xB8860B), Color(hex: 0x9400D3), Color(hex: 0x0066CC)])
}

/// A source language, inferred from a file extension. Drives the keyword set.
enum CodeLanguage {
    case swift, go, jsTs, python, rustC, json, markdown, shell, plain

    static func infer(fromPath path: String) -> CodeLanguage {
        let ext = (path as NSString).pathExtension.lowercased()
        switch ext {
        case "swift": return .swift
        case "go": return .go
        case "js", "jsx", "ts", "tsx", "mjs", "cjs": return .jsTs
        case "py": return .python
        case "rs", "c", "h", "cpp", "cc", "hpp", "m", "java", "kt": return .rustC
        case "json": return .json
        case "md", "markdown": return .markdown
        case "sh", "bash", "zsh": return .shell
        default: return .plain
        }
    }

    var keywords: Set<String> {
        switch self {
        case .swift:
            return ["func", "let", "var", "if", "else", "guard", "return", "for", "in", "while", "switch",
                    "case", "default", "struct", "class", "enum", "protocol", "extension", "import", "public",
                    "private", "internal", "fileprivate", "static", "self", "init", "deinit", "throws", "try",
                    "async", "await", "throw", "do", "catch", "nil", "true", "false", "some", "where", "as",
                    "is", "weak", "unowned", "lazy", "override", "final", "mutating", "typealias", "associatedtype"]
        case .go:
            return ["func", "var", "const", "if", "else", "return", "for", "range", "switch", "case", "default",
                    "struct", "interface", "type", "package", "import", "go", "defer", "chan", "select", "map",
                    "nil", "true", "false", "break", "continue", "fallthrough", "goto"]
        case .jsTs:
            return ["function", "let", "const", "var", "if", "else", "return", "for", "while", "switch", "case",
                    "default", "class", "extends", "import", "export", "from", "async", "await", "new", "this",
                    "null", "undefined", "true", "false", "typeof", "instanceof", "interface", "type", "enum",
                    "public", "private", "readonly", "try", "catch", "throw", "yield"]
        case .python:
            return ["def", "class", "if", "elif", "else", "return", "for", "while", "import", "from", "as",
                    "with", "try", "except", "finally", "raise", "lambda", "None", "True", "False", "and", "or",
                    "not", "in", "is", "async", "await", "yield", "pass", "break", "continue", "global"]
        case .rustC:
            return ["fn", "let", "mut", "if", "else", "return", "for", "while", "loop", "match", "struct",
                    "enum", "impl", "trait", "pub", "use", "mod", "const", "static", "int", "char", "void",
                    "class", "public", "private", "return", "new", "delete", "true", "false", "null", "self"]
        case .shell:
            return ["if", "then", "else", "elif", "fi", "for", "while", "do", "done", "case", "esac", "function",
                    "return", "export", "local", "echo", "cd", "in"]
        case .json, .markdown, .plain:
            return []
        }
    }
}

/// Produces syntax tokens for `text`. Order matters: comments/strings are matched first and
/// win over keyword/number matches inside them.
enum SyntaxHighlighter {
    static func tokens(_ text: String, language: CodeLanguage) -> [(NSRange, CodeToken)] {
        if language == .plain || language == .json {
            return jsonOrPlainTokens(text, json: language == .json)
        }
        let ns = text as NSString
        let full = NSRange(location: 0, length: ns.length)
        var claimed = [Bool](repeating: false, count: ns.length)
        var out: [(NSRange, CodeToken)] = []

        func mark(_ r: NSRange) -> Bool {
            if r.location < 0 || NSMaxRange(r) > claimed.count { return false }
            for i in r.location..<NSMaxRange(r) where claimed[i] { return false }
            for i in r.location..<NSMaxRange(r) { claimed[i] = true }
            return true
        }
        func apply(_ pattern: String, _ kind: CodeToken, options: NSRegularExpression.Options = []) {
            guard let re = try? NSRegularExpression(pattern: pattern, options: options) else { return }
            for m in re.matches(in: text, range: full) where mark(m.range) {
                out.append((m.range, kind))
            }
        }

        // Comments first (line + block), then strings, so tokens inside them aren't re-colored.
        apply("//[^\\n]*", .comment)
        apply("#[^\\n]*", .comment) // python/shell line comments
        apply("/\\*[\\s\\S]*?\\*/", .comment)
        apply("\"(?:\\\\.|[^\"\\\\\\n])*\"", .string)
        apply("'(?:\\\\.|[^'\\\\\\n])*'", .string)
        apply("`(?:\\\\.|[^`\\\\])*`", .string)
        apply("\\b[A-Z][A-Za-z0-9_]*\\b", .type) // Capitalized identifiers ≈ types
        apply("\\b\\d[\\d_.eExXa-fA-F]*\\b", .number)

        // Keywords last (whole-word), only on still-unclaimed spans.
        let kw = language.keywords
        if !kw.isEmpty, let re = try? NSRegularExpression(pattern: "\\b[A-Za-z_][A-Za-z0-9_]*\\b") {
            for m in re.matches(in: text, range: full) {
                let word = ns.substring(with: m.range)
                if kw.contains(word) && mark(m.range) { out.append((m.range, .keyword)) }
            }
        }
        return out
    }

    private static func jsonOrPlainTokens(_ text: String, json: Bool) -> [(NSRange, CodeToken)] {
        guard json else { return [] }
        let full = NSRange(location: 0, length: (text as NSString).length)
        var out: [(NSRange, CodeToken)] = []
        if let s = try? NSRegularExpression(pattern: "\"(?:\\\\.|[^\"\\\\])*\"") {
            for m in s.matches(in: text, range: full) { out.append((m.range, .string)) }
        }
        if let n = try? NSRegularExpression(pattern: "\\b-?\\d[\\d.eE+-]*\\b") {
            for m in n.matches(in: text, range: full) { out.append((m.range, .number)) }
        }
        if let b = try? NSRegularExpression(pattern: "\\b(true|false|null)\\b") {
            for m in b.matches(in: text, range: full) { out.append((m.range, .keyword)) }
        }
        return out
    }

    /// Builds a SwiftUI AttributedString for read-only rendering (diff view, iOS).
    static func attributedString(_ text: String, language: CodeLanguage, theme: CodeTheme) -> AttributedString {
        var attr = AttributedString(text)
        attr.foregroundColor = theme.plain
        let ns = text as NSString
        for (range, kind) in tokens(text, language: language) where kind != .plain {
            guard let r = Range(range, in: text),
                  let lo = AttributedString.Index(r.lowerBound, within: attr),
                  let hi = AttributedString.Index(r.upperBound, within: attr) else { continue }
            _ = ns // keep ns referenced for clarity; ranges are String-based
            attr[lo..<hi].foregroundColor = theme.color(kind)
        }
        return attr
    }
}
