package workbench

import "strings"

// cliWorkspaceWithin reports whether a guest path is root or lies beneath it.
func cliWorkspaceWithin(path, root string) bool {
	return root != "" && (path == root || strings.HasPrefix(path, strings.TrimRight(root, "/")+"/"))
}
