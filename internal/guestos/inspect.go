// Package guestos checks host prerequisites for a local guest system.
package guestos

// Guest system IDs are shared with the workbench installer. MacOS and
// Windows11 name guests that are no longer installed; they remain so that
// installation records written by earlier versions can still be read.
const (
	Debian13  = "debian-13"
	MacOS     = "macos"
	Windows11 = "windows-11"
)

// Inspection keeps host capability separate from availability in the workbench.
type Inspection struct {
	Guest            string `json:"guest"`
	HostCheckPassed  bool   `json:"hostCheckPassed"`
	WorkbenchSupport string `json:"workbenchSupport"`
	Detail           string `json:"detail"`
}
