import AppKit
import IOKit
import Security
import CryptoKit

@MainActor
enum MacProjectSetup {
    static var vendorURL: URL {
        #if arch(x86_64)
        return URL(fileURLWithPath: "/Library/Filesystems/macfuse.fs/Contents/Resources/macfuse.app")
        #else
        return URL(fileURLWithPath: "/Applications/macFUSE.app")
        #endif
    }

    static var needsInstaller: Bool {
        #if arch(x86_64)
        let info = NSDictionary(contentsOfFile: "/Library/Filesystems/macfuse.fs/Contents/Info.plist")
        let receipt = NSDictionary(contentsOfFile: "/var/db/receipts/io.macfuse.installer.components.core.plist")
        guard let version = info?["CFBundleShortVersionString"] as? String,
              version.compare("5.5.0", options: .numeric) != .orderedAscending,
              let installed = receipt?["PackageVersion"] as? String,
              installed.compare("5.5.0", options: .numeric) != .orderedAscending else { return true }
        return !FileManager.default.fileExists(atPath: "/usr/local/lib/libfuse3.4.dylib")
        #else
        return false
        #endif
    }

    static func openInstallerIfNeeded() async throws -> Bool {
        guard needsInstaller else { return false }
        let package = URL(fileURLWithPath: "/usr/local/share/cage/setup/macfuse.pkg")
        let attributes = try FileManager.default.attributesOfItem(atPath: package.path)
        guard attributes[.type] as? FileAttributeType == .typeRegular,
              (attributes[.ownerAccountID] as? NSNumber)?.intValue == 0,
              let permissions = attributes[.posixPermissions] as? NSNumber, permissions.intValue & 0o022 == 0,
              let size = attributes[.size] as? NSNumber, size.intValue > 0, size.intValue <= 64 * 1024 * 1024 else {
            throw Failure(7, "macfuse_installer_unavailable")
        }
        let data = try Data(contentsOf: package)
        guard SHA256.hash(data: data).map({ String(format: "%02x", $0) }).joined() == "5acf9e30bab227707de8d15eb28472a0cc9ed45709846b3e067d283294dc7f9e" else {
            throw Failure(7, "macfuse_installer_unavailable")
        }
        let installer = URL(fileURLWithPath: "/System/Library/CoreServices/Installer.app")
        _ = try await NSWorkspace.shared.open([package], withApplicationAt: installer, configuration: NSWorkspace.OpenConfiguration())
        return true
    }
}

// Invoked directly by the installation identity after verifying this bundle.
// Only Apple system settings and its authorization UI can supply a target.
@MainActor
enum MacMaintenance {
    private static let applications: Set<String> = ["com.apple.systempreferences", "com.apple.authorizationhost"]
    private static let panes = [
        "projects": "x-apple.systempreferences:com.apple.ExtensionsPreferences?extensionPointIdentifier=com.apple.fskit.fsmodule",
        "screen": "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture",
        "input": "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
    ]

    static func settingsURL(for purpose: String) -> URL? {
        panes[purpose].flatMap { URL(string: $0) }
    }

    static func runPasswordProbe(purpose: String) -> Never {
        guard panes[purpose] != nil else { exit(2) }
        MacSetupPassword.runStream()
    }

    private static func consoleSession() throws -> String {
        try consoleUser()
        let entry = IORegistryGetRootEntry(kIOMainPortDefault)
        guard entry != 0 else { throw Failure(4, "requires_gui_login") }
        defer { IOObjectRelease(entry) }
        guard let property = IORegistryEntryCreateCFProperty(entry, "IOConsoleUsers" as CFString, kCFAllocatorDefault, 0)?.takeRetainedValue(),
              let sessions = property as? [[String: Any]] else { throw Failure(4, "requires_gui_login") }
        let active = sessions.filter { $0["kCGSSessionOnConsoleKey"] as? Bool == true }
        guard active.count == 1, let current = active.first,
              current["kCGSessionLoginDoneKey"] as? Bool == true,
              current["CGSSessionScreenIsLocked"] as? Bool != true,
              current["kCGSSessionUserIDKey"] as? UInt32 == getuid(),
              current["kCGSSessionUserNameKey"] as? String == NSUserName(),
              let session = current["CGSSessionUniqueSessionUUID"] as? String,
              UUID(uuidString: session) != nil else { throw Failure(4, "requires_gui_login") }
        return session
    }

    private static func verified(_ app: NSRunningApplication, identifiers: Set<String>? = nil) -> Bool {
        guard let bundle = app.bundleIdentifier, (identifiers ?? applications).contains(bundle) else { return false }
        var code: SecCode?, requirement: SecRequirement?
        return SecRequirementCreateWithString("anchor apple and identifier \"\(bundle)\"" as CFString, [], &requirement) == errSecSuccess &&
            SecCodeCopyGuestWithAttributes(nil, [kSecGuestAttributePid as String: app.processIdentifier] as CFDictionary, [], &code) == errSecSuccess &&
            code != nil && requirement != nil && SecCodeCheckValidity(code!, [], requirement!) == errSecSuccess
    }

    static func run(_ operation: String, purpose: String) throws -> [String: Any] {
        try requireGuest()
        let session = try consoleSession()
        guard let url = settingsURL(for: purpose) else { throw Failure(8, "maintenance_purpose_invalid") }
        if operation == "open" {
            if purpose == "projects" {
                let configuration = NSWorkspace.OpenConfiguration()
                configuration.activates = false
                NSWorkspace.shared.openApplication(at: MacProjectSetup.vendorURL, configuration: configuration)
            }
            guard NSWorkspace.shared.open(url) else { throw Failure(8, "maintenance_open_failed") }
            return ["opened": true]
        }
        guard operation == "probe", let front = NSWorkspace.shared.frontmostApplication else {
            throw Failure(7, "maintenance_foreground_changed")
        }
        if !verified(front) {
            return ["error": "maintenance_foreground_changed", "blocking_pid": front.processIdentifier,
                    "blocking_bundle": front.bundleIdentifier ?? ""]
        }
        guard let windows = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] else {
            throw Failure(7, "maintenance_window_unavailable")
        }
        let display = CGDisplayBounds(CGMainDisplayID())
        var covering: [(bounds: CGRect, pid: Int32?, window: UInt32?)] = []
        for window in windows {
            guard let raw = window[kCGWindowBounds as String] as? [String: Any],
                  let bounds = CGRect(dictionaryRepresentation: raw as CFDictionary), bounds.width > 0, bounds.height > 0,
                  (window[kCGWindowAlpha as String] as? Double ?? 1) > 0 else { continue }
            let owner = window[kCGWindowOwnerPID as String] as? Int32
            if owner == front.processIdentifier && bounds.width >= 100 && bounds.height >= 60 {
                let region = bounds.intersection(display)
                guard !region.isNull, region.width >= 80, region.height >= 40,
                      let id = window[kCGWindowNumber as String] as? UInt32 else {
                    throw Failure(7, "maintenance_window_unavailable")
                }
                guard try consoleSession() == session else { throw Failure(4, "requires_gui_login") }
                guard NSWorkspace.shared.frontmostApplication?.processIdentifier == front.processIdentifier else {
                    throw Failure(7, "maintenance_foreground_changed")
                }
                if let blocker = covering.first(where: { $0.bounds.intersects(region) }) {
                    let bundle = blocker.pid.flatMap { NSRunningApplication(processIdentifier: $0)?.bundleIdentifier }
                    return ["error": "maintenance_window_obscured", "blocking_pid": blocker.pid ?? 0,
                            "blocking_window": blocker.window ?? 0, "blocking_bundle": bundle ?? ""]
                }
                return ["version": 1, "session": session, "user": NSUserName(), "pid": front.processIdentifier,
                    "window": id, "bundle": front.bundleIdentifier!,
                    "password_focused": MacSetupPassword.passwordFocused(front, window: bounds),
                    "rect": [region.minX, region.minY, region.width, region.height],
                    "display": [display.minX, display.minY, display.width, display.height]]
            }
            if owner != front.processIdentifier {
                #if arch(x86_64)
                if let owner, let app = NSRunningApplication(processIdentifier: owner),
                   let bundle = app.bundleIdentifier,
                   macMaintenanceCompositorSurface(bundle: bundle,
                       appleSigned: verified(app, identifiers: ["com.apple.dock", "com.apple.notificationcenterui"]),
                       layer: window[kCGWindowLayer as String] as? Int ?? -1,
                       bounds: bounds, display: display, systemMajor: ProcessInfo.processInfo.operatingSystemVersion.majorVersion) {
                    continue
                }
                #endif
                covering.append((bounds, owner, window[kCGWindowNumber as String] as? UInt32))
            }
        }
        throw Failure(7, "maintenance_window_unavailable")
    }
}
