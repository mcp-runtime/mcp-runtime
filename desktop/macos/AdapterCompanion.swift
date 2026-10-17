import AppKit
import Foundation
import SwiftUI

// The bundled CLI is the versioned client of the same protected local API used
// by terminal users. The app never reads profiles.json or handles credentials.
struct ProfileState: Decodable, Identifiable {
    let name, platformUrl, namespace, server, agent, human, team, endpoint: String
    let localReady: Bool
    let upstream, authentication, certificateExpires, sessionExpires, lastCheck: String
    let lastRenewalError: String?
    var id: String { name }
}

struct DiagnosticEvent: Decodable {
    let at, profile, message: String
}

struct DaemonState: Decodable {
    let version: Int
    let running, startAtLogin: Bool
    let profiles: [ProfileState]?
    let events: [DiagnosticEvent]?
}

enum CompanionError: LocalizedError {
    case unavailable, commandFailed, invalidProtocol, invalidStatus, tooMuchOutput
    var errorDescription: String? {
        switch self {
        case .unavailable: return "The bundled adapter CLI is unavailable. Reinstall the companion app."
        case .commandFailed: return "The operation failed. Inspect service logs or run the same adapter service command in your terminal."
        case .invalidProtocol: return "The app and daemon use different protocol versions. Update the app and reinstall the service."
        case .invalidStatus: return "The daemon returned an unreadable status. Inspect it with adapter service status."
        case .tooMuchOutput: return "The diagnostic response exceeded its size limit. Inspect service status in your terminal."
        }
    }
}

@MainActor final class CompanionModel: ObservableObject {
    @Published var state: DaemonState?
    @Published var error: String?
    @Published var busy = false

    nonisolated private static func command(_ action: String) throws -> Data {
        let allowed = ["status", "install", "uninstall", "start", "stop", "restart", "reconnect"]
        guard allowed.contains(action),
              let executable = Bundle.main.url(forAuxiliaryExecutable: "mcp-runtime") else {
            throw CompanionError.unavailable
        }
        let process = Process()
        process.executableURL = executable
        process.arguments = ["adapter", "service", action]
        // Credentials never occur in arguments or output. Discard stderr rather
        // than showing arbitrary upstream error bodies in the desktop window.
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = FileHandle.nullDevice
        try process.run()
        let timer = DispatchSource.makeTimerSource(queue: .global(qos: .utility))
        timer.schedule(deadline: .now() + 35)
        timer.setEventHandler { if process.isRunning { process.terminate() } }
        timer.resume()
        defer { timer.cancel() }
        var data = Data()
        while true {
            let chunk = pipe.fileHandleForReading.readData(ofLength: 8192)
            if chunk.isEmpty { break }
            if data.count + chunk.count > 1_048_576 {
                process.terminate()
                throw CompanionError.tooMuchOutput
            }
            data.append(chunk)
        }
        process.waitUntilExit()
        guard process.terminationStatus == 0 else { throw CompanionError.commandFailed }
        return data
    }

    private func readStatus() async throws {
        let data = try await Task.detached(priority: .utility) { try Self.command("status") }.value
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        guard let status = try? decoder.decode(DaemonState.self, from: data) else {
            throw CompanionError.invalidStatus
        }
        guard status.version == 1 else { throw CompanionError.invalidProtocol }
        state = status
    }

    func refresh() async {
        guard !busy else { return }
        busy = true
        defer { busy = false }
        do { try await readStatus(); error = nil }
        catch { state = nil; self.error = error.localizedDescription }
    }

    func perform(_ action: String) async {
        guard !busy else { return }
        busy = true
        defer { busy = false }
        do {
            _ = try await Task.detached(priority: .utility) { try Self.command(action) }.value
            try await readStatus()
            error = nil
        } catch { state = nil; self.error = error.localizedDescription }
    }
}

@MainActor struct CompanionWindow: View {
    @StateObject private var model = CompanionModel()
    @State private var showLogs = false
    @State private var confirmUninstall = false

    private func action(_ title: String, _ command: String) -> some View {
        Button(title) { Task { await model.perform(command) } }.disabled(model.busy)
    }

    private func displayDate(_ value: String) -> String {
        if value.hasPrefix("0001-") || value.isEmpty { return "Not measured" }
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let fractional = formatter.date(from: value)
        formatter.formatOptions = [.withInternetDateTime]
        guard let date = fractional ?? formatter.date(from: value) else { return "Unknown" }
        return date.formatted(date: .abbreviated, time: .standard)
    }

    private func profile(_ item: ProfileState) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text(item.name).font(.headline)
                Spacer()
                Text(item.localReady ? "Listener ready" : "Listener unavailable")
                    .foregroundColor(item.localReady ? .green : .secondary)
            }
            Text(item.platformUrl).textSelection(.enabled)
            Text("Team: \(item.team) · Agent: \(item.agent)")
            Text("User: \(item.human)")
            Text("Server: \(item.namespace)/\(item.server)")
            Text("Upstream: \(item.upstream) · Authentication: \(item.authentication)")
            Text("Last check: \(displayDate(item.lastCheck))")
            Text("Certificate expires: \(displayDate(item.certificateExpires))")
            Text("Session expires: \(displayDate(item.sessionExpires))")
            if item.authentication == "sign_in_required" {
                Text("Sign-in needed. Run auth login and adapter profile authorize for this profile in your terminal.")
                    .foregroundColor(.orange)
            }
            if let failure = item.lastRenewalError, !failure.isEmpty {
                Text(failure).foregroundColor(.orange)
                Text("Check network access and your platform login. Reauthorize this profile in the CLI when interaction is required.")
            }
            HStack {
                Text(item.endpoint).textSelection(.enabled)
                Spacer()
                Button("Copy endpoint") {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(item.endpoint, forType: .string)
                }
            }
            Text("Clients also need this profile’s local credential. Generate client configuration with adapter profile client-config.")
                .font(.caption).foregroundColor(.secondary)
        }
        .font(.callout)
        .padding()
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(nsColor: .controlBackgroundColor), in: RoundedRectangle(cornerRadius: 8))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Image(systemName: "network").font(.title)
                VStack(alignment: .leading) {
                    Text("MCP Runtime Adapter").font(.title2)
                    Text(model.state.map { $0.running ? "Daemon running" : "Daemon stopped" } ?? (model.error == nil ? "Checking daemon…" : "Daemon status unavailable"))
                }
                Spacer()
                if model.busy { ProgressView().controlSize(.small) }
                Button("Refresh") { Task { await model.refresh() } }.disabled(model.busy)
            }
            Text(model.state.map { $0.startAtLogin ? "Startup at login: enabled" : "Startup at login: disabled" } ?? "Startup at login: unknown")
            HStack {
                if model.state?.startAtLogin != true { action("Install and enable login startup", "install") }
                action("Start", "start")
                action("Stop", "stop")
                action("Restart", "restart")
                action("Reconnect", "reconnect")
            }
            if let error = model.error { Text(error).foregroundColor(.red).accessibilityLabel("Adapter error: \(error)") }
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 12) {
                    ForEach(model.state?.profiles ?? []) { item in profile(item) }
                    if model.state?.profiles?.isEmpty != false {
                        Text("No configured profiles. Sign in and add a profile with the MCP Runtime CLI, then reconnect.")
                            .padding().frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
            }
            Text("Local readiness does not prove upstream authorization. Closing or quitting this app leaves the daemon running.")
                .font(.caption).foregroundColor(.secondary)
            HStack {
                Button("Open logs") { showLogs = true }
                Spacer()
                Button("Uninstall service…", role: .destructive) { confirmUninstall = true }.disabled(model.busy)
            }
        }
        .padding(18)
        .frame(minWidth: 640, idealWidth: 680, minHeight: 500, idealHeight: 680)
        .confirmationDialog("Stop the daemon and remove startup at login? Profiles and credentials will be preserved.", isPresented: $confirmUninstall) {
            Button("Uninstall service", role: .destructive) { Task { await model.perform("uninstall") } }
        }
        .sheet(isPresented: $showLogs) {
            VStack(alignment: .leading, spacing: 12) {
                Text("Recent adapter diagnostics").font(.headline)
                ScrollView {
                    Text((model.state?.events ?? []).map { "\(displayDate($0.at)) · \($0.profile) · \($0.message)" }.joined(separator: "\n"))
                        .font(.system(.body, design: .monospaced)).textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                Button("Done") { showLogs = false }.keyboardShortcut(.defaultAction)
            }.padding().frame(minWidth: 600, minHeight: 320)
        }
        .task {
            while !Task.isCancelled {
                await model.refresh()
                try? await Task.sleep(nanoseconds: 10_000_000_000)
            }
        }
    }
}

@main @MainActor struct AdapterCompanionApp: App {
    var body: some Scene {
        Window("MCP Runtime Adapter", id: "adapter") { CompanionWindow() }
            .commands { CommandGroup(replacing: .newItem) {} }
    }
}
