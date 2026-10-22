import Foundation

@main
enum ControlClientTests {
    static func main() throws {
        for requirement in ["true", "false", "null"] {
            let bytes = Data("{\"id\":\"test\",\"server\":\"demo\",\"localUrl\":\"http://127.0.0.1:8099/demo/mcp\",\"authState\":\"enrolled\",\"oauthRequired\":\(requirement)}".utf8)
            let profile = try JSONDecoder().decode(ProfileStatus.self, from: bytes)
            expect(profile.oauthRequired == (requirement == "null" ? nil : requirement == "true"), "decode OAuth requirement \(requirement) without conflating it with certificate enrollment")
        }
        let home = FileManager.default.temporaryDirectory.appendingPathComponent("adapter-client-tests-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: home, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        defer { try? FileManager.default.removeItem(at: home) }

        let app = home.appendingPathComponent("MCPRuntimeAdapter.app")
        expect(ControlClient.bundledCLI(in: app) == nil, "missing helper reported")
        let helper = app.appendingPathComponent("Contents/Helpers/mcp-runtime")
        try write("#!/bin/sh\nexit 0\n", to: helper)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: helper.path)
        expect(ControlClient.bundledCLI(in: app) == helper, "find packaged helper in Contents/Helpers")

        let config = (name: "test-mcpsnoop", url: "http://127.0.0.1:8099/test-mcpsnoop/mcp", headers: ["X-MCP-Runtime-Local-Token": "test-private-local-token"])
        for (client, relative, rootKey) in [("Cursor", ".cursor/mcp.json", "mcpServers"), ("VS Code", "Library/Application Support/Code/User/mcp.json", "servers")] {
            let url = home.appendingPathComponent(relative)
            try write("{\"\(rootKey)\":{\"existing\":{\"url\":\"http://localhost:9000/mcp\"}},\"other\":true}", to: url)
            _ = try ControlClient.installClient(client, config: config, home: home)
            let bytes = try Data(contentsOf: url)
            let root = try JSONSerialization.jsonObject(with: bytes) as! [String: Any]
            let servers = root[rootKey] as! [String: Any]
            expect(servers["existing"] != nil && root["other"] as? Bool == true, "preserve existing \(client) config")
            let added = servers["mcp-runtime-test-mcpsnoop"] as! [String: Any]
            expect(added["url"] as? String == config.url, "install \(client) endpoint")
            expect((added["headers"] as? [String: String]) == config.headers, "install \(client) required local credential")
            if client == "VS Code" { expect(added["type"] as? String == "http", "VS Code transport format") }
            let permissions = try FileManager.default.attributesOfItem(atPath: url.path)[.posixPermissions] as! NSNumber
            expect(permissions.intValue == 0o600, "private \(client) config permissions")
            do {
                _ = try ControlClient.installClient(client, config: config, home: home)
                fatalError("duplicate \(client) config replaced")
            } catch {
                try expect(try Data(contentsOf: url) == bytes, "duplicate \(client) entry leaves config unchanged")
            }
        }

        let codex = home.appendingPathComponent(".codex/config.toml")
        try write("model = \"existing-model\"\n", to: codex)
        _ = try ControlClient.installClient("Codex", config: config, home: home)
        let toml = try String(contentsOf: codex, encoding: .utf8)
        expect(toml.hasPrefix("model = \"existing-model\""), "preserve Codex settings")
        expect(toml.contains("[mcp_servers.mcp-runtime-test-mcpsnoop.http_headers]"), "Codex header table")
        let parser = Process()
        parser.executableURL = URL(fileURLWithPath: "/usr/bin/env")
        parser.arguments = ["python3", "-c", "import sys,tomllib; d=tomllib.load(open(sys.argv[1],'rb')); assert d['mcp_servers']['mcp-runtime-test-mcpsnoop']['url'] == sys.argv[2]", codex.path, config.url]
        try parser.run()
        parser.waitUntilExit()
        expect(parser.terminationStatus == 0, "generated Codex TOML parses with the correct HTTP URL")
        do {
            _ = try ControlClient.installClient("Codex", config: config, home: home)
            fatalError("duplicate Codex config replaced")
        } catch { try expect(try String(contentsOf: codex, encoding: .utf8) == toml, "duplicate Codex entry leaves config unchanged") }

        for existing in ["[mcp_servers.\"mcp-runtime-test-mcpsnoop\"]\nurl=\"http://localhost/old\"\n", "mcp_servers = {}\n"] {
            try write(existing, to: codex)
            do {
                _ = try ControlClient.installClient("Codex", config: config, home: home)
                fatalError("incompatible Codex config extended")
            } catch { try expect(try String(contentsOf: codex, encoding: .utf8) == existing, "quoted duplicate and inline Codex tables preserved") }
        }

        let malformed = home.appendingPathComponent(".cursor/mcp.json")
        try write("{not-json", to: malformed)
        do {
            _ = try ControlClient.installClient("Cursor", config: config, home: home)
            fatalError("malformed Cursor config overwritten")
        } catch { try expect(try String(contentsOf: malformed, encoding: .utf8) == "{not-json", "malformed config preserved") }

        let claude = home.appendingPathComponent("mock-claude")
        try write("#!/bin/sh\numask 077\nprintf '%s\\n' \"$@\" > \"$HOME/claude-arguments\"\n", to: claude)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: claude.path)
        _ = try ControlClient.installClient("Claude Code", config: config, home: home, claudeExecutable: claude)
        let arguments = try String(contentsOf: home.appendingPathComponent("claude-arguments"), encoding: .utf8)
        expect(arguments.contains("--scope\nuser\n") && arguments.contains("--transport\nhttp\n"), "Claude CLI transport and scope")
        expect(arguments.contains("X-MCP-Runtime-Local-Token: test-private-local-token"), "Claude CLI local profile header")
        try write("#!/bin/sh\nprintf '%s' 'test-private-local-token' >&2\nexit 1\n", to: claude)
        do {
            _ = try ControlClient.installClient("Claude Code", config: config, home: home, claudeExecutable: claude)
            fatalError("Claude CLI failure was ignored")
        } catch {
            expect(!error.localizedDescription.contains("test-private-local-token"), "redact profile token from CLI errors")
        }
        print("Bundled helper and four client configuration checks passed")
    }

    private static func write(_ text: String, to url: URL) throws {
        try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        try text.write(to: url, atomically: true, encoding: .utf8)
    }

    private static func expect(_ condition: @autoclosure () throws -> Bool, _ message: String) rethrows {
        if try !condition() { fatalError(message) }
    }
}
