import SwiftUI
import AppKit

struct ContentView: View {
    @Environment(\.openWindow) private var openWindow
    @State private var daemonUp = false
    @State private var listenerReady = false
    @State private var listenAddr = "127.0.0.1:8099"
    @State private var checkedAt = ""
    @State private var login = false
    @State private var profiles: [ProfileStatus] = []
    @State private var logs: [String] = []
    @State private var showLogs = false
    @State private var banner = ""
    @State private var copiedID = ""
    @State private var showAdd = false
    @State private var addServer = ""
    @State private var addNamespace = ""
    @State private var addTeam = ""
    @State private var addAgent = ""
    @State private var addPlatform = "https://platform.mcpruntime.org"
    @State private var addRuntime = "https://mcp.mcpruntime.org"

    private let ink = Color(red: 0.09, green: 0.22, blue: 0.24)
    private let sea = Color(red: 0.07, green: 0.48, blue: 0.45)
    private let moss = Color(red: 0.16, green: 0.48, blue: 0.28)
    private let amber = Color(red: 0.72, green: 0.42, blue: 0.08)
    private let clay = Color(red: 0.62, green: 0.22, blue: 0.18)

    var body: some View {
        ZStack {
            LinearGradient(
                colors: [sea.opacity(0.16), Color(nsColor: .windowBackgroundColor)],
                startPoint: .top,
                endPoint: .center
            )
            .ignoresSafeArea()
            VStack(alignment: .leading, spacing: 0) {
                header
                if profiles.isEmpty {
                    ContentUnavailableView(
                        daemonUp ? "No profiles yet" : "Adapter status unavailable",
                        systemImage: daemonUp ? "point.3.connected.trianglepath.dotted" : "power",
                        description: Text(banner.isEmpty
                            ? "Add one with mcp-runtime adapter service profile add. Closing this window leaves the daemon running."
                            : banner)
                    )
                } else {
                    ScrollView {
                        LazyVStack(spacing: 12) {
                            ForEach(profiles) { profile in
                                profileCard(profile)
                            }
                        }
                        .padding(16)
                    }
                }
                if showLogs {
                    logPane
                }
            }
        }
        .frame(minWidth: 560, minHeight: 620)
        .onAppear { refresh() }
        .task { await poll() }
        .sheet(isPresented: $showAdd) { addSheet }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .center, spacing: 12) {
                ZStack {
                    RoundedRectangle(cornerRadius: 12, style: .continuous)
                        .fill(Color.white)
                        .frame(width: 42, height: 42)
                    if let mark = nsImage(named: "mcp-runtime-icon") {
                        Image(nsImage: mark)
                            .resizable()
                            .scaledToFit()
                            .frame(width: 32, height: 32)
                    }
                }
                VStack(alignment: .leading, spacing: 2) {
                    Text("MCP Runtime Adapter")
                        .font(.title3.weight(.semibold))
                        .foregroundStyle(.primary)
                    Text(daemonUp ? "Local profiles on \(listenAddr)" : "Daemon is not answering")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                statusPill
            }
            Text("Each card is one platform server. Your editor calls the local URL. The daemon uses that profile’s session certificate. Inspect opens the certificate identity and platform addresses. Closing this window leaves the daemon running.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if daemonUp && !profiles.isEmpty {
                HStack(spacing: 8) {
                    summaryChip("\(profiles.count) profiles", tone: sea)
                    summaryChip("\(enrolledCount) enrolled", tone: moss)
                    if blockedCount > 0 {
                        summaryChip("\(blockedCount) need attention", tone: amber)
                    }
                }
            }
            HStack(spacing: 8) {
                metaChip(login ? "Starts at login" : "Manual start", systemImage: "power")
                if !checkedAt.isEmpty {
                    metaChip(shortTime(checkedAt), systemImage: "clock")
                }
                Spacer()
                Button("Add profile") { showAdd = true }
                    .buttonStyle(.borderedProminent)
                    .tint(sea)
                Button("Refresh") { refresh() }
                    .buttonStyle(.bordered)
                Button(showLogs ? "Hide logs" : "Logs") {
                    showLogs.toggle()
                    if showLogs { loadLogs() }
                }
                .buttonStyle(.bordered)
            }
            if !banner.isEmpty {
                Text(banner)
                    .font(.caption)
                    .foregroundStyle(clay)
                    .textSelection(.enabled)
            }
        }
        .padding(18)
    }

    private var statusPill: some View {
        let ready = daemonUp && listenerReady
        return HStack(spacing: 6) {
            Circle()
                .fill(ready ? moss : amber)
                .frame(width: 8, height: 8)
            Text(daemonUp ? (listenerReady ? "Ready" : "Starting") : "Stopped")
                .font(.caption.weight(.semibold))
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
        .background((ready ? moss : amber).opacity(0.14), in: Capsule())
        .foregroundStyle(ready ? moss : amber)
    }

    private func profileCard(_ profile: ProfileStatus) -> some View {
        let tone = authColor(profile.authState)
        return VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .firstTextBaseline) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(profile.server)
                        .font(.title3.weight(.semibold))
                    if let namespace = profile.namespace, !namespace.isEmpty {
                        Text(namespace)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                Spacer()
                Text(authLabel(profile.authState))
                    .font(.caption.weight(.bold))
                    .padding(.horizontal, 10)
                    .padding(.vertical, 5)
                    .background(tone.opacity(0.16), in: Capsule())
                    .foregroundStyle(tone)
            }
            VStack(alignment: .leading, spacing: 4) {
                Text("Local URL")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(.secondary)
                Text(profile.localUrl)
                    .font(.callout.monospaced())
                    .foregroundStyle(sea)
                    .textSelection(.enabled)
            }
            VStack(alignment: .leading, spacing: 6) {
                Label(frontLine(profile), systemImage: "checkmark.seal")
                    .font(.callout)
                    .foregroundStyle(certTone(profile))
                Label(upstreamLine(profile), systemImage: "network")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                Text(nextStep(profile))
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                if let problem = shortProblem(profile) {
                    Text(problem)
                        .font(.caption)
                        .foregroundStyle(clay)
                        .textSelection(.enabled)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            HStack {
                Button(copiedID == profile.id ? "Copied" : "Copy local URL") { copy(profile) }
                    .buttonStyle(.borderedProminent)
                    .tint(sea)
                Button("Inspect") { openWindow(id: "inspect", value: profile.id) }
                    .buttonStyle(.bordered)
                Button("Reconnect") { reconnect(profile.id) }
                    .buttonStyle(.bordered)
            }
            .controlSize(.small)
        }
        .padding(14)
        .background(Color(nsColor: .controlBackgroundColor), in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .overlay(alignment: .leading) {
            RoundedRectangle(cornerRadius: 2)
                .fill(tone)
                .frame(width: 4)
                .padding(.vertical, 12)
        }
        .shadow(color: ink.opacity(0.06), radius: 8, y: 3)
    }

    private var logPane: some View {
        ScrollView {
            Text(logs.isEmpty ? "No daemon log lines yet. A missing grant is shown on the profile card." : logs.joined(separator: "\n"))
                .font(.system(.caption, design: .monospaced))
                .frame(maxWidth: .infinity, alignment: .leading)
                .textSelection(.enabled)
                .padding(12)
        }
        .frame(height: 140)
        .background(ink.opacity(0.06))
    }

    private var enrolledCount: Int {
        profiles.filter { $0.authState == "enrolled" }.count
    }

    private var blockedCount: Int {
        profiles.filter { $0.authState != "enrolled" }.count
    }

    private func summaryChip(_ title: String, tone: Color) -> some View {
        Text(title)
            .font(.caption.weight(.semibold))
            .padding(.horizontal, 10)
            .padding(.vertical, 5)
            .background(tone.opacity(0.14), in: Capsule())
            .foregroundStyle(tone)
    }

    private func frontLine(_ profile: ProfileStatus) -> String {
        guard profile.authState == "enrolled" else {
            return "No session certificate yet"
        }
        guard let exp = profile.certExpires, let date = ISO8601DateFormatter().date(from: exp) else {
            return "Session certificate is enrolled"
        }
        let minutes = Int(date.timeIntervalSinceNow / 60)
        if minutes < 0 {
            return "Session certificate expired \(shortTime(exp))"
        }
        if minutes < 90 {
            return "Session certificate expires in \(minutes) minutes. The daemon renews it during the last 5 minutes."
        }
        return "Session certificate expires \(shortTime(exp))"
    }

    private func certTone(_ profile: ProfileStatus) -> Color {
        guard profile.authState == "enrolled",
              let exp = profile.certExpires,
              let date = ISO8601DateFormatter().date(from: exp) else {
            return .primary
        }
        if date.timeIntervalSinceNow < 15 * 60 {
            return amber
        }
        return .primary
    }

    private func upstreamLine(_ profile: ProfileStatus) -> String {
        switch profile.upstreamReachable {
        case true: return "Platform host answered the last check"
        case false: return "Platform host did not answer the last check"
        default: return "Platform host has not been checked yet"
        }
    }

    private func nextStep(_ profile: ProfileStatus) -> String {
        switch profile.authState {
        case "enrolled":
            return "Point the editor at the local URL. Reconnect refreshes this profile’s connection."
        case "sign_in_required":
            return "This server needs an access grant. After the grant exists, use Reconnect."
        case "renewal_failed":
            return "The session certificate could not be renewed. Inspect shows the safe details."
        default:
            return "Use Reconnect to ask the daemon to enroll this profile."
        }
    }

    private func shortProblem(_ profile: ProfileStatus) -> String? {
        redacted(profile.renewalError ?? profile.lastError)
    }

    private func redacted(_ raw: String?) -> String? {
        guard var text = raw, !text.isEmpty else { return nil }
        if let regex = try? NSRegularExpression(pattern: "[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}") {
            let range = NSRange(text.startIndex..., in: text)
            text = regex.stringByReplacingMatches(in: text, range: range, withTemplate: "[id]")
        }
        if let bearer = text.range(of: "bearer ", options: .caseInsensitive) {
            text = String(text[..<bearer.upperBound]) + "[redacted]"
        }
        return text
    }

    private func nsImage(named name: String) -> NSImage? {
        if let url = Bundle.main.url(forResource: name, withExtension: "png") {
            return NSImage(contentsOf: url)
        }
        return nil
    }

    private func metaChip(_ title: String, systemImage: String) -> some View {
        Label(title, systemImage: systemImage)
            .font(.caption)
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .background(Color(nsColor: .controlBackgroundColor), in: Capsule())
    }

    private func authLabel(_ state: String) -> String {
        switch state {
        case "enrolled": return "Enrolled"
        case "sign_in_required": return "Needs a grant"
        case "renewal_failed": return "Renewal failed"
        default: return "Not enrolled"
        }
    }

    private func authColor(_ state: String) -> Color {
        switch state {
        case "enrolled": return moss
        case "sign_in_required", "renewal_failed": return amber
        default: return .secondary
        }
    }

    private func shortTime(_ raw: String) -> String {
        if let date = ISO8601DateFormatter().date(from: raw) {
            return date.formatted(date: .abbreviated, time: .shortened)
        }
        return raw
    }

    private func refresh() {
        do {
            let status = try ControlClient.status()
            daemonUp = true
            listenerReady = status.listenerReady
            listenAddr = status.listenAddr
            checkedAt = status.checkedAt
            login = status.startupAtLogin
            profiles = status.profiles
            banner = ""
        } catch {
            daemonUp = false
            profiles = []
            banner = error.localizedDescription
        }
    }

    private func loadLogs() {
        do {
            logs = try ControlClient.logs()
        } catch {
            logs = [error.localizedDescription]
        }
    }

    private func reconnect(_ id: String) {
        do {
            try ControlClient.restart(id: id)
            refresh()
        } catch {
            banner = error.localizedDescription
        }
    }

    private var addSheet: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Add a profile")
                .font(.title3.weight(.semibold))
            Text("A profile is one platform server. The daemon enrolls a session certificate for it and serves a local URL.")
                .font(.callout)
                .foregroundStyle(.secondary)
            field("Server", text: $addServer)
            field("Namespace", text: $addNamespace)
            field("Team", text: $addTeam)
            field("Agent", text: $addAgent)
            field("Platform API", text: $addPlatform)
            field("Upstream MCP URL", text: $addRuntime)
            HStack {
                Button("Cancel") { showAdd = false }
                Spacer()
                Button("Add") { submitAdd() }
                    .buttonStyle(.borderedProminent)
                    .tint(sea)
                    .disabled(addServer.isEmpty || addRuntime.isEmpty)
            }
        }
        .padding(20)
        .frame(width: 460)
    }

    private func field(_ title: String, text: Binding<String>) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title).font(.caption).foregroundStyle(.secondary)
            TextField(title, text: text)
                .textFieldStyle(.roundedBorder)
        }
    }

    private func submitAdd() {
        let runtime = addRuntime.hasSuffix("/mcp") ? addRuntime : addRuntime + "/" + addServer + "/mcp"
        do {
            try ControlClient.addProfile(server: addServer, namespace: addNamespace, team: addTeam, agent: addAgent, platformURL: addPlatform, runtimeURL: runtime)
            showAdd = false
            addServer = ""
            refresh()
        } catch {
            banner = error.localizedDescription
            showAdd = false
        }
    }

    private func copy(_ profile: ProfileStatus) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(profile.localUrl, forType: .string)
        copiedID = profile.id
    }

    private func poll() async {
        while !Task.isCancelled {
            try? await Task.sleep(for: .seconds(5))
            refresh()
        }
    }
}
