import Foundation
import AppKit
import Darwin
import UniformTypeIdentifiers

enum ControlClientError: Error, LocalizedError {
    case socket(String)
    case http(String)

    var errorDescription: String? {
        switch self {
        case .socket(let message), .http(let message):
            return message
        }
    }
}

struct DaemonStatus: Decodable {
    var protocolVersion: Int?
    var processRunning: Bool
    var listenAddr: String
    var listenerReady: Bool
    var checkedAt: String
    var startupAtLogin: Bool
    var profiles: [ProfileStatus]

    enum CodingKeys: String, CodingKey {
        case protocolVersion = "protocol"
        case processRunning, listenAddr, listenerReady, checkedAt, startupAtLogin, profiles
    }
}

struct ProfileStatus: Decodable, Identifiable {
    var id: String
    var server: String
    var team: String?
    var human: String?
    var agent: String?
    var platformUrl: String?
    var runtimeUrl: String?
    var namespace: String?
    var localUrl: String
    var authState: String
    var certExpires: String?
    var sessionExpires: String?
    var certNotBefore: String?
    var certSpiffe: String?
    var renewalError: String?
    var lastError: String?
    var upstreamReachable: Bool?
    var upstreamCheckedAt: String?
}

enum ControlClient {
    static func configDirectory() -> String {
        // Finder and Dock launch the companion independently of the user's
        // shell. The installed service plist is the source of truth because
        // it is where the daemon got its environment. Prefer it even if a
        // stale shell/launcher variable points at an older test directory.
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        let launchAgent = URL(fileURLWithPath: home)
            .appendingPathComponent("Library/LaunchAgents/org.mcpruntime.adapter.plist")
        if FileManager.default.fileExists(atPath: launchAgent.path) {
            if let data = try? Data(contentsOf: launchAgent),
           let plist = try? PropertyListSerialization.propertyList(from: data, format: nil),
           let root = plist as? [String: Any] {
                if let environment = root["EnvironmentVariables"] as? [String: Any],
                   let configured = environment["MCP_RUNTIME_CONFIG_DIR"] as? String,
                   !configured.isEmpty {
                    return (configured as NSString).expandingTildeInPath
                }
            }
            // An installed service with no override uses the CLI's default,
            // even when the Dock app was launched by a stale shell.
            return home + "/.mcpruntime"
        }
        if let configured = ProcessInfo.processInfo.environment["MCP_RUNTIME_CONFIG_DIR"], !configured.isEmpty {
            return (configured as NSString).expandingTildeInPath
        }
        return home + "/.mcpruntime"
    }

    static func socketPath() -> String {
        return configDirectory() + "/run/adapterd.sock"
    }

    static func launchAgentInstalled() -> Bool {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        return FileManager.default.fileExists(atPath: home + "/Library/LaunchAgents/org.mcpruntime.adapter.plist")
    }

    static func chooseClientConfigURL(suggestedName: String) -> URL? {
        let panel = NSSavePanel()
        panel.allowedContentTypes = [.json]
        panel.canCreateDirectories = true
        panel.prompt = "Export"
        panel.message = "Choose a new file name. Existing client configs are never overwritten."
        panel.nameFieldStringValue = suggestedName
        guard panel.runModal() == .OK else { return nil }
        return panel.url
    }

    static func saveClientConfig(profileID: String, to url: URL) throws {
        let config = try clientConfig(id: profileID)
        let fd = open(url.path, O_WRONLY | O_CREAT | O_EXCL, S_IRUSR | S_IWUSR)
        guard fd >= 0 else {
            if errno == EEXIST {
                throw ControlClientError.http("That file already exists. Choose a new name to keep the existing config safe.")
            }
            throw ControlClientError.http("Could not create the config file securely: \(String(cString: strerror(errno)))")
        }
        defer { close(fd) }
        var offset = 0
        try config.withUnsafeBytes { raw in
            guard let base = raw.baseAddress else { return }
            while offset < raw.count {
                let written = Darwin.write(fd, base.advanced(by: offset), raw.count - offset)
                if written <= 0 {
                    throw ControlClientError.http("Could not write the client config securely: \(String(cString: strerror(errno)))")
                }
                offset += written
            }
        }
        if fsync(fd) != 0 {
            throw ControlClientError.http("Could not save the client config securely: \(String(cString: strerror(errno)))")
        }
    }

    static func status() throws -> DaemonStatus {
        let body = try request(method: "GET", path: "/v1/status", body: nil)
        do {
            return try JSONDecoder().decode(DaemonStatus.self, from: body)
        } catch {
            throw ControlClientError.http("status could not be read: \(error.localizedDescription)")
        }
    }

    static func logs() throws -> [String] {
        let body = try request(method: "GET", path: "/v1/logs", body: nil)
        struct Payload: Decodable { var lines: [String] }
        return try JSONDecoder().decode(Payload.self, from: body).lines
    }

    static func restart(id: String) throws {
        let body = try request(method: "POST", path: "/v1/profiles/\(id)/restart", body: nil)
        let status = try JSONDecoder().decode(DaemonStatus.self, from: body)
        guard let profile = status.profiles.first(where: { $0.id == id }), profile.authState == "enrolled" else {
            let profile = status.profiles.first(where: { $0.id == id })
            throw ControlClientError.http(profile?.renewalError ?? profile?.lastError ?? "Authorization is required. Sign in and verify the matching access grant, then reconnect.")
        }
    }

    static func removeProfile(id: String) throws {
        _ = try request(method: "DELETE", path: "/v1/profiles/\(id)?purge=true", body: nil)
    }

    static func manage(_ action: String) throws {
        let allowed = ["install", "start", "stop", "restart", "uninstall"]
        guard allowed.contains(action),
              let executable = Bundle.main.url(forResource: "mcp-runtime", withExtension: nil, subdirectory: "Helpers") else {
            throw ControlClientError.socket("The bundled adapter CLI is unavailable. Reinstall the companion app.")
        }
        let process = Process()
        process.executableURL = executable
        process.arguments = ["adapter", "service", action]
        // Finder-launched apps can inherit an old development override. The
        // installed LaunchAgent owns the daemon's state directory; never let a
        // stale MCP_RUNTIME_CONFIG_DIR rewrite it during Install and start.
        var environment = ProcessInfo.processInfo.environment
        environment.removeValue(forKey: "MCP_RUNTIME_CONFIG_DIR")
        environment.removeValue(forKey: "MCP_RUNTIME_ADAPTER_FILE_SECRETS")
        if action == "install", launchAgentInstalled() {
            let directory = configDirectory()
            if directory != FileManager.default.homeDirectoryForCurrentUser.path + "/.mcpruntime" {
                environment["MCP_RUNTIME_CONFIG_DIR"] = directory
            }
            let plist = URL(fileURLWithPath: FileManager.default.homeDirectoryForCurrentUser.path)
                .appendingPathComponent("Library/LaunchAgents/org.mcpruntime.adapter.plist")
            if let data = try? Data(contentsOf: plist),
               let root = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any],
               let values = root["EnvironmentVariables"] as? [String: String],
               let fileSecrets = values["MCP_RUNTIME_ADAPTER_FILE_SECRETS"], !fileSecrets.isEmpty {
                environment["MCP_RUNTIME_ADAPTER_FILE_SECRETS"] = fileSecrets
            }
        }
        process.environment = environment
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe
        try process.run()
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        guard process.terminationStatus == 0 else {
            let detail = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            throw ControlClientError.http(detail.isEmpty ? "adapter service \(action) failed" : detail)
        }
    }

    static func clientConfig(id: String) throws -> Data {
        return try request(method: "GET", path: "/v1/profiles/\(id)/client-config", body: nil)
    }

    static func localServerConfig(id: String) throws -> (name: String, url: String, headers: [String: String]) {
        let data = try clientConfig(id: id)
        guard let root = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              let servers = root["mcpServers"] as? [String: Any],
              let name = servers.keys.sorted().first,
              let server = servers[name] as? [String: Any],
              let url = server["url"] as? String else {
            throw ControlClientError.http("The adapter returned an invalid MCP client config.")
        }
        return (name, url, server["headers"] as? [String: String] ?? [:])
    }

    static func installClient(_ client: String, profileID: String) throws -> String {
        let config = try localServerConfig(id: profileID)
        let home = FileManager.default.homeDirectoryForCurrentUser
        let slug = config.name.lowercased().map { character -> Character in
            character.isASCII && (character.isLetter || character.isNumber || character == "-") ? character : "-"
        }
        let name = "mcp-runtime-" + String(slug).trimmingCharacters(in: CharacterSet(charactersIn: "-"))
        let entry: [String: Any] = ["url": config.url, "headers": config.headers]

        switch client {
        case "Cursor", "VS Code":
            let relative = client == "Cursor"
                ? ".cursor/mcp.json"
                : "Library/Application Support/Code/User/mcp.json"
            let url = home.appendingPathComponent(relative)
            var root = try readJSONConfig(url)
            let key = client == "Cursor" ? "mcpServers" : "servers"
            if root[key] != nil && !(root[key] is [String: Any]) {
                throw ControlClientError.http("The \(key) field in \(url.path) is not an object. The file was left unchanged.")
            }
            var servers = root[key] as? [String: Any] ?? [:]
            guard servers[name] == nil else {
                throw ControlClientError.http("\(name) is already configured in \(url.path). Remove that entry first if you want to replace it.")
            }
            var clientEntry = entry
            if client == "VS Code" { clientEntry["type"] = "http" }
            servers[name] = clientEntry
            root[key] = servers
            try writeJSONConfig(root, to: url)
            return "Added \(name) to \(url.path). Restart or reload \(client) to connect."
        case "Codex":
            let url = home.appendingPathComponent(".codex/config.toml")
            let current = try readTextConfig(url)
            let section = "[mcp_servers.\(name)]"
            guard !current.components(separatedBy: .newlines).contains(where: { $0.trimmingCharacters(in: .whitespaces) == section }) else {
                throw ControlClientError.http("\(name) is already configured in \(url.path). Remove that entry first if you want to replace it.")
            }
            var lines = [section, "url = \(tomlString(config.url))"]
            if !config.headers.isEmpty {
                lines.append("[mcp_servers.\(name).http_headers]")
                for (key, value) in config.headers.sorted(by: { $0.key < $1.key }) {
                    lines.append("\(tomlString(key)) = \(tomlString(value))")
                }
            }
            try writeTextConfig(current + (current.isEmpty || current.hasSuffix("\n\n") ? "" : "\n") + lines.joined(separator: "\n") + "\n", to: url)
            return "Added \(name) to \(url.path). Restart Codex to connect."
        case "Claude Code":
            guard let executable = findExecutable("claude", extraPaths: ["/opt/homebrew/bin/claude", "/usr/local/bin/claude", home.appendingPathComponent(".local/bin/claude").path]) else {
                throw ControlClientError.http("Claude Code CLI was not found. Install it, then try again.")
            }
            guard let token = config.headers["X-MCP-Runtime-Local-Token"] else {
                throw ControlClientError.http("The adapter did not provide its local profile token.")
            }
            let process = Process()
            process.executableURL = URL(fileURLWithPath: executable)
            process.arguments = ["mcp", "add", "--transport", "http", "--scope", "user", "--header", "X-MCP-Runtime-Local-Token: \(token)", name, config.url]
            process.environment = ["PATH": "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"]
            let output = Pipe()
            process.standardOutput = output
            process.standardError = output
            try process.run()
            let result = output.fileHandleForReading.readDataToEndOfFile()
            process.waitUntilExit()
            guard process.terminationStatus == 0 else {
                let detail = String(data: result, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
                throw ControlClientError.http(detail.isEmpty ? "Claude Code could not add the MCP server." : detail)
            }
            return "Added \(name) to Claude Code for your user account. Run /mcp or restart Claude Code to connect."
        default:
            throw ControlClientError.http("Choose Cursor, Codex, Claude Code, or VS Code.")
        }
    }

    private static func readJSONConfig(_ url: URL) throws -> [String: Any] {
        guard FileManager.default.fileExists(atPath: url.path) else { return [:] }
        let data = try Data(contentsOf: url)
        guard let root = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            throw ControlClientError.http("Could not read the existing JSON config at \(url.path). It was left unchanged.")
        }
        return root
    }

    private static func readTextConfig(_ url: URL) throws -> String {
        guard FileManager.default.fileExists(atPath: url.path) else { return "" }
        do { return try String(contentsOf: url, encoding: .utf8) }
        catch { throw ControlClientError.http("Could not read the existing config at \(url.path). It was left unchanged.") }
    }

    private static func writeJSONConfig(_ value: [String: Any], to url: URL) throws {
        let data = try JSONSerialization.data(withJSONObject: value, options: [.prettyPrinted, .sortedKeys])
        try writeConfigData(data, to: url)
    }

    private static func writeTextConfig(_ value: String, to url: URL) throws {
        try writeConfigData(Data(value.utf8), to: url)
    }

    private static func writeConfigData(_ data: Data, to url: URL) throws {
        try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let temporary = url.deletingLastPathComponent().appendingPathComponent(".\(url.lastPathComponent).\(UUID().uuidString).tmp")
        let fd = open(temporary.path, O_WRONLY | O_CREAT | O_EXCL, S_IRUSR | S_IWUSR)
        guard fd >= 0 else {
            throw ControlClientError.http("Could not create a private config file: \(String(cString: strerror(errno)))")
        }
        var succeeded = false
        defer {
            close(fd)
            if !succeeded { unlink(temporary.path) }
        }
        var offset = 0
        try data.withUnsafeBytes { raw in
            guard let base = raw.baseAddress else { return }
            while offset < raw.count {
                let written = Darwin.write(fd, base.advanced(by: offset), raw.count - offset)
                if written <= 0 {
                    throw ControlClientError.http("Could not write the private config file: \(String(cString: strerror(errno)))")
                }
                offset += written
            }
        }
        guard fsync(fd) == 0 else {
            throw ControlClientError.http("Could not save the private config file: \(String(cString: strerror(errno)))")
        }
        guard rename(temporary.path, url.path) == 0 else {
            throw ControlClientError.http("Could not install the config at \(url.path): \(String(cString: strerror(errno)))")
        }
        succeeded = true
    }

    private static func tomlString(_ value: String) -> String {
        let data = try! JSONSerialization.data(withJSONObject: [value])
        let encoded = String(data: data, encoding: .utf8)!
        return String(encoded.dropFirst().dropLast())
    }

    private static func findExecutable(_ name: String, extraPaths: [String]) -> String? {
        let pathCandidates = (ProcessInfo.processInfo.environment["PATH"] ?? "")
            .split(separator: ":")
            .map { String($0) + "/\(name)" }
        let candidates = extraPaths + pathCandidates + ["/opt/homebrew/bin/\(name)", "/usr/local/bin/\(name)", "/usr/bin/\(name)"]
        return candidates.first { FileManager.default.isExecutableFile(atPath: $0) }
    }

    static func addProfile(server: String, namespace: String, team: String, agent: String, runtimeURL: String) throws {
        let payload: [String: String] = [
            "server": server,
            "namespace": namespace,
            "team": team,
            "agent": agent,
            "runtimeUrl": runtimeURL,
        ]
        let body = try JSONSerialization.data(withJSONObject: payload)
        _ = try request(method: "POST", path: "/v1/profiles", body: body)
    }

    static func request(method: String, path: String, body: Data?) throws -> Data {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        if fd < 0 {
            throw ControlClientError.socket("could not open socket")
        }
        defer { close(fd) }
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let sock = socketPath()
        let bytes = Array(sock.utf8)
        guard bytes.count < 104 else {
            throw ControlClientError.socket("socket path is too long")
        }
        withUnsafeMutableBytes(of: &addr.sun_path) { raw in
            raw.copyBytes(from: bytes)
            raw[bytes.count] = 0
        }
        addr.sun_len = UInt8(MemoryLayout<UInt8>.size + MemoryLayout<sa_family_t>.size + bytes.count + 1)
        let length = socklen_t(addr.sun_len)
        let connected = withUnsafePointer(to: &addr) { ptr in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sock in
                Darwin.connect(fd, sock, length)
            }
        }
        if connected != 0 {
            let detail = String(cString: strerror(errno))
            throw ControlClientError.socket("could not connect to \(sock): \(detail)")
        }
        var text = "\(method) \(path) HTTP/1.1\r\nHost: adapter\r\nConnection: close\r\n"
        if let body {
            text += "Content-Type: application/json\r\nContent-Length: \(body.count)\r\n"
        }
        text += "\r\n"
        let handle = FileHandle(fileDescriptor: fd, closeOnDealloc: false)
        try handle.write(contentsOf: Data(text.utf8))
        if let body {
            try handle.write(contentsOf: body)
        }
        let data = try handle.readToEnd() ?? Data()
        guard let raw = String(data: data, encoding: .utf8), let range = raw.range(of: "\r\n\r\n") else {
            throw ControlClientError.http("empty management response (\(data.count) bytes)")
        }
        let header = String(raw[..<range.lowerBound])
        if !header.contains(" 200 ") && !header.contains(" 201 ") && !header.contains(" 204 ") {
            throw ControlClientError.http(header)
        }
        return Data(raw[range.upperBound...].utf8)
    }
}
