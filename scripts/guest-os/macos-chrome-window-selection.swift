import Foundation
import CoreGraphics

struct MacChromeCaptureCandidate {
    let id: UInt32
    let frame: CGRect
    let applicationWindow: Bool
    let focused: Bool
}

// Chrome tooltips can be layer-zero windows owned by the browser process.
// Prefer real accessibility windows; attached menus remain in the parent's
// ScreenCaptureKit stream. Real login windows may be small and stay eligible.
func selectMacChromeCaptureWindow(_ candidates: [MacChromeCaptureCandidate]) -> UInt32? {
    let windows = candidates.filter(\.applicationWindow)
    if let focused = windows.first(where: \.focused) { return focused.id }
    if let first = windows.first { return first.id }
    // Screen recording may remain allowed after Accessibility is revoked.
    // In that case show the largest regular window, never a tiny hover tip.
    return candidates.filter { $0.frame.width >= 320 && $0.frame.height >= 240 }
        .max { $0.frame.width * $0.frame.height < $1.frame.width * $1.frame.height }?.id
}
