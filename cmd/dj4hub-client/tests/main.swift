import Foundation
import CryptoKit
import Darwin

func expect(_ condition: @autoclosure () -> Bool, _ message: String) {
    if !condition() { fatalError(message) }
}

func expectFailure(_ operation: () throws -> Void) {
    do { try operation(); fatalError("expected failure") } catch {}
}

final class Fixture {
    let home = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
    var loaded = false
    var executable = ""
    var calls: Any = []
    var commands: [[String]] = []
    var failBootstrap = false
    var failHealth = false
    var failStatus = false
    var clearHealthOnBootstrap = false
    var foreignService = false
    var reportedHash: String?
    lazy var manager: ServiceManager = ServiceManager(
        home: home, bundle: home.appendingPathComponent("DJ & 4G Hub.app"),
        readyTimeout: 0,
        run: { [unowned self] args in
            self.commands.append(args)
            switch args[0] {
            case "print":
                return ServiceCommand(status: self.loaded ? 0 : 113, output: "  program = \(self.executable)\n")
            case "bootout":
                self.loaded = false
                return ServiceCommand(status: 0, output: "")
            case "bootstrap":
                if self.failBootstrap {
                    self.failBootstrap = false
                    return ServiceCommand(status: 5, output: "test failure")
                }
                let data = try! Data(contentsOf: URL(fileURLWithPath: args[2]))
                let config = try! PropertyListSerialization.propertyList(from: data, format: nil) as! [String: Any]
                self.executable = (config["ProgramArguments"] as! [String])[0]
                self.reportedHash = nil
                self.loaded = true
                if self.clearHealthOnBootstrap { self.failHealth = false }
                return ServiceCommand(status: 0, output: "")
            default: fatalError("unexpected launchctl command")
            }
        },
        request: { [unowned self] url in
            guard self.loaded || self.foreignService else { throw ServiceFailure(message: "offline") }
            if url.path == "/api/sip/status" {
                if self.failStatus { throw ServiceFailure(message: "offline") }
                return try JSONSerialization.data(withJSONObject: ["calls": self.calls])
            }
            if self.failHealth { throw ServiceFailure(message: "offline") }
            let data = try Data(contentsOf: self.manager.helper)
            let hash = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
            return try JSONSerialization.data(withJSONObject: [
                "ok": true, "service": ["executable": self.executable, "sha256": self.reportedHash ?? hash]
            ])
        })

    init() throws {
        try FileManager.default.createDirectory(at: manager.helper.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Data("fake helper".utf8).write(to: manager.helper)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: manager.helper.path)
        try FileManager.default.createDirectory(at: manager.support, withIntermediateDirectories: true)
    }

    deinit { try? FileManager.default.removeItem(at: home) }

    func legacy() throws {
        loaded = true
        executable = manager.support.appendingPathComponent("bin/old-service").path
        let config: [String: Any] = [
            "Label": manager.label, "ProgramArguments": [executable, "-port", "/dev/test"],
            "EnvironmentVariables": ["EXISTING_SETTING": "preserved"],
            "KeepAlive": false
        ]
        try PropertyListSerialization.data(fromPropertyList: config, format: .xml, options: 0)
            .write(to: manager.legacyAgent)
    }
}

do {
    let fresh = try Fixture()
    try fresh.manager.ensureStarted()
    expect(fresh.loaded && fresh.executable == fresh.manager.helper.path, "fresh start must use bundled service")
    let config = try PropertyListSerialization.propertyList(from: Data(contentsOf: fresh.manager.agent), format: nil) as! [String: Any]
    expect(config["KeepAlive"] as? Bool == true, "crash recovery must remain enabled")
    let starts = fresh.commands.filter { $0[0] == "bootstrap" }.count
    try fresh.manager.ensureStarted()
    expect(fresh.commands.filter { $0[0] == "bootstrap" }.count == starts, "reopen must not restart healthy service")
    fresh.reportedHash = "old binary still in memory"
    try fresh.manager.ensureStarted()
    expect(fresh.commands.filter { $0[0] == "bootstrap" }.count == starts + 1, "app update must replace old running code")
    try fresh.manager.stop()
    expect(!fresh.loaded && !FileManager.default.fileExists(atPath: fresh.manager.agent.path), "stop must remove login startup")
    try fresh.manager.ensureStarted()
    expect(fresh.loaded, "stopped service must be restartable")
    try fresh.manager.ensureStarted(restart: true)
    expect(fresh.loaded, "explicit restart must restore service")

    let migration = try Fixture()
    try migration.legacy()
    let setting = migration.manager.support.appendingPathComponent("sip-password")
    try Data("do not overwrite".utf8).write(to: setting)
    try migration.manager.ensureStarted()
    expect(migration.executable == migration.manager.helper.path, "migration must replace old executable")
    expect(migration.commands.filter { $0[0] == "bootout" }.count == 1, "migration must stop exactly one job")
    let migrated = try PropertyListSerialization.propertyList(from: Data(contentsOf: migration.manager.agent), format: nil) as! [String: Any]
    expect((migrated["ProgramArguments"] as! [String]).suffix(2) == ["-port", "/dev/test"], "preserve custom arguments")
    expect((migrated["EnvironmentVariables"] as! [String: String])["EXISTING_SETTING"] == "preserved", "preserve environment")
    let savedSetting = try String(contentsOf: setting, encoding: .utf8)
    expect(savedSetting == "do not overwrite", "settings must survive migration")

    for state in ["dialing", "ringing", "active", "unknown"] {
        let busy = try Fixture()
        try busy.legacy()
        busy.calls = [["state": state]]
        expectFailure { try busy.manager.ensureStarted() }
        expectFailure { try busy.manager.stop() }
        expect(!busy.commands.contains { $0[0] == "bootout" }, "busy or unknown calls must prevent disruption")
    }
    expectFailure { try ServiceManager.requireIdle(Data("{}".utf8)) }
    expectFailure { try ServiceManager.requireIdle(Data("{\"calls\":\"bad\"}".utf8)) }
    try ServiceManager.requireIdle(Data("{\"calls\":null}".utf8))

    let rollback = try Fixture()
    try rollback.legacy()
    let previous = try Data(contentsOf: rollback.manager.legacyAgent)
    let oldPath = rollback.executable
    rollback.failBootstrap = true
    expectFailure { try rollback.manager.ensureStarted() }
    expect(rollback.loaded && rollback.executable == oldPath, "failed migration must restart old job")
    let restored = try Data(contentsOf: rollback.manager.legacyAgent)
    expect(restored == previous, "rollback must restore old plist")
    expect(!FileManager.default.fileExists(atPath: rollback.manager.agent.path), "rollback must remove new login agent")

    let timeout = try Fixture()
    timeout.failHealth = true
    expectFailure { try timeout.manager.ensureStarted() }
    expect(!timeout.loaded, "failed fresh start must not leave a crash loop")

    let dead = try Fixture()
    try dead.manager.ensureStarted()
    dead.failHealth = true
    dead.failStatus = true
    dead.clearHealthOnBootstrap = true
    let bootstrapCount = dead.commands.filter { $0[0] == "bootstrap" }.count
    try dead.manager.ensureStarted(restart: true)
    expect(dead.loaded && dead.commands.filter { $0[0] == "bootstrap" }.count == bootstrapCount + 1,
           "a loaded job with no process must be restartable even while HTTP is unavailable")

    let foreign = try Fixture()
    foreign.foreignService = true
    expectFailure { try foreign.manager.ensureStarted() }
    expect(!foreign.commands.contains { $0[0] == "bootstrap" }, "must not start beside an unmanaged service")

    let locked = try Fixture()
    let fd = Darwin.open(locked.manager.support.appendingPathComponent(".service-manager.lock").path,
                         O_CREAT | O_RDWR, S_IRUSR | S_IWUSR)
    expect(fd >= 0 && flock(fd, LOCK_EX | LOCK_NB) == 0, "fixture lock")
    expectFailure { try locked.manager.ensureStarted() }
    Darwin.close(fd)
    expect(locked.commands.isEmpty, "concurrent manager must not touch launchd")
    print("PASS: fresh install, migration, reopen, stop/start, restart, call guards, rollback, timeout, ownership, locking")
} catch {
    fputs("FAIL: \(error)\n", stderr)
    exit(1)
}
