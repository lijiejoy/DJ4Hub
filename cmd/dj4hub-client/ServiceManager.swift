import Foundation
import Darwin
import CryptoKit

struct ServiceFailure: LocalizedError {
    let message: String
    var errorDescription: String? { message }
}

struct ServiceCommand {
    var status: Int32
    var output: String
}

// All operations run on the client's serial worker queue. The file lock also
// excludes another copy of the app managing this user's same launch job.
final class ServiceManager {
    let label = "com.dj4hub.local"
    let domain: String
    let support: URL
    let agent: URL
    let helper: URL
    let logs: URL
    let baseURL: URL
    let run: ([String]) -> ServiceCommand
    let request: (URL) throws -> Data
    let readyTimeout: TimeInterval
    private let files = FileManager.default
    private lazy var helperHash: String? = {
        guard let data = try? Data(contentsOf: helper, options: .mappedIfSafe) else { return nil }
        return SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
    }()

    init(home: URL = FileManager.default.homeDirectoryForCurrentUser,
         bundle: URL = Bundle.main.bundleURL,
         baseURL: URL = URL(string: "http://127.0.0.1:7575")!,
         readyTimeout: TimeInterval = 20,
         run: @escaping ([String]) -> ServiceCommand = ServiceManager.launchctl,
         request: @escaping (URL) throws -> Data = ServiceManager.fetch) {
        domain = "gui/\(getuid())"
        support = home.appendingPathComponent("Library/Application Support/DJ 4G Hub")
        agent = home.appendingPathComponent("Library/LaunchAgents/com.dj4hub.local.plist")
        helper = bundle.appendingPathComponent("Contents/MacOS/dj4ghub-macos")
        logs = home.appendingPathComponent("Library/Logs/DJ 4G Hub/dj4ghub-launchd.log")
        self.baseURL = baseURL
        self.readyTimeout = readyTimeout
        self.run = run
        self.request = request
    }

    static func launchctl(_ args: [String]) -> ServiceCommand {
        let process = Process()
        let output = Pipe()
        process.executableURL = URL(fileURLWithPath: "/bin/launchctl")
        process.arguments = args
        process.standardOutput = output
        process.standardError = output
        do {
            try process.run()
            let data = output.fileHandleForReading.readDataToEndOfFile()
            process.waitUntilExit()
            return ServiceCommand(status: process.terminationStatus,
                                  output: String(data: data, encoding: .utf8) ?? "")
        } catch { return ServiceCommand(status: -1, output: error.localizedDescription) }
    }

    static func fetch(_ url: URL) throws -> Data {
        let signal = DispatchSemaphore(value: 0)
        let result = HTTPResult()
        var req = URLRequest(url: url)
        req.timeoutInterval = 2
        req.cachePolicy = .reloadIgnoringLocalCacheData
        let task = URLSession.shared.dataTask(with: req) { data, response, error in
            result.data = data
            result.ok = (response as? HTTPURLResponse)?.statusCode == 200 && error == nil
            signal.signal()
        }
        task.resume()
        guard signal.wait(timeout: .now() + 3) == .success else {
            task.cancel()
            throw ServiceFailure(message: "服务响应超时。")
        }
        guard result.ok, let data = result.data else {
            throw ServiceFailure(message: "无法确认服务状态，请查看日志。")
        }
        return data
    }

    var job: String { "\(domain)/\(label)" }
    var legacyAgent: URL { support.appendingPathComponent("\(label).plist") }

    func healthy() -> Bool {
        guard let data = try? request(baseURL.appendingPathComponent("api/health")),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
        else { return false }
        return object["ok"] as? Bool == true
    }

    private func runningBundledVersion() -> Bool {
        guard let data = try? request(baseURL.appendingPathComponent("api/health")),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let identity = object["service"] as? [String: Any],
              let expected = helperHash else { return false }
        return identity["executable"] as? String == helper.path
            && identity["sha256"] as? String == expected
    }

    static func requireIdle(_ data: Data) throws {
        guard let body = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              body["calls"] != nil,
              body["calls"] is NSNull || body["calls"] is [[String: Any]] else {
            throw ServiceFailure(message: "无法确认通话状态，暂不停止或更新服务。")
        }
        for call in body["calls"] as? [[String: Any]] ?? [] {
            guard let state = call["state"] as? String,
                  ["ended", "failed", "missed"].contains(state) else {
                throw ServiceFailure(message: "正在通话或有来电，请挂断后再操作。")
            }
        }
    }

    private func checkIdle(loaded: ServiceCommand) throws {
        // launchd retains crashed jobs while it waits to respawn them. No PID
        // means there is no running process or call to protect.
        let pidText = loaded.output.components(separatedBy: .newlines).first {
            $0.trimmingCharacters(in: .whitespaces).hasPrefix("pid = ")
        }?.components(separatedBy: "=").last
        let pid = pidText.flatMap { Int32($0.trimmingCharacters(in: .whitespaces)) } ?? 0
        if pid == 0 && !healthy() { return }
        try Self.requireIdle(request(baseURL.appendingPathComponent("api/sip/status")))
    }

    private func locked<T>(_ action: () throws -> T) throws -> T {
        try files.createDirectory(at: support, withIntermediateDirectories: true)
        let fd = Darwin.open(support.appendingPathComponent(".service-manager.lock").path,
                             O_CREAT | O_RDWR, S_IRUSR | S_IWUSR)
        guard fd >= 0 else { throw ServiceFailure(message: "无法打开服务管理锁。") }
        defer { Darwin.close(fd) }
        guard flock(fd, LOCK_EX | LOCK_NB) == 0 else {
            throw ServiceFailure(message: "另一个窗口正在管理服务，请稍后重试。")
        }
        defer { flock(fd, LOCK_UN) }
        return try action()
    }

    private func configuration() throws -> Data {
        var config: [String: Any] = [:]
        if let data = try? Data(contentsOf: agent),
           let old = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any] {
            config = old
        } else if let data = try? Data(contentsOf: legacyAgent),
                  let old = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any] {
            config = old
        }
        let oldArguments = config["ProgramArguments"] as? [String] ?? []
        config["Label"] = label
        config.removeValue(forKey: "Program")
        config["ProgramArguments"] = [helper.path] + Array(oldArguments.dropFirst())
        var env = config["EnvironmentVariables"] as? [String: String] ?? [:]
        env["PATH"] = helper.deletingLastPathComponent().path
            + ":/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin"
        config["EnvironmentVariables"] = env
        config["WorkingDirectory"] = support.path
        config["StandardOutPath"] = logs.path
        config["StandardErrorPath"] = logs.path
        config["RunAtLoad"] = true
        config["KeepAlive"] = true
        config["ThrottleInterval"] = 10
        return try PropertyListSerialization.data(fromPropertyList: config, format: .xml, options: 0)
    }

    private func waitReady() throws {
        let deadline = Date().addingTimeInterval(readyTimeout)
        repeat {
            if healthy() && runningBundledVersion() { return }
            Thread.sleep(forTimeInterval: 0.25)
        } while Date() < deadline
        throw ServiceFailure(message: "服务未能在规定时间内启动，请查看日志。")
    }

    private func unload(_ loaded: ServiceCommand) throws {
        guard run(["bootout", job]).status == 0 else {
            throw ServiceFailure(message: "无法停止旧服务，未进行替换。")
        }
        let pidLine = loaded.output.components(separatedBy: .newlines).first {
            $0.trimmingCharacters(in: .whitespaces).hasPrefix("pid = ")
        }
        if let text = pidLine?.components(separatedBy: "=").last,
           let pid = Int32(text.trimmingCharacters(in: .whitespaces)), pid > 1 {
            let deadline = Date().addingTimeInterval(10)
            while Darwin.kill(pid, 0) == 0 || errno == EPERM {
                guard Date() < deadline else {
                    throw ServiceFailure(message: "旧服务仍在退出，请稍后重试。")
                }
                Thread.sleep(forTimeInterval: 0.1)
            }
        }
    }

    func ensureStarted(restart: Bool = false) throws {
        try locked {
            guard files.isExecutableFile(atPath: helper.path) else {
                throw ServiceFailure(message: "App 内缺少通话服务，请重新安装完整的 DJ 4G Hub.app。")
            }
            let desired = try configuration()
            let loaded = run(["print", job])
            let isLoaded = loaded.status == 0
            let matches = (try? Data(contentsOf: agent)) == desired
                && loaded.output.components(separatedBy: .newlines).contains {
                    $0.trimmingCharacters(in: .whitespaces) == "program = \(helper.path)"
                }
            if isLoaded && matches && runningBundledVersion() && !restart {
                try waitReady()
                return
            }
            if isLoaded { try checkIdle(loaded: loaded) }
            else if healthy() {
                throw ServiceFailure(message: "已有其他方式启动的服务正在运行，请先退出该服务。")
            }

            try files.createDirectory(at: agent.deletingLastPathComponent(), withIntermediateDirectories: true)
            try files.createDirectory(at: logs.deletingLastPathComponent(), withIntermediateDirectories: true)
            let oldAgent = try? Data(contentsOf: agent)
            let oldLegacy = try? Data(contentsOf: legacyAgent)
            let rollback = oldAgent ?? oldLegacy
            if let rollback = rollback {
                let backup = support.appendingPathComponent("service-backups/\(UUID().uuidString).plist")
                try files.createDirectory(at: backup.deletingLastPathComponent(), withIntermediateDirectories: true)
                try rollback.write(to: backup, options: .atomic)
            }
            if isLoaded { try unload(loaded) }
            do {
                try desired.write(to: agent, options: .atomic)
                // Keep the old launcher path pointed at the same bundled helper.
                try desired.write(to: legacyAgent, options: .atomic)
                guard run(["bootstrap", domain, agent.path]).status == 0 else {
                    throw ServiceFailure(message: "无法注册后台服务，请检查系统后台项目设置和日志。")
                }
                try waitReady()
            } catch {
                _ = run(["bootout", job])
                if let oldAgent = oldAgent { try? oldAgent.write(to: agent, options: .atomic) }
                else { try? files.removeItem(at: agent) }
                if let oldLegacy = oldLegacy { try? oldLegacy.write(to: legacyAgent, options: .atomic) }
                else { try? files.removeItem(at: legacyAgent) }
                if isLoaded, let rollback = rollback {
                    let source = oldAgent != nil ? agent : legacyAgent
                    try? rollback.write(to: source, options: .atomic)
                    _ = run(["bootstrap", domain, source.path])
                }
                throw error
            }
        }
    }

    func stop() throws {
        try locked {
            let loaded = run(["print", job])
            if loaded.status == 0 {
                try checkIdle(loaded: loaded)
                try unload(loaded)
            } else if healthy() {
                throw ServiceFailure(message: "服务由其他方式启动，无法从当前 App 停止。")
            }
            // Removing the login agent makes an explicit stop survive login.
            try? files.removeItem(at: agent)
        }
    }
}

private final class HTTPResult: @unchecked Sendable {
    var data: Data?
    var ok = false
}
