import AppKit
import SwiftUI

struct Tokens: Decodable { let input: Int64; let cached: Int64; let output: Int64; let reasoning: Int64 }
struct Row: Decodable, Identifiable {
    var id: String { name }
    let name: String
    let usage_updates: Int
    let tokens: Tokens
    let total_tokens: Int64
    let cache_percent: Double
    let known_value_usd: Double
    let unpriced_updates: Int
    var value: String {
        if usage_updates > 0 && unpriced_updates == usage_updates { return "Unpriced" }
        return String(format: "~$%.2f", known_value_usd) + (unpriced_updates > 0 ? " + ?" : "")
    }
}
struct Quota: Decodable, Identifiable {
    var id: String { bucket + window }
    let bucket: String
    let window: String
    let used_percent: Double
    let window_minutes: Int
    let resets_at: Int64
    let observed_at: String
    let age_seconds: Int64
    let stale: Bool
    let plan: String
    var duration: String {
        if window_minutes <= 0 { return "Unknown window" }
        if window_minutes % 1440 == 0 { return "\(window_minutes / 1440)-day window" }
        if window_minutes % 60 == 0 { return "\(window_minutes / 60)-hour window" }
        return "\(window_minutes)-minute window"
    }
}
struct Diagnostics: Decodable {
    let files: Int
    let malformed_records: Int
    let counter_regressions: Int
    let missing_timestamps: Int
    let inferred_counter_resets: Int
    let reconciled_sessions: Int
    let warnings: [String]?
    var problems: Int { malformed_records + counter_regressions + missing_timestamps + (warnings?.count ?? 0) }
}
struct Snapshot: Decodable {
    let summary: Row
    let models: [Row]
    let daily: [Row]
    let projects: [Row]
    let quotas: [Quota]
    let diagnostics: Diagnostics
}
func compact(_ n: Int64) -> String {
    if n >= 1_000_000 { return String(format:"%.2fM", Double(n)/1_000_000) }
    if n >= 1_000 { return String(format:"%.1fK", Double(n)/1_000) }
    return "\(n)"
}

@MainActor final class Store: ObservableObject {
    @Published var snapshot: Snapshot?
    @Published var error: String?
    @Published var period = "today"
    @Published var lastUpdate: Date?
    private var process: Process?
    private var pipe: Pipe?
    private var buffer = Data()
    var onChange: (() -> Void)?
    func start() {
        stop()
        error = nil
        snapshot = nil
        lastUpdate = nil
        let task = Process()
        let output = Pipe()
        task.executableURL = Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/burn")
        task.arguments = ["watch", "--since", period, "--interval", "15s"]
        // Finder doesn't inherit shell environment. Optional local settings support custom roots/prices.
        var env = ProcessInfo.processInfo.environment
        let settings = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".config/burn/settings.json")
        if FileManager.default.fileExists(atPath: settings.path) {
            do {
                let data = try Data(contentsOf: settings)
                let values = try JSONDecoder().decode([String:String].self, from:data)
                for key in ["CODEX_HOME", "CLAUDE_CONFIG_DIR", "BURN_PRICES"] {
                    if let value = values[key] { env[key] = value }
                }
            } catch { self.error = "Invalid ~/.config/burn/settings.json"; onChange?(); return }
        }
        task.environment = env
        task.standardOutput = output
        task.standardError = FileHandle.nullDevice
        process = task
        pipe = output
        output.fileHandleForReading.readabilityHandler = { [weak self, weak task] handle in
            let data = handle.availableData
            Task { @MainActor [weak self, weak task] in
                guard let self, let task, self.process === task else { return }
                if data.isEmpty { handle.readabilityHandler = nil; return }
                self.buffer.append(data)
                while let end = self.buffer.firstIndex(of: 10) {
                    let line = self.buffer.prefix(upTo: end)
                    self.buffer.removeSubrange(...end)
                    do {
                        self.snapshot = try JSONDecoder().decode(Snapshot.self, from: line)
                        self.lastUpdate = Date()
                        self.error = nil
                    } catch { self.error = "Could not decode Go engine output. Rebuild both components." }
                    self.onChange?()
                }
                if self.buffer.count > 16_000_000 { self.error = "Engine response too large"; self.stop(); self.onChange?() }
            }
        }
        task.terminationHandler = { [weak self, weak task] _ in
            Task { @MainActor [weak self, weak task] in
                guard let self, let task, self.process === task else { return }
                self.error = "Usage engine stopped. Choose Refresh to retry."
                self.onChange?()
            }
        }
        do { try task.run() } catch { self.error = "Could not start bundled Go engine. Rebuild the app."; onChange?() }
    }
    func stop() {
        pipe?.fileHandleForReading.readabilityHandler = nil
        process?.terminationHandler = nil
        if let task = process, task.isRunning { task.terminate() }
        process = nil; pipe = nil; buffer.removeAll()
    }
}

struct Dashboard: View {
    @ObservedObject var store: Store
    var size = NSSize(width: 410, height: 640)
    @State private var detail = "Models"
    var body: some View {
        VStack(spacing: 0) {
        ScrollView(.vertical) {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                Image(systemName: "flame.fill").foregroundStyle(.orange)
                Text("Burn").font(.title2.bold())
                Spacer()
                Text("LOCAL USAGE").font(.caption2.weight(.semibold)).foregroundStyle(.secondary)
            }
            Picker("Period", selection: $store.period) {
                Text("Today").tag("today"); Text("7 days").tag("7d"); Text("30 days").tag("30d")
            }.pickerStyle(.segmented).labelsHidden().onChange(of: store.period) { _ in store.start() }
            if let error = store.error {
                Label(error, systemImage:"exclamationmark.triangle.fill").foregroundStyle(.orange).font(.callout)
            }
            if let data = store.snapshot {
                HStack(alignment:.top) {
                    metric("Recorded tokens", compact(data.summary.total_tokens))
                    Spacer()
                    metric("API-equivalent value", data.summary.value)
                }
                HStack {
                    Label("\(data.summary.usage_updates) usage updates", systemImage:"waveform.path")
                    Spacer()
                    Text(String(format:"%.1f%% cache hit",data.summary.cache_percent))
                }.font(.caption).foregroundStyle(.secondary)
                Divider()
                Text("CODEX ACCOUNT LIMITS").font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                if data.quotas.isEmpty {
                    Text("No quota snapshot in local logs yet. Run a Codex task, then refresh.").font(.callout).foregroundStyle(.secondary)
                }
                ForEach(data.quotas) { quota in
                    VStack(alignment:.leading, spacing:5) {
                        HStack {
                            Text(quota.duration).fontWeight(.medium)
                            Spacer()
                            Text(String(format:"%.0f%% used",quota.used_percent)).monospacedDigit()
                        }
                        ProgressView(value: quota.used_percent, total:100).tint(quota.stale ? .gray : .orange)
                        HStack {
                            Text("\(quota.bucket) · \(quota.plan)")
                            Spacer()
                            Text(quota.stale ? "Stale snapshot" : "Observed \(max(0,quota.age_seconds)/60)m ago")
                        }.font(.caption2).foregroundStyle(quota.stale ? .orange : .secondary)
                        if quota.resets_at > 0 {
                            Text("Reset: \(Date(timeIntervalSince1970:Double(quota.resets_at)).formatted(date:.abbreviated,time:.shortened))")
                                .font(.caption2).foregroundStyle(.secondary)
                        }
                    }
                }
                Divider()
                Picker("Breakdown",selection:$detail) {
                    Text("Models").tag("Models");Text("Days").tag("Days");Text("Projects").tag("Projects")
                }.pickerStyle(.segmented).labelsHidden()
                    VStack(alignment:.leading,spacing:10) {
                        ForEach(detail == "Days" ? data.daily : detail == "Projects" ? data.projects : data.models) { row in
                            HStack {
                                VStack(alignment:.leading,spacing:3) {
                                    Text(row.name).font(.callout).lineLimit(2)
                                    Text("\(compact(row.total_tokens)) tokens · \(row.usage_updates) updates").font(.caption2).foregroundStyle(.secondary)
                                }
                                Spacer()
                                Text(row.value).font(.callout.monospacedDigit())
                            }
                        }
                        if data.summary.usage_updates == 0 { Text("No recorded usage in this period.").foregroundStyle(.secondary) }
                    }
                if data.diagnostics.problems > 0 {
                    Text("⚠ \(data.diagnostics.problems) accounting issues. Run burn doctor for details.").font(.caption).foregroundStyle(.orange)
                }
                Text("Standard short-context API value, not your subscription bill. Unpriced usage is excluded. Quotas are recorded snapshots; remote and cloud activity may be absent.")
                    .font(.caption2).foregroundStyle(.secondary).fixedSize(horizontal:false,vertical:true)
                Text("\(data.diagnostics.files) files · \(data.diagnostics.reconciled_sessions) sessions reconciled · \(data.diagnostics.inferred_counter_resets) inferred resets")
                    .font(.caption2).foregroundStyle(.secondary)
            } else if store.error == nil { ProgressView("Reading local usage…").padding(.vertical) }
        }.padding(20).frame(maxWidth: .infinity, alignment: .leading)
        }
            Divider()
            HStack {
                Button("Refresh") { store.start() }
                if let updated = store.lastUpdate { Text(updated,style:.time).font(.caption2).foregroundStyle(.secondary) }
                Spacer()
                Button("Quit") { NSApp.terminate(nil) }.keyboardShortcut("q")
            }.padding(.horizontal, 20).padding(.vertical, 12)
        }.frame(width: size.width, height: size.height)
    }
    func metric(_ title:String,_ value:String)->some View {
        VStack(alignment:.leading,spacing:5) {Text(title).font(.caption).foregroundStyle(.secondary);Text(value).font(.system(size:26,weight:.semibold,design:.rounded)).monospacedDigit()}
    }
}

@MainActor final class AppDelegate: NSObject, NSApplicationDelegate {
    let store = Store()
    var status: NSStatusItem!
    let popover = NSPopover()
    var hosting: NSHostingController<Dashboard>!
    var dashboardWindow: NSWindow?
    func applicationDidFinishLaunching(_ notification: Notification) {
        // Keep a single engine when the application is opened repeatedly.
        let others = NSRunningApplication.runningApplications(withBundleIdentifier:Bundle.main.bundleIdentifier ?? "local.burn.meter")
        if others.contains(where:{$0.processIdentifier != ProcessInfo.processInfo.processIdentifier}) { NSApp.terminate(nil); return }
        // A growing token label can push the item out of a crowded menu bar.
        status = NSStatusBar.system.statusItem(withLength:NSStatusItem.squareLength)
        status.autosaveName = "BurnStatusItem"
        status.button?.image = NSImage(systemSymbolName: "flame.fill", accessibilityDescription: "Burn usage")
        status.button?.image?.isTemplate = true
        status.button?.toolTip = "Burn · Reading local usage…"
        status.button?.target = self
        status.button?.action = #selector(toggle)
        popover.behavior = .transient
        hosting = NSHostingController(rootView:Dashboard(store:store))
        popover.contentViewController = hosting
        store.onChange = { [weak self] in
            guard let self else {return}
            if let error = self.store.error {self.status.button?.toolTip = "Burn · " + error}
            else if let snap = self.store.snapshot {self.status.button?.toolTip = "Burn · " + compact(snap.summary.total_tokens) + " tokens"}
        }
        store.start()
    }
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        // Opening the running app must work even when macOS hides its menu item.
        guard status != nil else { return false }
        popover.performClose(nil)
        if dashboardWindow == nil {
            let available = NSScreen.main?.visibleFrame.size ?? NSSize(width: 800, height: 700)
            let size = NSSize(width: min(410, max(1, available.width - 32)),
                              height: min(640, max(1, available.height - 64)))
            let window = NSWindow(contentRect: NSRect(origin: .zero, size: size),
                                  styleMask: [.titled, .closable, .miniaturizable],
                                  backing: .buffered, defer: false)
            window.title = "Burn"
            window.isReleasedWhenClosed = false
            window.contentViewController = NSHostingController(rootView: Dashboard(store: store, size: size))
            window.center()
            dashboardWindow = window
        }
        dashboardWindow?.deminiaturize(nil)
        dashboardWindow?.makeKeyAndOrderFront(nil)
        sender.activate(ignoringOtherApps: true)
        return false
    }
    @objc func toggle() {
        if popover.isShown {popover.performClose(nil)}
        else if let button = status.button {
            // Use the menu item's screen, including scaled/multiple displays.
            // Leave room for the popover arrow and screen edges.
            let available = (button.window?.screen ?? NSScreen.main)?.visibleFrame.size ?? NSSize(width: 800, height: 700)
            let size = NSSize(width: min(410, max(1, available.width - 32)),
                              height: min(640, max(1, available.height - 32)))
            hosting.rootView = Dashboard(store: store, size: size)
            popover.contentSize = size
            popover.show(relativeTo:button.bounds,of:button,preferredEdge:.minY)
            NSApp.activate(ignoringOtherApps:true)
        }
    }
    func applicationWillTerminate(_ notification: Notification) {store.stop()}
}
@main struct BurnMain {
    @MainActor static func main() {
        let app = NSApplication.shared
        let delegate = AppDelegate()
        app.delegate = delegate
        app.setActivationPolicy(.accessory)
        withExtendedLifetime(delegate) { app.run() }
    }
}
