import Foundation

struct ProfileDraft {
    var server: String
    var agent: String
    var namespace: String
    var runtimeURL: String
    var exactEndpoint: Bool

    var serverName: String { server.trimmingCharacters(in: .whitespacesAndNewlines) }
    var agentID: String { agent.trimmingCharacters(in: .whitespacesAndNewlines) }
    var namespaceName: String { namespace.trimmingCharacters(in: .whitespacesAndNewlines) }

    var localSlug: String {
        let characters = serverName.lowercased().compactMap { character -> Character? in
            if character.isASCII && (character.isLetter || character.isNumber || character == "-") { return character }
            if character == "_" || character == "." || character == " " { return "-" }
            return nil
        }
        return String(characters).trimmingCharacters(in: CharacterSet(charactersIn: "-"))
    }

    var endpoint: String? {
        let raw = runtimeURL.trimmingCharacters(in: .whitespacesAndNewlines)
        guard var components = URLComponents(string: raw),
              let scheme = components.scheme?.lowercased(), ["http", "https"].contains(scheme),
              let host = components.host, !host.isEmpty,
              components.user == nil, components.password == nil, components.fragment == nil else { return nil }
        if !exactEndpoint {
            components.path = components.path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
            components.path = "/" + (components.path.isEmpty ? "" : components.path + "/") + serverName + "/mcp"
        }
        return components.url?.absoluteString
    }

    var validationMessage: String? {
        if serverName.isEmpty { return "Enter the platform server name." }
        if localSlug.isEmpty || localSlug == "mcp" { return "Choose a server name that can be used in a local MCP path." }
        if serverName.contains(where: { "/?#".contains($0) }) { return "Enter the server name separately from its MCP URL." }
        if agentID.isEmpty { return "Enter the active Agent ID from your platform's Agents page." }
        if endpoint == nil { return "Enter an HTTP or HTTPS MCP URL with a host and no embedded credentials or fragment." }
        return nil
    }
}

enum AdapterTimestamp {
    static func date(_ raw: String) -> Date? {
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return fractional.date(from: raw) ?? ISO8601DateFormatter().date(from: raw)
    }
}
