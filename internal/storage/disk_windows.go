//go:build windows

package storage

import "golang.org/x/sys/windows"

func diskUsage(path string) (total, used, free int64, err error) {
	root, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, 0, err
	}
	var available, capacity, totalFree uint64
	err = windows.GetDiskFreeSpaceEx(root, &available, &capacity, &totalFree)
	if err != nil {
		return 0, 0, 0, err
	}
	return int64(capacity), int64(capacity - available), int64(available), nil
}
