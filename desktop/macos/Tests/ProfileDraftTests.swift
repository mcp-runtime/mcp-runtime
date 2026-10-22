import Foundation

@main
enum ProfileDraftTests {
    static func main() {
        var draft = ProfileDraft(server: " cully ", agent: " agt_test ", namespace: " team-a ", runtimeURL: "https://mcp.example.com", exactEndpoint: false)
        expect(draft.validationMessage == nil, "valid required fields")
        expect(draft.serverName == "cully" && draft.agentID == "agt_test" && draft.namespaceName == "team-a", "trim form input")
        expect(draft.endpoint == "https://mcp.example.com/cully/mcp", "default endpoint")

        draft.runtimeURL = "https://example.com/prefix/?tenant=dev%2Fqa"
        expect(draft.endpoint == "https://example.com/prefix/cully/mcp?tenant=dev%2Fqa", "base URL query stays after appended path")
        draft.exactEndpoint = true
        expect(draft.endpoint == draft.runtimeURL, "exact endpoint preserves custom path and encoded query")

        for invalid in ["ftp://example.com/mcp", "https:///mcp", "https://user:secret@example.com/mcp", "https://example.com/mcp#fragment", ""] {
            draft.runtimeURL = invalid
            expect(draft.validationMessage != nil, "reject invalid MCP endpoint")
        }
        draft.runtimeURL = "http://localhost:8088/custom/mcp"
        expect(draft.validationMessage == nil, "local HTTP endpoint")
        draft.agent = " "
        expect(draft.validationMessage?.contains("Agent ID") == true, "require agent before enrollment")
        draft.agent = "agt_test"
        draft.server = "mcp"
        expect(draft.validationMessage != nil, "reject reserved local path")
        draft.server = "other/mcp"
        expect(draft.validationMessage != nil, "reject endpoint in server name")
        draft.server = "Cully Server"
        expect(draft.localSlug == "cully-server", "preview matches daemon slug")

        expect(AdapterTimestamp.date("2026-10-21T06:24:26.359055Z") != nil, "display fractional daemon timestamps")
        expect(AdapterTimestamp.date("2026-10-21T06:24:26Z") != nil, "display whole-second timestamps")
        expect(AdapterTimestamp.date("unavailable") == nil, "invalid timestamp fallback")
        print("Profile form and timestamp checks passed")
    }

    private static func expect(_ condition: @autoclosure () -> Bool, _ message: String) {
        if !condition() { fatalError(message) }
    }
}
