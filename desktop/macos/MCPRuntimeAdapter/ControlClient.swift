import Foundation
import Darwin

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
    var agent: String?
    var platformUrl: String?
    var runtimeUrl: String?
    var namespace: String?
    var localUrl: String
    var authState: String
    var certExpires: String?
    var certNotBefore: String?
    var certSpiffe: String?
    var renewalError: String?
    var lastError: String?
    var upstreamReachable: Bool?
    var upstreamCheckedAt: String?
}

enum ControlClient {
    static func socketPath() -> String {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        return home + "/.mcpruntime/run/adapterd.sock"
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
        _ = try request(method: "POST", path: "/v1/profiles/\(id)/restart", body: nil)
    }

    static func addProfile(server: String, namespace: String, team: String, agent: String, platformURL: String, runtimeURL: String) throws {
        let payload: [String: String] = [
            "server": server,
            "namespace": namespace,
            "team": team,
            "agent": agent,
            "platformUrl": platformURL,
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
