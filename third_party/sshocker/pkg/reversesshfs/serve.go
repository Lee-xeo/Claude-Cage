package reversesshfs

import "io"

// ServeConfined serves only localPath over an already secured transport. It uses
// the same root and metadata protections as builtin reverse-sshfs mounts.
func ServeConfined(transport io.ReadWriteCloser, localPath string, readonly bool, readonlyNames []string) error {
	server, root, err := newRootedServer(transport, localPath, readonly, readonlyNames)
	if err != nil {
		return err
	}
	defer root.Close()
	defer server.Close()
	return server.Serve()
}

// Owner 是客端挂载时文件呈现成的属主，即 sshfs 的 -o uid=,gid=。
type Owner struct{ UID, GID uint32 }

// ServeConfinedAs 与 ServeConfined 相同，但客端把文件呈现为 owner 所有：把属主设为 owner
// 自身（或 -1 不变）的 chown 视为无需变更并成功返回，其余属性照常设置；改成其他 ID 仍照旧报错。
func ServeConfinedAs(transport io.ReadWriteCloser, localPath string, owner Owner, readonly bool, readonlyNames []string) error {
	server, root, err := newRootedServer(transport, localPath, readonly, readonlyNames)
	if err != nil {
		return err
	}
	root.presentAs(owner)
	defer root.Close()
	defer server.Close()
	return server.Serve()
}

// ServeConfinedVirtual exposes the selected directory at / without disclosing
// its host path. All reads, writes and Guard decisions use the same confined
// root and metadata protections as ServeConfined.
func ServeConfinedVirtual(transport io.ReadWriteCloser, localPath string, readonly bool, readonlyNames []string) error {
	server, root, err := newRootedServerAt(transport, localPath, "/", readonly, readonlyNames)
	if err != nil {
		return err
	}
	defer root.Close()
	defer server.Close()
	return server.Serve()
}
