//go:build !windows

package storage

import "golang.org/x/sys/unix"

func diskUsage(path string) (total, used, free int64, err error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, 0, 0, err
	}
	blockSize := int64(stat.Bsize)
	total = int64(stat.Blocks) * blockSize
	free = int64(stat.Bavail) * blockSize
	used = total - int64(stat.Bfree)*blockSize
	return total, used, free, nil
}
