import SwiftUI
import OculusKit

/// Accounts & Usage: keep multiple credentials per agent (personal / work logins or API keys) and
/// hot-swap which one new sessions use, alongside a per-provider token/cost usage meter. Env keys
/// are stored 0600 on the daemon host; this view is the switcher + meter.
struct AccountsView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onClose: () -> Void

    @State private var showAdd = false

    private var byProvider: [(provider: String, accounts: [Account])] {
        let groups = Dictionary(grouping: model.accounts, by: { $0.provider })
        return groups.keys.sorted().map { (provider: $0, accounts: groups[$0] ?? []) }
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Label("Accounts & Usage", systemImage: "person.2.badge.key").font(.headline)
                Spacer()
                Button { showAdd = true } label: { Label("Add", systemImage: "plus") }
                Button("Done", action: onClose).keyboardShortcut(.cancelAction)
            }
            .padding(16)
            Divider().overlay(palette.border)

            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    usageSection
                    if model.accounts.isEmpty {
                        emptyState
                    } else {
                        ForEach(byProvider, id: \.provider) { group in
                            providerSection(group.provider, group.accounts)
                        }
                    }
                }
                .padding(16)
            }
        }
        .frame(minWidth: 520, minHeight: 440)
        .background(palette.background)
        .task { await model.loadAccounts() }
        .sheet(isPresented: $showAdd) {
            AddAccountSheet(model: model, palette: palette) { showAdd = false }
        }
    }

    private var usageSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("USAGE").font(.system(size: 10.5, weight: .semibold)).tracking(0.8).foregroundStyle(palette.mutedForeground)
            if model.providerUsage.isEmpty {
                Text("No usage yet this session.").font(.callout).foregroundStyle(palette.mutedForeground)
            } else {
                ForEach(model.providerUsage.sorted { $0.costUSD > $1.costUSD }) { u in
                    HStack(spacing: 10) {
                        Text(u.provider).font(.system(size: 13, weight: .medium)).frame(width: 110, alignment: .leading)
                        Text("\(u.sessions) session\(u.sessions == 1 ? "" : "s")").font(.system(size: 11, design: .monospaced)).foregroundStyle(palette.mutedForeground)
                        Spacer()
                        Text("\(fmtTokens(u.inputTokens + u.outputTokens)) tok").font(.system(size: 11, design: .monospaced)).foregroundStyle(palette.mutedForeground)
                        if u.costUSD > 0 {
                            Text(String(format: "$%.3f", u.costUSD)).font(.system(size: 12, weight: .semibold, design: .monospaced)).foregroundStyle(palette.primary)
                        }
                    }
                    .padding(.vertical, 6).padding(.horizontal, 10)
                    .background(RoundedRectangle(cornerRadius: 8).fill(palette.secondary.opacity(0.4)))
                }
            }
        }
    }

    private func providerSection(_ provider: String, _ accounts: [Account]) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(provider.uppercased()).font(.system(size: 10.5, weight: .semibold)).tracking(0.8).foregroundStyle(palette.mutedForeground)
            ForEach(accounts) { a in
                HStack(spacing: 10) {
                    Image(systemName: a.active == true ? "largecircle.fill.circle" : "circle")
                        .foregroundStyle(a.active == true ? palette.primary : palette.mutedForeground)
                    VStack(alignment: .leading, spacing: 1) {
                        Text(a.name).font(.system(size: 13, weight: .medium)).foregroundStyle(palette.foreground)
                        if let env = a.env, !env.isEmpty {
                            Text(env.keys.sorted().joined(separator: ", ")).font(.system(size: 10, design: .monospaced)).foregroundStyle(palette.mutedForeground).lineLimit(1)
                        }
                    }
                    Spacer()
                    if a.active == true {
                        Text("ACTIVE").font(.system(size: 9, weight: .bold)).tracking(0.5).foregroundStyle(palette.primary)
                    } else {
                        Button("Use") { Task { await model.activateAccount(provider: provider, id: a.id) } }
                            .buttonStyle(.bordered).controlSize(.small)
                    }
                    Button { Task { await model.deleteAccount(a.id) } } label: { Image(systemName: "trash").font(.system(size: 11)) }
                        .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
                }
                .padding(.vertical, 7).padding(.horizontal, 10)
                .background(RoundedRectangle(cornerRadius: 8).fill(palette.secondary.opacity(a.active == true ? 0.6 : 0.3)))
            }
        }
    }

    private var emptyState: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("No accounts yet").font(.subheadline.weight(.medium)).foregroundStyle(palette.foreground)
            Text("Add a named credential (API key or config-dir env) per agent, then hot-swap which one new sessions use.")
                .font(.callout).foregroundStyle(palette.mutedForeground)
        }
    }

    private func fmtTokens(_ n: Int) -> String {
        if n >= 1_000_000 { return String(format: "%.1fM", Double(n) / 1_000_000) }
        if n >= 1_000 { return String(format: "%.1fk", Double(n) / 1_000) }
        return "\(n)"
    }
}

/// Add/edit an account: provider + name + a small env-var table (KEY=value pairs).
struct AddAccountSheet: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onClose: () -> Void

    @State private var provider = ""
    @State private var name = ""
    @State private var rows: [EnvRow] = [EnvRow()]

    struct EnvRow: Identifiable { let id = UUID(); var key = ""; var value = "" }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                Text("Add account").font(.headline)
                Spacer()
                Button("Cancel", action: onClose).keyboardShortcut(.cancelAction)
            }
            HStack(spacing: 12) {
                VStack(alignment: .leading, spacing: 4) {
                    Text("AGENT").font(.system(size: 10, weight: .semibold)).foregroundStyle(palette.mutedForeground)
                    Picker("", selection: $provider) {
                        Text("—").tag("")
                        ForEach(model.providers, id: \.self) { Text($0).tag($0) }
                    }.labelsHidden().frame(minWidth: 130)
                }
                VStack(alignment: .leading, spacing: 4) {
                    Text("NAME").font(.system(size: 10, weight: .semibold)).foregroundStyle(palette.mutedForeground)
                    TextField("e.g. Work", text: $name).textFieldStyle(.roundedBorder).frame(width: 160)
                }
            }
            Text("ENV OVERRIDES (API keys / config dirs)").font(.system(size: 10, weight: .semibold)).foregroundStyle(palette.mutedForeground)
            ForEach($rows) { $row in
                HStack(spacing: 8) {
                    TextField("KEY", text: $row.key).textFieldStyle(.roundedBorder).frame(width: 180)
                    TextField("value", text: $row.value).textFieldStyle(.roundedBorder)
                }
            }
            Button { rows.append(EnvRow()) } label: { Label("Add variable", systemImage: "plus").font(.caption) }
                .buttonStyle(.plain).foregroundStyle(palette.primary)
            HStack {
                Spacer()
                Button {
                    var env: [String: String] = [:]
                    for r in rows where !r.key.trimmingCharacters(in: .whitespaces).isEmpty { env[r.key] = r.value }
                    Task {
                        await model.upsertAccount(Account(provider: provider, name: name, env: env.isEmpty ? nil : env))
                        onClose()
                    }
                } label: { Text("Save account") }
                .buttonStyle(.borderedProminent).tint(palette.primary)
                .disabled(provider.isEmpty || name.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
        .padding(20).frame(width: 480).background(palette.background)
        .onAppear { if provider.isEmpty { provider = model.providers.first ?? "" } }
    }
}
