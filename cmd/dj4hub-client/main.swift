import AppKit
import Foundation

let consoleURL = URL(string: "http://127.0.0.1:7575")!
let healthURL = consoleURL.appendingPathComponent("api/health")

final class AppDelegate: NSObject, NSApplicationDelegate {
    var statusItem: NSStatusItem!
    var dialItem: NSMenuItem!
    var timer: Timer?
    let service = ServiceManager()
    let worker = DispatchQueue(label: "com.dj4hub.client.service", qos: .userInitiated)
    var managing = false
    var serviceItems: [NSMenuItem] = []

    func applicationDidFinishLaunching(_ notification: Notification) {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        statusItem.button?.title = "○ 4G"
        let menu = NSMenu()
        let items: [(String, Selector, String?)] = [
            ("拨打电话", #selector(openDialer), "phone"),
            ("打开控制台", #selector(openConsole), "safari"),
            ("启动服务", #selector(startService), "play.circle"),
            ("重启服务", #selector(restartService), "arrow.clockwise"),
            ("停止服务", #selector(stopService), "stop.circle"),
            ("查看日志", #selector(showLogs), "doc.text"),
            ("退出状态栏（后台继续接听）", #selector(quit), nil),
            ("退出并停止服务", #selector(quitAndStop), "power")
        ]
        for (index, value) in items.enumerated() {
            if index == 2 || index == 6 { menu.addItem(.separator()) }
            let item = NSMenuItem(title: value.0, action: value.1, keyEquivalent: "")
            item.target = self
            if let symbol = value.2 {
                item.image = NSImage(systemSymbolName: symbol, accessibilityDescription: value.0)
            }
            menu.addItem(item)
            if index == 0 { dialItem = item }
            if index == 0 || (2...4).contains(index) || index == 7 { serviceItems.append(item) }
        }
        menu.autoenablesItems = false
        statusItem.menu = menu
        updateStatus()
        timer = Timer.scheduledTimer(withTimeInterval: 5, repeats: true) { [weak self] _ in
            self?.updateStatus()
        }
        manage {
            try self.service.ensureStarted()
            if !CommandLine.arguments.contains("--background") {
                DispatchQueue.main.async { NSWorkspace.shared.open(consoleURL) }
            }
        }
    }

    func updateStatus() {
        var request = URLRequest(url: healthURL)
        request.timeoutInterval = 2
        URLSession.shared.dataTask(with: request) { [weak self] _, response, _ in
            let ok = (response as? HTTPURLResponse)?.statusCode == 200
            DispatchQueue.main.async {
                self?.statusItem.button?.attributedTitle = NSAttributedString(
                    string: "\(ok ? "●" : "○") 4G",
                    attributes: [.foregroundColor: ok ? NSColor.systemGreen : NSColor.systemRed,
                                 .font: NSFont.systemFont(ofSize: 13, weight: .semibold)])
                self?.statusItem.button?.toolTip = ok ? "DJ 4G Hub · 服务运行中" : "DJ 4G Hub · 服务未响应"
            }
        }.resume()
    }

    @objc func openDialer() {
        manage {
            try self.service.ensureStarted()
            var request = URLRequest(url: consoleURL.appendingPathComponent("api/sip/panel"))
            request.httpMethod = "POST"
            request.timeoutInterval = 5
            let response = try self.post(request)
            if let error = response["error"] as? String { throw ServiceFailure(message: error) }
        }
    }

    func post(_ request: URLRequest) throws -> [String: Any] {
        // A private session keeps this worker synchronous without blocking AppKit.
        let result = PanelResult()
        let signal = DispatchSemaphore(value: 0)
        let task = URLSession.shared.dataTask(with: request) { data, response, _ in
            result.body = data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }
            result.ok = (response as? HTTPURLResponse)?.statusCode == 200
            signal.signal()
        }
        task.resume()
        guard signal.wait(timeout: .now() + 6) == .success else {
            task.cancel()
            throw ServiceFailure(message: "打开通话面板超时。")
        }
        guard result.ok else {
            throw ServiceFailure(message: result.body?["error"] as? String ?? "无法打开通话面板。")
        }
        return result.body ?? [:]
    }

    @objc func openConsole() { NSWorkspace.shared.open(consoleURL) }
    @objc func showLogs() { NSWorkspace.shared.open(service.logs) }
    @objc func quit() { NSApp.terminate(nil) }

    @objc func startService() { manage { try self.service.ensureStarted() } }
    @objc func restartService() { manage { try self.service.ensureStarted(restart: true) } }
    @objc func stopService() { manage { try self.service.stop() } }
    @objc func quitAndStop() {
        manage {
            try self.service.stop()
            DispatchQueue.main.async { NSApp.terminate(nil) }
        }
    }

    func manage(_ operation: @escaping () throws -> Void) {
        guard !managing else { return }
        managing = true
        serviceItems.forEach { $0.isEnabled = false }
        worker.async {
            var failure: String?
            do { try operation() } catch { failure = error.localizedDescription }
            let message = failure
            DispatchQueue.main.async {
                self.managing = false
                self.serviceItems.forEach { $0.isEnabled = true }
                self.updateStatus()
                if let message = message {
                    NSApp.activate(ignoringOtherApps: true)
                    let alert = NSAlert()
                    alert.messageText = "操作未完成"
                    alert.informativeText = message
                    alert.addButton(withTitle: "好")
                    alert.runModal()
                }
            }
        }
    }
}

// The semaphore above synchronizes the worker read with URLSession completion.
final class PanelResult: @unchecked Sendable {
    var body: [String: Any]?
    var ok = false
}

if let action = CommandLine.arguments.first(where: {
    ["--start-service", "--stop-service", "--restart-service"].contains($0)
}) {
    do {
        let manager = ServiceManager()
        if action == "--stop-service" { try manager.stop() }
        else { try manager.ensureStarted(restart: action == "--restart-service") }
        print("DJ 4G Hub: \(action) completed.")
        exit(0)
    } catch {
        fputs("DJ 4G Hub: \(error.localizedDescription)\n", stderr)
        exit(1)
    }
}

let application = NSApplication.shared
let delegate = AppDelegate()
application.setActivationPolicy(.accessory)
application.delegate = delegate
application.run()
