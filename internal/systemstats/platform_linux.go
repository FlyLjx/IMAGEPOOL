//go:build linux

package systemstats

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

type linuxReader struct{}

func newPlatformReader() rawReader { return linuxReader{} }

func (linuxReader) read(diskPath string) rawSnapshot {
	result := rawSnapshot{cores: runtime.NumCPU(), disk: readDiskStats(diskPath)}
	if data, err := os.ReadFile("/proc/stat"); err == nil {
		result.cpu = parseProcStat(data)
	}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		result.load1, result.load5, result.load15 = parseLoadAvg(data)
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		result.memory = parseMemInfo(data)
	}
	if data, err := os.ReadFile("/proc/net/dev"); err == nil {
		result.network = parseNetDev(data)
	}
	return result
}

func readDiskStats(path string) DiskStats {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "/"
	}
	result := DiskStats{Path: path}
	candidate := path
	if absolute, err := filepath.Abs(candidate); err == nil {
		candidate = absolute
	}
	for {
		var stat unix.Statfs_t
		if err := unix.Statfs(candidate, &stat); err == nil {
			blockSize := uint64(stat.Bsize)
			result.TotalBytes = uint64(stat.Blocks) * blockSize
			freeBytes := uint64(stat.Bfree) * blockSize
			result.AvailableBytes = uint64(stat.Bavail) * blockSize
			if freeBytes <= result.TotalBytes {
				result.UsedBytes = result.TotalBytes - freeBytes
			}
			return result
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return result
		}
		candidate = parent
	}
}
