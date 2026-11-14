import CoreGraphics

// macOS 15 gives these Apple compositor surfaces full-display bounds even
// when their contents occupy only the Dock or notification banner area.
// The caller still verifies the foreground Settings/authorization app before
// and after every frame and input operation, including secure password focus.
func macMaintenanceCompositorSurface(bundle: String, appleSigned: Bool, layer: Int,
                                     bounds: CGRect, display: CGRect, systemMajor: Int) -> Bool {
    guard systemMajor == 15, appleSigned, !display.isEmpty,
          bounds == display else { return false }
    return bundle == "com.apple.dock" && layer == 20 ||
        bundle == "com.apple.notificationcenterui" && layer == 23
}
