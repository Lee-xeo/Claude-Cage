import Foundation

@main
struct ChromeWindowSelectionTest {
    static func main() {
        let main = MacChromeCaptureCandidate(id: 1, frame: CGRect(x: 0, y: 0, width: 1100, height: 700), applicationWindow: true, focused: false)
        let tooltip = MacChromeCaptureCandidate(id: 2, frame: CGRect(x: 800, y: 20, width: 260, height: 80), applicationWindow: false, focused: false)
        precondition(selectMacChromeCaptureWindow([tooltip, main]) == 1)
        let login = MacChromeCaptureCandidate(id: 3, frame: CGRect(x: 30, y: 30, width: 280, height: 220), applicationWindow: true, focused: true)
        precondition(selectMacChromeCaptureWindow([tooltip, login, main]) == 3)
        let background = MacChromeCaptureCandidate(id: 4, frame: CGRect(x: 40, y: 40, width: 600, height: 400), applicationWindow: true, focused: false)
        precondition(selectMacChromeCaptureWindow([tooltip, background, main]) == 4)
        let noAccessibility = MacChromeCaptureCandidate(id: 1, frame: main.frame, applicationWindow: false, focused: false)
        precondition(selectMacChromeCaptureWindow([tooltip, noAccessibility]) == 1)
        precondition(selectMacChromeCaptureWindow([tooltip]) == nil)
        precondition(selectMacChromeCaptureWindow([]) == nil)
        print("Chrome tooltip, login popup, background window and revoked Accessibility selection passed.")
    }
}
