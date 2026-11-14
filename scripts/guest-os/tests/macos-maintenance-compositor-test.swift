import CoreGraphics

@main
struct MaintenanceCompositorTests {
    static func main() {
        let display = CGRect(x: 0, y: 0, width: 1600, height: 1000)
        func accepted(_ bundle: String, _ layer: Int, _ bounds: CGRect,
                      signed: Bool = true, major: Int = 15) -> Bool {
            macMaintenanceCompositorSurface(bundle: bundle, appleSigned: signed,
                                            layer: layer, bounds: bounds, display: display, systemMajor: major)
        }
        precondition(accepted("com.apple.dock", 20, display))
        precondition(accepted("com.apple.notificationcenterui", 23, display))
        precondition(!accepted("com.apple.dock", 20, display, signed: false))
        precondition(!accepted("com.apple.notificationcenterui", 23, display, signed: false))
        precondition(!accepted("com.example.notificationcenterui", 23, display))
        precondition(!accepted("com.apple.dock", 0, display))
        precondition(!accepted("com.apple.notificationcenterui", 0, display))
        precondition(!accepted("com.apple.dock", 20, CGRect(x: 0, y: 700, width: 1600, height: 300)))
        precondition(!accepted("com.apple.notificationcenterui", 23, display.offsetBy(dx: 1, dy: 0)))
        precondition(!accepted("com.apple.dock", 20, display, major: 26))
        print("macOS maintenance compositor checks passed")
    }
}
