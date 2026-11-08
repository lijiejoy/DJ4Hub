import AppKit
import Foundation

let files = FileManager.default
guard CommandLine.arguments.count == 3 else {
    fputs("Usage: install-app SOURCE.app DESTINATION.app\n", stderr)
    exit(2)
}
let source = URL(fileURLWithPath: CommandLine.arguments[1]).standardizedFileURL
let target = URL(fileURLWithPath: CommandLine.arguments[2]).standardizedFileURL
guard source != target, target.pathExtension == "app",
      Bundle(url: source)?.bundleIdentifier == "com.dj4hub.client",
      files.isExecutableFile(atPath: source.appendingPathComponent("Contents/MacOS/dj4ghub-macos").path) else {
    fputs("Invalid app source or destination.\n", stderr)
    exit(2)
}

let manager = ServiceManager(bundle: target)
let oldAgent = try? Data(contentsOf: manager.agent)
let oldLegacy = try? Data(contentsOf: manager.legacyAgent)
let wasLoaded = manager.run(["print", manager.job]).status == 0
let existed = files.fileExists(atPath: target.path)
let token = ISO8601DateFormatter().string(from: Date()).replacingOccurrences(of: ":", with: "-") + "-" + UUID().uuidString
let backup = manager.support.appendingPathComponent("client-backups/\(token).app")
let staged = target.deletingLastPathComponent().appendingPathComponent(".DJ4Hub-install-\(UUID().uuidString).app")
var stopped = false
var replaced = false

func openApp() {
    let process = Process()
    process.executableURL = URL(fileURLWithPath: "/usr/bin/open")
    process.arguments = ["-a", target.path, "--args", "--background"]
    do {
        try process.run()
        process.waitUntilExit()
    } catch { fputs("无法打开状态栏 App: \(error.localizedDescription)\n", stderr) }
}

do {
    try files.createDirectory(at: backup.deletingLastPathComponent(), withIntermediateDirectories: true)
    try files.copyItem(at: source, to: staged)
    defer { try? files.removeItem(at: staged) }
    if existed { try files.copyItem(at: target, to: backup) }
    // Check immediately before the disruptive step, after slow staging/backup.
    try manager.stop()
    stopped = true
    let clients = NSRunningApplication.runningApplications(withBundleIdentifier: "com.dj4hub.client")
    for client in clients { client.terminate() }
    let deadline = Date().addingTimeInterval(5)
    while clients.contains(where: { !$0.isTerminated }) && Date() < deadline {
        RunLoop.current.run(until: Date().addingTimeInterval(0.1))
    }
    guard clients.allSatisfy({ $0.isTerminated }) else {
        throw ServiceFailure(message: "状态栏 App 未退出，安装已取消。")
    }
    replaced = true
    if existed { try files.removeItem(at: target) }
    try files.moveItem(at: staged, to: target)
    try manager.ensureStarted()
    openApp()
    print("Installed: \(target.path)")
    if existed { print("Previous app: \(backup.path)") }
    print("Bundled service is healthy; existing settings preserved.")
} catch {
    if stopped {
        if replaced {
            try? files.removeItem(at: target)
            if existed { try? files.copyItem(at: backup, to: target) }
        }
        if let oldAgent = oldAgent { try? oldAgent.write(to: manager.agent, options: .atomic) }
        else { try? files.removeItem(at: manager.agent) }
        if let oldLegacy = oldLegacy { try? oldLegacy.write(to: manager.legacyAgent, options: .atomic) }
        else { try? files.removeItem(at: manager.legacyAgent) }
        if wasLoaded {
            let previous = oldAgent != nil ? manager.agent : manager.legacyAgent
            _ = manager.run(["bootstrap", manager.domain, previous.path])
        }
        if existed { openApp() }
    }
    try? files.removeItem(at: staged)
    fputs("Install failed: \(error.localizedDescription)\n", stderr)
    exit(1)
}
