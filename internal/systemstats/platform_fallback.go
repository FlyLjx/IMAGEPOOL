//go:build !linux

package systemstats

import "runtime"

type fallbackReader struct{}

func newPlatformReader() rawReader { return fallbackReader{} }

func (fallbackReader) read(diskPath string) rawSnapshot {
	return rawSnapshot{cores: runtime.NumCPU(), disk: DiskStats{Path: diskPath}}
}
