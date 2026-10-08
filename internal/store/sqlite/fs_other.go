//go:build !linux

package sqlite

func networkFS(string) (string, bool) { return "", false }
