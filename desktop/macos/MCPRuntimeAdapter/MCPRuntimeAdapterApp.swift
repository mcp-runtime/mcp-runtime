import SwiftUI

@main
struct MCPRuntimeAdapterApp: App {
    var body: some Scene {
        WindowGroup {
            ContentView()
        }
        .defaultSize(width: 640, height: 720)

        WindowGroup(id: "inspect", for: String.self) { profileID in
            InspectView(profileID: profileID.wrappedValue ?? "")
        }
        .defaultSize(width: 520, height: 460)
    }
}
