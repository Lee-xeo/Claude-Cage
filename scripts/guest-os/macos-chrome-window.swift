// The Aqua user launches signed Chrome with the same profile as the previous
// workbench. Only windows of that profile or an active login can be shared.
import AppKit
import ScreenCaptureKit
import Security

@MainActor
final class MacChromeWindow {
    static let shared = MacChromeWindow()
    private let app = URL(fileURLWithPath: "/Applications/Google Chrome.app")
    private let bundleID = "com.google.Chrome"
    private let profile = FileManager.default.homeDirectoryForCurrentUser
        .appendingPathComponent("Library/Application Support/Cage/Browser", isDirectory: true)
    private let fm = FileManager.default
    private let loginRegistry = FileManager.default.homeDirectoryForCurrentUser
        .appendingPathComponent("Library/Caches/dev.cage.GuestClaudeWindow/LoginSessions", isDirectory: true)
    private let requirement: SecRequirement?
    private struct Login {
        let directory: URL
        let process: Process
        let expiry: Task<Void, Never>
    }
    private var logins: [String: Login] = [:]
    private var currentLogin: String?

    private init() {
        var rule: SecRequirement?
        let expression = #"anchor apple generic and identifier "com.google.Chrome" and certificate leaf[subject.OU] = "EQHXZ8M8AV""#
        SecRequirementCreateWithString(expression as CFString, [], &rule)
        requirement = rule
        // An interrupted helper loses its timers. Revoke the temporary
        // browsers it recorded, without touching the user's normal profile.
        if let entries = try? fm.contentsOfDirectory(at: loginRegistry, includingPropertiesForKeys: nil) {
            for entry in entries where UUID(uuidString: entry.lastPathComponent) != nil {
                let directory = temporaryProfile(entry.lastPathComponent)
                let apps = applications(directory)
                for app in apps { Darwin.kill(app.processIdentifier, SIGTERM) }
                Task { @MainActor in
                    for _ in 0..<50 where apps.contains(where: { !$0.isTerminated }) {
                        try? await Task.sleep(nanoseconds: 100_000_000)
                    }
                    if self.applications(directory).isEmpty {
                        self.removeTemporaryProfile(directory, entry: entry)
                    }
                }
            }
        }
    }

    private func temporaryProfile(_ id: String) -> URL {
        fm.temporaryDirectory.appendingPathComponent("Browser-" + id, isDirectory: true)
    }

    private func removeTemporaryProfile(_ directory: URL, entry: URL) {
        do {
            if fm.fileExists(atPath: directory.path) { try fm.removeItem(at: directory) }
            try fm.removeItem(at: entry)
        } catch { GuestDiagnostic.record("window", "login_cleanup_pending") }
    }

    // A macOS Chrome process can own another user's browsing profile in the
    // same Aqua session. Read only argv, never its environment or page URLs.
    private func arguments(_ pid: pid_t) -> [String] {
        var mib = [CTL_KERN, KERN_PROCARGS2, pid]
        var size = 0
        guard sysctl(&mib, 3, nil, &size, nil, 0) == 0, size > 4, size <= 4 << 20 else { return [] }
        var bytes = [UInt8](repeating: 0, count: size)
        guard sysctl(&mib, 3, &bytes, &size, nil, 0) == 0, size > 4 else { return [] }
        let argc = bytes.withUnsafeBytes { $0.loadUnaligned(as: Int32.self) }
        guard argc > 0, argc < 4096, let end = bytes[4..<size].firstIndex(of: 0) else { return [] }
        var start = end + 1
        while start < size && bytes[start] == 0 { start += 1 }
        var args: [String] = []
        for _ in 0..<argc {
            guard start < size, let end = bytes[start..<size].firstIndex(of: 0) else { return [] }
            args.append(String(decoding: bytes[start..<end], as: UTF8.self))
            start = end + 1
        }
        return args
    }

    private func applications(_ directory: URL) -> [NSRunningApplication] {
        guard let requirement else { return [] }
        return NSRunningApplication.runningApplications(withBundleIdentifier: bundleID).filter { running in
            guard !running.isTerminated, running.bundleURL?.standardizedFileURL == app.standardizedFileURL else { return false }
            let args = arguments(running.processIdentifier)
            guard args.contains("--user-data-dir=" + directory.path),
                  !args.contains(where: { $0 == "--headless" || $0.hasPrefix("--headless=") || $0 == "--remote-debugging-pipe" }) else { return false }
            var code: SecCode?
            let attrs = [kSecGuestAttributePid: running.processIdentifier] as CFDictionary
            return SecCodeCopyGuestWithAttributes(nil, attrs, [], &code) == errSecSuccess &&
                code.map { SecCodeCheckValidity($0, [], requirement) == errSecSuccess } == true
        }
    }

    private var activeDirectory: URL { currentLogin.flatMap { logins[$0]?.directory } ?? profile }
    func owns(_ pid: pid_t) -> Bool { applications(activeDirectory).contains { $0.processIdentifier == pid } }
    var status: [String: Any] {
        let regular = applications(profile).first
        var result: [String: Any] = ["surface": "browser", "installed": fm.fileExists(atPath: app.path),
         "running": !applications(activeDirectory).isEmpty, "version": Bundle(url: app)?.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "",
         "login": currentLogin != nil, "screen": CGPreflightScreenCaptureAccess(),
         "input": AXIsProcessTrusted(), "input_protocol": 1, "persistent": true, "push_frames": true]
        if let regular {
            result["profile_pid"] = regular.processIdentifier
            var info = proc_bsdinfo()
            let length = Int32(MemoryLayout.size(ofValue: info))
            if proc_pidinfo(regular.processIdentifier, PROC_PIDTBSDINFO, 0, &info, length) == length {
                result["profile_started"] = info.pbi_start_tvsec
            }
        }
        return result
    }

    static func profileArguments(_ profile: URL) throws -> [String] {
        let fm = FileManager.default
        try fm.createDirectory(at: profile, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        var args = ["--user-data-dir=" + profile.path, "--no-first-run", "--no-default-browser-check"]
        // Playwright used Chrome's mock Keychain for OS crypt. Keep that
        // backend only for existing profiles, or their cookies cannot decrypt.
        let marker = profile.appendingPathComponent(".system-keychain")
        if !fm.fileExists(atPath: marker.path) {
            if fm.fileExists(atPath: profile.appendingPathComponent("Local State").path) {
                args.append("--use-mock-keychain")
            } else { try Data().write(to: marker, options: .atomic) }
        }
        return args
    }

    private func start(_ args: [String]) throws -> Process {
        var code: SecStaticCode?
        guard let requirement, SecStaticCodeCreateWithPath(app as CFURL, [], &code) == errSecSuccess,
              let code, SecStaticCodeCheckValidity(code, [], requirement) == errSecSuccess else { throw Failure(5, "identity_error") }
        let process = Process()
        process.executableURL = app.appendingPathComponent("Contents/MacOS/Google Chrome")
        process.arguments = args + (try Self.creationArguments())
        process.currentDirectoryURL = fm.homeDirectoryForCurrentUser
        process.environment = ProcessInfo.processInfo.environment.filter { ["HOME", "USER", "LOGNAME", "PATH", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE"].contains($0.key) }
        process.standardInput = FileHandle.nullDevice
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        try process.run()
        return process
    }

    // Read only this guest user's public, persisted creation preferences.
    // These switches also apply to temporary authorization profiles.
    static func creationArguments() throws -> [String] {
        let file = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/Application Support/Cage/guest-profile.json")
        if !FileManager.default.fileExists(atPath: file.path) { return [] }
        let attributes = try FileManager.default.attributesOfItem(atPath: file.path)
        guard attributes[.type] as? FileAttributeType == .typeRegular,
              let size = attributes[.size] as? NSNumber, size.intValue <= 4096 else { throw Failure(8, "invalid_profile") }
        struct Preferences: Decodable {
            let browser_width: Int
            let browser_height: Int
            let webgl: String
            let canvas: String
            let graphics_backend: String?
        }
        let value = try JSONDecoder().decode(Preferences.self, from: Data(contentsOf: file))
        guard (800...3840).contains(value.browser_width), (600...2160).contains(value.browser_height),
              ["native", "disabled"].contains(value.webgl), ["native", "blocked"].contains(value.canvas) else { throw Failure(8, "invalid_profile") }
        var flags = ["--window-size=\(value.browser_width),\(value.browser_height)"]
        let backend = value.graphics_backend ?? "auto"
        guard ["auto", "metal", "opengl", "software"].contains(backend), value.webgl != "disabled" || backend == "auto" else { throw Failure(8, "invalid_profile") }
        if backend != "auto" {
            flags += ["--use-gl=angle", "--use-angle=" + (backend == "opengl" ? "gl" : backend == "software" ? "swiftshader" : "metal")]
        }
        if value.webgl == "disabled" { flags += ["--disable-3d-apis", "--disable-webgl"] }
        if value.canvas == "blocked" { flags.append("--disable-reading-from-canvas") }
        return flags
    }

    func launch(_ input: Request) throws {
        var address: String?
        if let raw = input.url {
            guard raw.utf8.count <= 16384, let url = URLComponents(string: raw),
                  ["http","https"].contains(url.scheme?.lowercased()), let host = url.host, !host.isEmpty,
                  url.user == nil, url.password == nil else { throw Failure(8, "invalid_url") }
            if input.login != nil {
                guard url.scheme == "https", ["claude.ai","claude.com","console.anthropic.com","platform.claude.com","platform.anthropic.com"].contains(host.lowercased()) else { throw Failure(8, "invalid_login") }
            }
            address = raw
        }
        if let login = input.login {
            guard login.range(of: "^[a-f0-9-]{16,64}$", options: .regularExpression) != nil, let address else { throw Failure(8, "invalid_login") }
            if let existing = logins[login], existing.process.isRunning {
                currentLogin = login
                applications(existing.directory).first?.activate()
                return
            }
            let id = UUID().uuidString
            let directory = temporaryProfile(id)
            let entry = loginRegistry.appendingPathComponent(id)
            try fm.createDirectory(at: loginRegistry, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
            try fm.createDirectory(at: directory, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
            let process: Process
            do {
                try Data().write(to: entry, options: .withoutOverwriting)
                process = try start(["--user-data-dir=" + directory.path, "--no-first-run", "--no-default-browser-check", "--incognito", address])
            } catch { removeTemporaryProfile(directory, entry: entry); throw error }
            let expiry = Task { @MainActor [weak self] in
                do { try await Task.sleep(nanoseconds: 900_000_000_000) } catch { return }
                self?.finish(login)
            }
            currentLogin = login
            logins[login] = Login(directory: directory, process: process, expiry: expiry)
            process.terminationHandler = { [weak self] _ in
                Task { @MainActor in
                    if self?.logins[login]?.directory == directory { self?.finish(login) }
                    self?.removeTemporaryProfile(directory, entry: entry)
                }
            }
            return
        }
        // Attaching a new workbench viewer must not interrupt an authorization.
        if address == nil, let login = currentLogin, let pending = logins[login], pending.process.isRunning {
            applications(pending.directory).first?.activate()
            return
        }
        currentLogin = nil
        if let running = applications(profile).first, address == nil {
            running.activate()
            if CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID)
                .flatMap({ $0 as? [[String: Any]] })?.contains(where: {
                    $0[kCGWindowOwnerPID as String] as? pid_t == running.processIdentifier && $0[kCGWindowLayer as String] as? Int == 0
                }) == true { return }
        }
        var args = try Self.profileArguments(profile)
        if applications(profile).isEmpty { args.append("--restore-last-session") }
        if let address { args.append(address) }
        _ = try start(args)
        applications(profile).first?.activate()
    }

    func finish(_ token: String) {
        guard let login = logins.removeValue(forKey: token) else { return }
        login.expiry.cancel()
        if login.process.isRunning { login.process.terminate() }
        if currentLogin == token {
            currentLogin = logins.keys.sorted().first
            applications(activeDirectory).first?.activate()
        }
    }

    func ownsPoint(_ point: CGPoint, pid: pid_t, window: CGWindowID) throws -> Bool {
        try consoleUser()
        guard NSWorkspace.shared.frontmostApplication?.processIdentifier == pid, owns(pid) else { return false }
        var element: AXUIElement?
        guard AXUIElementCopyElementAtPosition(AXUIElementCreateSystemWide(), Float(point.x), Float(point.y), &element) == .success,
              let element else { return false }
        var owner: pid_t = 0
        guard AXUIElementGetPid(element, &owner) == .success, owner == pid else { return false }
        // AX hit testing disregards transparent system overlays but includes
        // focused Chrome controls; prevent a stale frame hitting another
        // top-level window of the same Chrome process.
        let windows = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String:Any]] ?? []
        let foremost = windows.first { info in
            guard info[kCGWindowOwnerPID as String] as? pid_t == pid, info[kCGWindowLayer as String] as? Int == 0,
                  let box = info[kCGWindowBounds as String] as? NSDictionary,
                  let rect = CGRect(dictionaryRepresentation: box) else { return false }
            return rect.contains(point)
        }
        return foremost?[kCGWindowNumber as String] as? CGWindowID == window
    }

    func window() async throws -> SCWindow {
        guard CGPreflightScreenCaptureAccess() else { throw Failure(6, "screen_recording_permission") }
        let apps = applications(activeDirectory)
        let pids = Set(apps.map(\.processIdentifier))
        guard !pids.isEmpty else { throw Failure(3, "not_running") }
        let content = try await SCShareableContent.excludingDesktopWindows(true, onScreenWindowsOnly: true)
        let candidates = content.windows.filter {
            $0.isOnScreen && $0.windowLayer == 0 && !$0.frame.isEmpty &&
            $0.owningApplication.map { $0.bundleIdentifier == bundleID && pids.contains($0.processID) } == true
        }
        // The WindowServer orders windows front to back. Login popups may be
        // smaller than the opener; the frontmost window owns the user's input.
        let stack = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String:Any]] ?? []
        let ordered = stack.compactMap { item -> SCWindow? in
            guard let id = item[kCGWindowNumber as String] as? CGWindowID else { return nil }
            return candidates.first { $0.windowID == id }
        }
        // The omnibox suggestion panel is ordered above the browser while
        // keyboard focus remains in the original Chrome window.
        let focused = ordered.first { window in
            guard let pid = window.owningApplication?.processID else { return false }
            return owningAXWindow(pid, window.frame) != nil
        }
        if let window = focused ?? ordered.first, let pid = window.owningApplication?.processID, owns(pid) { return window }
        throw Failure(7, "no_visible_window")
    }
}
