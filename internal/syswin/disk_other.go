//go:build !windows

package syswin

func GetDiskFreeSpace(path string) (*DiskSpace, error) {
	total := uint64(1024 * 1024 * 1024 * 500)
	free := uint64(1024 * 1024 * 1024 * 150)
	used := total - free
	return &DiskSpace{
		Drive:          "/",
		FreeBytes:      free,
		TotalBytes:     total,
		UsedBytes:      used,
		AvailableBytes: free,
		UsedPercent:    float64(used) / float64(total) * 100.0,
	}, nil
}