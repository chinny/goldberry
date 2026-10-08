//go:build linux

package sqlite

import "syscall"

// Filesystem magic numbers from statfs(2).
var networkMagic = map[int64]string{
	0x6969:     "nfs",
	0x517b:     "smb",
	0xfe534d42: "smb2",
	0xff534d42: "cifs",
	0x65735546: "fuse",
	0x01021997: "9p",
}

// networkFS reports whether dir sits on a network filesystem.
func networkFS(dir string) (string, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return "", false
	}
	name, ok := networkMagic[int64(st.Type)] //nolint:unconvert // Type is int32 on some linux arches
	return name, ok
}
