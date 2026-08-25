import SwiftUI
import OculusKit

/// Choosing what to work on by REPOSITORY rather than by path.
///
/// What this replaces: every folder the daemon had ever seen an agent run in, listed in full, with no
/// search. Auto-registration adds them and nothing removes them, so the list filled with worktrees,
/// three checkouts of the same repo, and temp directories under /var/folders — and on a work machine
/// it was long enough that finding anything meant scrolling past all of it.
///
/// A repository name is the thing a person actually knows. Which of nine directories holds it is a
/// question the machine can answer, so it does: the daemon reports where each repo is already checked
/// out, and offers to clone the ones that are not.
struct GitHubPicker: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    /// Called with a path on the daemon host once a repo has been resolved to one.
    var onPicked: (String) -> Void

    @State private var answer: GitHubRepos?
    @State private var loading = true
    @State private var failure: String?
    @State private var query = ""
    @State private var cloning: String?
    @State private var cloneRoot = ""
    @FocusState private var searchFocused: Bool

    /// How many rows to render before asking the user to narrow the search.
    ///
    /// The point of this screen is to stop being a wall of rows; showing a hundred would rebuild the
    /// thing it replaces. Already-cloned repos sort first daemon-side, so the visible set is the
    /// useful one.
    private static let visibleLimit = 25

    private var repos: [GitHubRepo] { answer?.repos ?? [] }

    private var filtered: [GitHubRepo] {
        let q = query.trimmingCharacters(in: .whitespaces).lowercased()
        guard !q.isEmpty else { return repos }
        return repos.filter {
            $0.nameWithOwner.lowercased().contains(q) ||
            ($0.description ?? "").lowercased().contains(q)
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            header
            if loading {
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small)
                    Text("Asking GitHub…").font(.caption).foregroundStyle(palette.mutedForeground)
                }
                .frame(minHeight: 44)
            } else if let reason = failure ?? (answer?.available == false ? answer?.reason : nil) {
                unavailable(reason)
            } else {
                search
                list
            }
        }
        .task { await load() }
    }

    private var header: some View {
        HStack(spacing: 6) {
            Image(systemName: "chevron.left.forwardslash.chevron.right")
                .font(.caption).foregroundStyle(palette.mutedForeground).accessibilityHidden(true)
            Text("Repository").font(.footnote.weight(.semibold)).foregroundStyle(palette.mutedForeground)
            if let account = answer?.account, !account.isEmpty {
                Text(account).font(.caption2).foregroundStyle(palette.mutedForeground)
                    .padding(.horizontal, 5).padding(.vertical, 1)
                    .background(Capsule().fill(palette.muted.opacity(0.4)))
            }
            Spacer(minLength: 0)
            if !loading {
                Button { Task { await load(force: true) } } label: {
                    Image(systemName: "arrow.clockwise").font(.caption)
                        .foregroundStyle(palette.mutedForeground)
                        .frame(minWidth: 44, minHeight: 44, alignment: .trailing)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Refresh repositories")
            }
        }
    }

    /// gh missing or signed out is an ordinary state on a fresh machine, not an error to apologise
    /// for — so it says the one command that fixes it and leaves the folder browser reachable.
    private func unavailable(_ reason: String) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .top, spacing: 7) {
                Image(systemName: "info.circle").font(.caption)
                    .foregroundStyle(palette.mutedForeground).accessibilityHidden(true)
                Text(reason).font(.caption).foregroundStyle(palette.foreground)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Text("You can still pick a folder below.")
                .font(.caption2).foregroundStyle(palette.mutedForeground)
        }
        .padding(9)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(OculusShape.rounded(OculusRadius.sm).fill(palette.muted.opacity(0.25)))
    }

    private var search: some View {
        HStack(spacing: 6) {
            Image(systemName: "magnifyingglass").font(.caption)
                .foregroundStyle(palette.mutedForeground).accessibilityHidden(true)
            TextField("Search \(repos.count) repositories", text: $query)
                .textFieldStyle(.plain)
                .plainInput()
                .focused($searchFocused)
                .submitLabel(.search)
            if !query.isEmpty {
                Button { query = "" } label: {
                    Image(systemName: "xmark.circle.fill").font(.caption)
                        .foregroundStyle(palette.mutedForeground)
                        .frame(minWidth: 44, minHeight: 44, alignment: .trailing)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Clear search")
            }
        }
        .padding(.horizontal, 10).padding(.vertical, 7)
        .frame(minHeight: 44)
        .background(OculusShape.rounded(OculusRadius.sm).fill(palette.muted.opacity(0.25)))
    }

    @ViewBuilder private var list: some View {
        let shown = Array(filtered.prefix(Self.visibleLimit))
        if filtered.isEmpty {
            Text(query.isEmpty
                 ? "No repositories came back for this account."
                 : "Nothing matches “\(query)”.")
                .font(.caption).foregroundStyle(palette.mutedForeground)
                .frame(maxWidth: .infinity, alignment: .leading).padding(.vertical, 8)
        } else {
            VStack(spacing: 5) {
                ForEach(shown) { row($0) }
            }
            if filtered.count > shown.count {
                Text("\(filtered.count - shown.count) more — narrow the search to see them.")
                    .font(.caption2).foregroundStyle(palette.mutedForeground)
            }
        }
    }

    private func row(_ repo: GitHubRepo) -> some View {
        let onDisk = repo.localPath?.isEmpty == false
        let busy = cloning == repo.nameWithOwner
        return Button {
            Task { await pick(repo) }
        } label: {
            HStack(spacing: 10) {
                Image(systemName: onDisk ? "internaldrive" : "arrow.down.circle")
                    .font(.subheadline)
                    .foregroundStyle(onDisk ? palette.primary : palette.mutedForeground)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 1) {
                    HStack(spacing: 5) {
                        Text(repo.name).font(.footnote.weight(.medium)).foregroundStyle(palette.foreground)
                            .lineLimit(1)
                        if repo.isPrivate == true {
                            Image(systemName: "lock.fill").font(.caption2)
                                .foregroundStyle(palette.mutedForeground)
                                .accessibilityLabel("Private")
                        }
                        if let lang = repo.language, !lang.isEmpty {
                            Text(lang).font(.caption2).foregroundStyle(palette.mutedForeground)
                        }
                    }
                    // The owner matters: three of these can share a name across orgs, which is
                    // exactly the ambiguity the old folder list made someone resolve by path.
                    Text(onDisk ? ((repo.localPath! as NSString).abbreviatingWithTildeInPath) : repo.nameWithOwner)
                        .font(.caption).foregroundStyle(palette.mutedForeground)
                        .lineLimit(1).truncationMode(.middle)
                }
                Spacer(minLength: 0)
                if busy {
                    ProgressView().controlSize(.small)
                } else if !onDisk {
                    Text("Clone").font(.caption2.weight(.semibold))
                        .foregroundStyle(palette.primaryText)
                        .padding(.horizontal, 6).padding(.vertical, 2)
                        .background(Capsule().fill(palette.primary.opacity(0.16)))
                }
            }
            .padding(.horizontal, 10).padding(.vertical, 8)
            .frame(minHeight: 44)
            .background(OculusShape.rounded(OculusRadius.sm).fill(palette.muted.opacity(0.22)))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(cloning != nil)
        .accessibilityLabel(repo.nameWithOwner)
        .accessibilityHint(onDisk ? "Already on this machine. Adds it to the session."
                                  : "Not checked out. Clones it, then adds it to the session.")
    }

    private func load(force: Bool = false) async {
        if !force && answer != nil { return }
        loading = true
        failure = nil
        do {
            let a = try await model.githubRepos()
            answer = a
            cloneRoot = a.cloneRoots?.first ?? ""
        } catch {
            failure = error.localizedDescription
        }
        loading = false
    }

    /// Resolves a repo to a directory on the daemon host, cloning first when it isn't there yet.
    private func pick(_ repo: GitHubRepo) async {
        if let local = repo.localPath, !local.isEmpty {
            onPicked(local)
            return
        }
        guard !cloneRoot.isEmpty else {
            failure = "There's nowhere to clone into yet. Add a folder below once, and it becomes the default."
            return
        }
        cloning = repo.nameWithOwner
        defer { cloning = nil }
        do {
            let path = try await model.githubClone(nameWithOwner: repo.nameWithOwner, parent: cloneRoot)
            onPicked(path)
            // Refresh so the row flips to "on disk" rather than still offering to clone what was
            // just cloned.
            await load(force: true)
        } catch {
            failure = error.localizedDescription
        }
    }
}
