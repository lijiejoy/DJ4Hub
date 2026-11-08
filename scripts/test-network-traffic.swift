import Foundation

// Compile with the production Service.swift and TrafficHistory.swift when the
// installed command-line toolchain does not include XCTest.
@main
enum NetworkTrafficCheck {
    static func main() {
        func sample(_ time: Int, _ bytes: Int, _ counterID: String = "old") -> HubValue {
            HubValue(["available": true, "sampled_at_ms": time, "rx_bytes": bytes,
                      "tx_bytes": bytes, "interface": "en9", "counter_id": counterID])
        }
        var history = TrafficHistory()
        history.record(sample(1000, 100))
        history.record(sample(2000, 200))
        precondition(history.download == [100] && history.upload == [100])
        history.record(sample(3000, 9000, "new"))
        precondition(history.download.isEmpty && history.upload.isEmpty)
        history.record(sample(4000, 9100, "new"))
        precondition(history.download == [100])
        history.record(sample(5000, 1, "new"))
        precondition(history.download.isEmpty)
        history.record(sample(30000, 100, "new"))
        precondition(history.download.isEmpty)
        history.record(HubValue(["available": false]))
        precondition(history.download.isEmpty && history.upload.isEmpty)
        print("PASS: native traffic rates, USB replacement, counter reset, long gap, disconnect")
    }
}
