import SwiftUI

struct InspectView: View {
    let profileID: String
    @State private var profile: ProfileStatus?
    @State private var message = "Loading profile"

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Inspect")
                .font(.title3.weight(.semibold))
            Text("This window shows connection and certificate metadata. Private keys and local credentials stay in the credential store and are not displayed.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if let profile {
                detail("Server", profile.server)
                detail("State", label(profile.authState))
                detail("OAuth", profile.oauthRequired.map { $0 ? "Required in your MCP client" : "Not required by server configuration" } ?? "Unknown; reconnect to refresh server metadata")
                detail("Local URL", profile.localUrl)
                detail("Platform MCP", profile.runtimeUrl)
                detail("Platform API", profile.platformUrl)
                detail("Namespace", profile.namespace)
                detail("Team", profile.team)
                detail("Signed-in user", profile.human)
                detail("Agent", profile.agent)
                detail("Certificate from", profile.certNotBefore)
                detail("Certificate expires", profile.certExpires)
                detail("Session expires", profile.sessionExpires)
                detail("Session identity", profile.certSpiffe)
                detail("Upstream", upstream(profile))
                if let problem = redacted(profile.renewalError ?? profile.lastError) {
                    Text(problem)
                        .font(.caption)
                        .textSelection(.enabled)
                        .foregroundStyle(.red)
                }
            } else {
                Text(message)
                    .foregroundStyle(.secondary)
            }
            Spacer()
        }
        .padding(20)
        .frame(minWidth: 460, minHeight: 380)
        .task(id: profileID) { await load() }
    }

    private func detail(_ title: String, _ value: String?) -> some View {
        Group {
            if let value, !value.isEmpty {
                HStack(alignment: .firstTextBaseline) {
                    Text(title)
                        .frame(width: 140, alignment: .leading)
                        .foregroundStyle(.secondary)
                    Text(value)
                        .textSelection(.enabled)
                }
                .font(.callout)
            }
        }
    }

    private func load() async {
        do {
            let status = try ControlClient.status()
            profile = status.profiles.first { $0.id == profileID }
            if profile == nil {
                message = "That profile is no longer on the daemon."
            }
        } catch {
            profile = nil
            message = error.localizedDescription
        }
    }

    private func label(_ state: String) -> String {
        switch state {
        case "enrolled": return "Enrolled"
        case "sign_in_required": return "Authorization required"
        case "renewal_failed": return "Renewal failed"
        default: return "Not enrolled"
        }
    }

    private func upstream(_ profile: ProfileStatus) -> String {
        if let ok = profile.upstreamReachable {
            return ok ? "Platform host reachable" : "Platform host unreachable"
        }
        return "Not checked yet"
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
}
