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
    @State private var bannerIsError = false
    @State private var statusError = ""
    @State private var copiedID = ""
    @State private var signInCommandCopied = false
    @State private var showAdd = false
    @State private var busy = false
    @State private var serviceAction = ""
    @State private var refreshing = false
    @State private var addServer = ""
    @State private var addNamespace = ""
    @State private var addAgent = ""
    @State private var addRuntime = "https://mcp.mcpruntime.org"
    @State private var addRuntimeIsFullEndpoint = false
    @State private var addError = ""
    @State private var showAdvancedFields = false
    @State private var confirmStopService = false
    @State private var confirmUninstallService = false
    @State private var profileToRemove: ProfileStatus?
    @FocusState private var focusedField: FormField?
    private enum FormField: Hashable { case server, agent, namespace, endpoint }

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
                    emptyState
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
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("adapter.root")
        .frame(minWidth: 660, minHeight: 620)
        .onAppear { refresh() }
        .task { await poll() }
        .sheet(isPresented: $showAdd) { addSheet }
        .confirmationDialog("Stop the adapter service?", isPresented: $confirmStopService, titleVisibility: .visible) {
            Button("Stop service", role: .destructive) { manage("stop") }
        } message: {
            Text("Agents will lose access to all local MCP URLs until the service starts again.")
        }
        .confirmationDialog("Uninstall the adapter service?", isPresented: $confirmUninstallService, titleVisibility: .visible) {
            Button("Uninstall service", role: .destructive) { manage("uninstall") }
        } message: {
            Text("This removes the login item and bundled helper. Saved profiles and credentials will remain on this Mac.")
        }
        .confirmationDialog("Remove this local profile?", isPresented: Binding(
            get: { profileToRemove != nil },
            set: { if !$0 { profileToRemove = nil } }
        ), titleVisibility: .visible) {
            Button("Remove profile and local credentials", role: .destructive) {
                if let profileToRemove { removeProfile(profileToRemove) }
                profileToRemove = nil
            }
        } message: {
            Text("This removes the profile, local token, and certificate from this Mac. It does not revoke the platform session.")
        }
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
                    Text(!serviceAction.isEmpty ? serviceProgress : (daemonUp ? "Local profiles on \(listenAddr)" : "Daemon is not answering"))
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                statusPill
            }
            Text("Connect your agent to a platform server. The adapter keeps its local MCP endpoint ready, even after you close this window.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if daemonUp && !profiles.isEmpty {
                HStack(spacing: 8) {
                    summaryChip("\(profiles.count) \(profiles.count == 1 ? "profile" : "profiles")", tone: sea)
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
                if !ControlClient.launchAgentInstalled() && !profiles.isEmpty {
                    Button("Install and start") { manage("install") }
                        .buttonStyle(.borderedProminent)
                        .tint(sea)
                        .disabled(busy)
                } else if ControlClient.launchAgentInstalled() {
                    Menu {
                        Button("Start service") { manage("start") }
                        Button("Restart service") { manage("restart") }
                        Divider()
                        Button("Stop service…", role: .destructive) { confirmStopService = true }
                        Button("Uninstall service…", role: .destructive) { confirmUninstallService = true }
                    } label: {
                        Label("Service", systemImage: "gearshape")
                    }
                    .disabled(busy)
                }
            }
            HStack(spacing: 8) {
                Spacer()
                Button {
                    showLogs.toggle()
                    if showLogs { loadLogs() }
                } label: {
                    Label(showLogs ? "Hide logs" : "Logs", systemImage: "text.alignleft")
                }
                .buttonStyle(.bordered)
                .accessibilityLabel(showLogs ? "Hide logs" : "Logs")
                .accessibilityIdentifier("adapter.logs")
                Button {
                    refresh()
                } label: {
                    Label("Refresh", systemImage: "arrow.clockwise")
                }
                .buttonStyle(.bordered)
                .accessibilityLabel("Refresh")
                .accessibilityIdentifier("adapter.refresh")
                .disabled(refreshing)
                Button {
                    addError = ""
                    showAdd = true
                } label: {
                    Label("Add profile", systemImage: "plus")
                }
                .accessibilityLabel("Add profile")
                .accessibilityIdentifier("adapter.add-profile")
                .buttonStyle(.borderedProminent)
                .tint(sea)
                .disabled(busy || !daemonUp || !listenerReady)
            }
            if !banner.isEmpty {
                HStack(alignment: .top) {
                    Label(banner, systemImage: bannerIsError ? "exclamationmark.triangle.fill" : "checkmark.circle.fill")
                        .frame(maxWidth: .infinity, alignment: .leading)
                    Button { banner = "" } label: { Image(systemName: "xmark") }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Dismiss message")
                }
                    .font(.callout)
                    .foregroundStyle(bannerIsError ? clay : moss)
                    .textSelection(.enabled)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background((bannerIsError ? clay : moss).opacity(0.09), in: RoundedRectangle(cornerRadius: 10))
            }
            if !statusError.isEmpty && ControlClient.launchAgentInstalled() {
                VStack(alignment: .leading, spacing: 6) {
                    Label("The adapter service is unavailable. Start or restart it from Service.", systemImage: "exclamationmark.triangle")
                    DisclosureGroup("Connection details") {
                        Text(statusError).textSelection(.enabled).font(.caption)
                    }
                }
                .font(.callout)
                .foregroundStyle(clay)
            }
        }
        .padding(18)
    }

    private var emptyState: some View {
        VStack(alignment: .leading, spacing: 18) {
            VStack(alignment: .leading, spacing: 8) {
                Image(systemName: daemonUp ? "point.3.connected.trianglepath.dotted" : "bolt.horizontal.circle")
                    .font(.system(size: 32, weight: .medium))
                    .foregroundStyle(sea)
                Text(daemonUp ? "Connect your first MCP server" : "Start your local adapter")
                    .font(.title2.weight(.bold))
                Text(daemonUp
                    ? "Create a profile for a platform server. The adapter handles its session certificate and gives your agent a local MCP URL."
                    : "Install the background service once. It stays available while the app is closed and starts when you log in.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if !daemonUp {
                Button {
                    manage("install")
                } label: {
                    Label(busy ? "Starting adapter…" : "Install and start", systemImage: busy ? "hourglass" : "power")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .tint(sea)
                .controlSize(.large)
                .disabled(busy)
                setupTip("Your profiles and credentials stay on this Mac.", icon: "lock.shield")
            } else {
                VStack(alignment: .leading, spacing: 12) {
                    HStack(alignment: .top, spacing: 10) {
                        setupStep("1", title: "Sign in", detail: "Choose the account with access to the server.")
                        Spacer(minLength: 8)
                        Button(signInCommandCopied ? "Copied" : "Copy command") {
                            copySignInCommand()
                        }
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                        .accessibilityIdentifier("adapter.copy-sign-in-command")
                    }
                    setupStep("2", title: "Add a profile", detail: "Enter the server name and the active Agent ID from your platform's Agents page.")
                    setupStep("3", title: "Connect your agent", detail: "Choose Cursor, Codex, Claude Code, or VS Code. MCP Runtime writes the right config for that client.")
                }
                .padding(16)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color(nsColor: .controlBackgroundColor), in: RoundedRectangle(cornerRadius: 16, style: .continuous))
                Button {
                    addError = ""
                    showAdd = true
                } label: {
                    Label("Add your first profile", systemImage: "plus")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .tint(sea)
                .controlSize(.large)
                .disabled(busy || !listenerReady)
            }
            Spacer(minLength: 0)
        }
        .frame(maxWidth: 620, maxHeight: .infinity, alignment: .topLeading)
        .padding(28)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func setupStep(_ number: String, title: String, detail: String) -> some View {
        HStack(alignment: .top, spacing: 12) {
            Text(number)
                .font(.caption.weight(.bold))
                .foregroundStyle(sea)
                .frame(width: 24, height: 24)
                .background(sea.opacity(0.12), in: Circle())
            VStack(alignment: .leading, spacing: 3) {
                Text(title).font(.callout.weight(.semibold))
                Text(detail).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    private func setupTip(_ title: String, icon: String) -> some View {
        Label(title, systemImage: icon)
            .font(.caption)
            .foregroundStyle(.secondary)
    }

    private var statusPill: some View {
        let ready = daemonUp && listenerReady
        return HStack(spacing: 6) {
            Circle()
                .fill(ready ? moss : amber)
                .frame(width: 8, height: 8)
            Text(!serviceAction.isEmpty ? serviceProgress : (daemonUp ? (listenerReady ? "Ready" : "Starting") : "Stopped"))
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
                Text("Use Connect your agent to install or export the URL with its required profile credential.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            if let human = profile.human, !human.isEmpty {
                Text("Signed-in user: \(human)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
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
                    .buttonStyle(.bordered)
                    .help("The URL also requires the local profile credential. Use Connect your agent for a complete configuration.")
                    .accessibilityIdentifier("adapter.copy-url.\(profile.id)")
                Button("Inspect") { openWindow(id: "inspect", value: profile.id) }
                    .buttonStyle(.bordered)
                    .accessibilityIdentifier("adapter.inspect.\(profile.id)")
                Menu {
                    Button("Install in Cursor") { installClient("Cursor", profile: profile) }
                    Button("Install in Codex") { installClient("Codex", profile: profile) }
                    Button("Install in Claude Code") { installClient("Claude Code", profile: profile) }
                    Button("Install in VS Code") { installClient("VS Code", profile: profile) }
                    Divider()
                    Button("Export config file…") { exportConfig(profile) }
                } label: {
                    Label("Connect your agent", systemImage: "arrow.up.forward.app")
                }
                .menuStyle(.borderedButton)
                .accessibilityIdentifier("adapter.connect-agent.\(profile.id)")
                .disabled(busy || !daemonUp)
                Button("Reconnect") { reconnect(profile.id) }
                    .buttonStyle(.bordered)
                    .accessibilityLabel("Reconnect")
                    .accessibilityIdentifier("adapter.reconnect.\(profile.id)")
                    .disabled(busy || !daemonUp)
                Menu {
                    Button("Remove profile…", role: .destructive) { profileToRemove = profile }
                } label: {
                    Image(systemName: "ellipsis")
                        .frame(minWidth: 24)
                }
                .menuStyle(.borderlessButton)
                .accessibilityLabel("More actions for \(profile.server)")
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
        guard let exp = profile.certExpires, let date = AdapterTimestamp.date(exp) else {
            return "Session certificate is enrolled"
        }
        let minutes = Int(date.timeIntervalSinceNow / 60)
        if date <= Date() {
            return "Session certificate expired \(shortTime(exp))"
        }
        if minutes < 90 {
            return "Session certificate expires in \(minutes) minutes. Renewal cannot extend the authorized session."
        }
        return "Session certificate expires \(shortTime(exp))"
    }

    private func certTone(_ profile: ProfileStatus) -> Color {
        guard profile.authState == "enrolled",
              let exp = profile.certExpires,
              let date = AdapterTimestamp.date(exp) else {
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
            return "Connect your agent to the local URL. Reconnect refreshes this profile’s connection."
        case "sign_in_required":
            return "Sign in with mcp-runtime auth login and verify this agent has a matching access grant, then choose Reconnect."
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
        case "sign_in_required": return "Authorization required"
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
        if let date = AdapterTimestamp.date(raw) {
            return date.formatted(date: .abbreviated, time: .shortened)
        }
        return raw
    }

    private func refresh() {
        guard !refreshing else { return }
        refreshing = true
        DispatchQueue.global(qos: .utility).async {
            do {
                let status = try ControlClient.status()
                DispatchQueue.main.async {
                    refreshing = false
                    daemonUp = true
                    listenerReady = status.listenerReady
                    listenAddr = status.listenAddr
                    checkedAt = status.checkedAt
                    login = status.startupAtLogin
                    profiles = status.profiles
                    statusError = ""
                }
            } catch {
                let message = redacted(error.localizedDescription) ?? "Adapter status is unavailable."
                let installed = ControlClient.launchAgentInstalled()
                DispatchQueue.main.async {
                    refreshing = false
                    daemonUp = false
                    listenerReady = false
                    login = installed
                    statusError = serviceAction.isEmpty ? message : ""
                }
            }
        }
    }

    private func manage(_ action: String) {
        guard !busy else { return }
        busy = true
        serviceAction = action
        statusError = ""
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                try ControlClient.manage(action)
                DispatchQueue.main.async {
                    busy = false
                    serviceAction = ""
                    banner = "Adapter service \(action) completed."
                    bannerIsError = false
                    refresh()
                }
            } catch {
                DispatchQueue.main.async {
                    busy = false
                    serviceAction = ""
                    banner = error.localizedDescription
                    bannerIsError = true
                }
            }
        }
    }

    private var serviceProgress: String {
        switch serviceAction {
        case "install": return "Installing service…"
        case "start": return "Starting service…"
        case "restart": return "Restarting service…"
        case "stop": return "Stopping service…"
        case "uninstall": return "Uninstalling service…"
        default: return "Updating service…"
        }
    }

    private func exportConfig(_ profile: ProfileStatus) {
        guard !busy else { return }
        let name = localSlug(profile.server)
        guard let url = ControlClient.chooseClientConfigURL(suggestedName: "mcp-runtime-\(name.isEmpty ? "server" : name).json") else { return }
        busy = true
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                try ControlClient.saveClientConfig(profileID: profile.id, to: url)
                DispatchQueue.main.async {
                    busy = false
                    banner = "Client config saved with owner-only permissions: \(url.path)"
                    bannerIsError = false
                }
            } catch {
                DispatchQueue.main.async {
                    busy = false
                    banner = error.localizedDescription
                    bannerIsError = true
                }
            }
        }
    }

    private func installClient(_ client: String, profile: ProfileStatus) {
        guard !busy else { return }
        busy = true
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                let message = try ControlClient.installClient(client, profileID: profile.id)
                DispatchQueue.main.async {
                    busy = false
                    banner = message
                    bannerIsError = false
                }
            } catch {
                DispatchQueue.main.async {
                    busy = false
                    banner = error.localizedDescription
                    bannerIsError = true
                }
            }
        }
    }

    private func loadLogs() {
        DispatchQueue.global(qos: .utility).async {
            let result: [String]
            do {
                result = try ControlClient.logs()
            } catch {
                result = [redacted(error.localizedDescription) ?? "Daemon logs are unavailable."]
            }
            DispatchQueue.main.async { logs = result }
        }
    }

    private func reconnect(_ id: String) {
        guard !busy else { return }
        busy = true
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                try ControlClient.restart(id: id)
                DispatchQueue.main.async {
                    busy = false
                    banner = "Profile reconnected."
                    bannerIsError = false
                    refresh()
                }
            } catch {
                let message = redacted(error.localizedDescription) ?? "Profile reconnect failed."
                DispatchQueue.main.async {
                    busy = false
                    banner = message
                    bannerIsError = true
                }
            }
        }
    }

    private func removeProfile(_ profile: ProfileStatus) {
        guard !busy else { return }
        busy = true
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                try ControlClient.removeProfile(id: profile.id)
                DispatchQueue.main.async {
                    busy = false
                    banner = "Removed \(profile.server) and its local credentials. The platform session was not revoked."
                    bannerIsError = false
                    refresh()
                }
            } catch {
                DispatchQueue.main.async {
                    busy = false
                    banner = redacted(error.localizedDescription) ?? "Profile could not be removed."
                    bannerIsError = true
                }
            }
        }
    }

    private var addSheet: some View {
        VStack(spacing: 0) {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    VStack(alignment: .leading, spacing: 5) {
                        Text("Connect an MCP server")
                            .font(.title2.weight(.bold))
                        Text("Use your current mcp-runtime sign-in. Your password is never stored by the adapter.")
                            .font(.callout)
                            .foregroundStyle(.secondary)
                    }
                    VStack(alignment: .leading, spacing: 10) {
                        field("Server name", text: $addServer, focus: .server)
                        Text("Required · for example, cully")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .padding(.top, -6)
                        field("Agent ID", text: $addAgent, focus: .agent)
                        Text("Required · use an active agt_… ID from the platform's Agents page.")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                    if !addServer.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                        VStack(alignment: .leading, spacing: 5) {
                            Label("Local endpoint preview", systemImage: "link")
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(.secondary)
                            Text("http://\(listenAddr)/\(draft.localSlug)/mcp")
                                .font(.callout.monospaced())
                                .textSelection(.enabled)
                                .foregroundStyle(sea)
                            Text("A numeric suffix is added if this local path is already used.")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                        .padding(12)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(sea.opacity(0.08), in: RoundedRectangle(cornerRadius: 10))
                    }
                    DisclosureGroup("Advanced connection settings", isExpanded: $showAdvancedFields) {
                        VStack(alignment: .leading, spacing: 10) {
                            field("Namespace (optional)", text: $addNamespace, focus: .namespace)
                            Text("Defaults to your primary namespace. Set it when the server belongs to another namespace.")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                            field("Platform MCP URL", text: $addRuntime, focus: .endpoint)
                            Toggle("This is the complete MCP endpoint URL", isOn: $addRuntimeIsFullEndpoint)
                                .toggleStyle(.checkbox)
                            Text(addRuntimeIsFullEndpoint
                                ? "The adapter uses this URL exactly, including its path and query."
                                : "The adapter adds /<server>/mcp to this base URL. The platform API comes from your current sign-in.")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                            if !addServer.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                                Text("Platform MCP endpoint: \(draft.endpoint ?? "Enter a valid URL")")
                                    .font(.caption.monospaced())
                                    .textSelection(.enabled)
                                    .foregroundStyle(.secondary)
                            }
                        }
                        .padding(.top, 10)
                    }
                    .font(.callout.weight(.semibold))
                    if !addError.isEmpty {
                        Label(addError, systemImage: "exclamationmark.triangle.fill")
                            .font(.callout)
                            .foregroundStyle(clay)
                            .textSelection(.enabled)
                            .fixedSize(horizontal: false, vertical: true)
                            .padding(12)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(clay.opacity(0.08), in: RoundedRectangle(cornerRadius: 10))
                    }
                }
                .padding(22)
            }
            Divider()
            HStack {
                Button("Cancel") { showAdd = false }
                    .keyboardShortcut(.cancelAction)
                    .accessibilityLabel("Cancel")
                    .accessibilityIdentifier("adapter.add.cancel")
                    .disabled(busy)
                Spacer()
                Button {
                    submitAdd()
                } label: {
                    HStack(spacing: 8) {
                        if busy {
                            ProgressView().controlSize(.small)
                            Text("Connecting…")
                        } else {
                            Label("Connect profile", systemImage: "arrow.right.circle.fill")
                        }
                    }
                }
                .accessibilityLabel("Connect profile")
                .accessibilityIdentifier("adapter.add.submit")
                .buttonStyle(.borderedProminent)
                .keyboardShortcut(.defaultAction)
                .tint(sea)
                .disabled(busy || draft.serverName.isEmpty || draft.agentID.isEmpty || !listenerReady)
            }
            .padding(16)
        }
        .frame(width: 520, height: 620)
        .interactiveDismissDisabled(busy)
        .onAppear { focusedField = .server }
    }

    private func field(_ title: String, text: Binding<String>, focus: FormField) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title).font(.caption.weight(.semibold)).foregroundStyle(.secondary)
            TextField(title, text: text)
                .textFieldStyle(.roundedBorder)
                .focused($focusedField, equals: focus)
                .accessibilityLabel(Text(title))
                .accessibilityIdentifier("adapter.field.\(title.replacingOccurrences(of: " ", with: "-").lowercased())")
        }
    }

    private func submitAdd() {
        guard !busy else { return }
        let submitted = draft
        if let validation = submitted.validationMessage {
            addError = validation
            return
        }
        guard let runtime = submitted.endpoint else { return }
        busy = true
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                try ControlClient.addProfile(server: submitted.serverName, namespace: submitted.namespaceName, agent: submitted.agentID, runtimeURL: runtime)
                DispatchQueue.main.async {
                    busy = false
                    showAdd = false
                    addError = ""
                    addServer = ""
                    addNamespace = ""
                    addAgent = ""
                    addRuntime = "https://mcp.mcpruntime.org"
                    addRuntimeIsFullEndpoint = false
                    banner = "Profile saved. Check its authorization status below; sign-in or Reconnect may be needed."
                    bannerIsError = false
                    refresh()
                }
            } catch {
                let message = redacted(error.localizedDescription) ?? "Profile could not be added. Check the fields and try again."
                DispatchQueue.main.async {
                    busy = false
                    addError = message
                    banner = message
                    bannerIsError = true
                }
            }
        }
    }

    private var draft: ProfileDraft {
        ProfileDraft(server: addServer, agent: addAgent, namespace: addNamespace,
                     runtimeURL: addRuntime, exactEndpoint: addRuntimeIsFullEndpoint)
    }

    private func localSlug(_ server: String) -> String {
        let slug = server.lowercased().map { character -> Character in
            if character.isASCII && (character.isLetter || character.isNumber || character == "-") { return character }
            if character == "_" || character == "." || character == " " { return "-" }
            return "\0"
        }.filter { $0 != "\0" }
        return String(slug).trimmingCharacters(in: CharacterSet(charactersIn: "-"))
    }

    private func copy(_ profile: ProfileStatus) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(profile.localUrl, forType: .string)
        copiedID = profile.id
        DispatchQueue.main.asyncAfter(deadline: .now() + 2) {
            if copiedID == profile.id { copiedID = "" }
        }
    }

    private func copySignInCommand() {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString("mcp-runtime auth login", forType: .string)
        signInCommandCopied = true
        DispatchQueue.main.asyncAfter(deadline: .now() + 2) {
            signInCommandCopied = false
        }
    }

    private func poll() async {
        while !Task.isCancelled {
            try? await Task.sleep(for: .seconds(5))
            refresh()
        }
    }
}
