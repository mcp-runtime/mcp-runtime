import SwiftUI

@main
struct MCPRuntimeAdapterApp: App {
    var body: some Scene {
        WindowGroup {
            ContentView()
        }
        .defaultSize(width: 720, height: 760)

        WindowGroup(id: "inspect", for: String.self) { profileID in
            InspectView(profileID: profileID.wrappedValue ?? "")
        }
        .defaultSize(width: 520, height: 460)
    }
}
