import Cocoa
import UserNotifications

final class ClaudeCompletionNotifications: NSObject, UNUserNotificationCenterDelegate {
    static let available = Bundle.main.bundleURL.pathExtension == "app"
        && Bundle.main.bundleIdentifier != nil
        && Bundle.main.bundleIdentifier != "io.github.lee-xeo.claude-cage-client"

    private static let preference = "cage.forwardClaudeCompletionNotifications"
    private static let prefix = "cage-claude-completion:"
    private static let lifetime: TimeInterval = 60
    private let center = UNUserNotificationCenter.current()
    private var since = Date()
    private var seen: [UUID: Date] = [:]
    private var target: (terminal: String, expires: Date)?
    private let showWorkbench: () -> Void

    init(showWorkbench: @escaping () -> Void) {
        self.showWorkbench = showWorkbench
        super.init()
        center.delegate = self
    }

    var enabled: Bool { UserDefaults.standard.bool(forKey: Self.preference) }

    func toggle(completion: @escaping (Bool) -> Void) {
        if enabled {
            UserDefaults.standard.set(false, forKey: Self.preference)
            target = nil
            completion(true)
            return
        }
        center.requestAuthorization(options: [.alert]) { granted, error in
            DispatchQueue.main.async {
                if let error { CageDiagnostics.record("Claude completion notification authorization failed: \(error.localizedDescription)") }
                if granted {
                    self.since = Date()
                    UserDefaults.standard.set(true, forKey: Self.preference)
                }
                completion(granted)
            }
        }
    }

    func receive(_ events: [[String: Any]], visible: Set<String>) {
        guard enabled else { return }
        let now = Date()
        let cutoff = max(since, now.addingTimeInterval(-Self.lifetime))
        seen = seen.filter { $0.value >= cutoff }
        for event in events.prefix(96) {
            guard let id = (event["id"] as? String).flatMap(UUID.init(uuidString:)),
                  let terminal = event["terminal"] as? String, Self.validTerminal(terminal),
                  let seconds = event["at"] as? Double, seconds.isFinite,
                  let message = event["message"] as? String, !message.isEmpty,
                  seen[id] == nil else { continue }
            let finished = Date(timeIntervalSince1970: seconds)
            guard finished >= cutoff, finished <= now.addingTimeInterval(5) else { continue }
            seen[id] = finished
            guard !visible.contains(terminal) else { continue }
            let content = UNMutableNotificationContent()
            let title = event["title"] as? String ?? ""
            content.title = title.isEmpty ? "Claude Code" : title
            content.body = message
            content.userInfo = ["terminal": terminal]
            let request = UNNotificationRequest(identifier: Self.prefix + id.uuidString, content: content, trigger: nil)
            center.add(request) { error in
                if let error { CageDiagnostics.record("Claude completion notification delivery failed: \(error.localizedDescription)") }
            }
        }
    }

    func target(in terminals: [String]) -> String? {
        guard let pending = target else { return nil }
        if pending.expires <= Date() { target = nil; return nil }
        guard terminals.contains(pending.terminal) else { return nil }
        target = nil
        return pending.terminal
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .list])
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
                                withCompletionHandler completionHandler: @escaping () -> Void) {
        let request = response.notification.request
        guard request.identifier.hasPrefix(Self.prefix), response.actionIdentifier == UNNotificationDefaultActionIdentifier,
              let terminal = request.content.userInfo["terminal"] as? String, Self.validTerminal(terminal)
        else { completionHandler(); return }
        DispatchQueue.main.async {
            if self.enabled {
                self.target = (terminal, Date().addingTimeInterval(Self.lifetime))
                self.showWorkbench()
            }
            completionHandler()
        }
    }

    private static func validTerminal(_ id: String) -> Bool {
        id.utf8.count == 16 && id.utf8.allSatisfy { (48...57).contains($0) || (97...102).contains($0) }
    }
}
