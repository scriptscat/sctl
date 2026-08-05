//go:build !windows

package fsutil

func restrictFileAccess(string) error { return nil }
