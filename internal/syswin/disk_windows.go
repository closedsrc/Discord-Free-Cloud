//go:build windows

package syswin

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modKernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procGetDiskFreeSpaceExW = modKernel32.NewProc("GetDiskFreeSpaceExW")
)

func GetDiskFreeSpace(path string) (*DiskSpace, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	volumeRoot := filepath.VolumeName(absPath)
	if volumeRoot == "" {
		volumeRoot = "C:"
	}
	driveName := volumeRoot
	if !strings.HasSuffix(volumeRoot, `\`) {
		volumeRoot += `\`
	}

	utf16Path, err := syscall.UTF16PtrFromString(volumeRoot)
	if err != nil {
		return nil, fmt.Errorf("path conversion failed %w", err)
	}

	var freeBytesAvailable uint64
	var totalNumberOfBytes uint64
	var totalNumberOfFreeBytes uint64

	r1, _, lastErr := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(utf16Path)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalNumberOfBytes)),
		uintptr(unsafe.Pointer(&totalNumberOfFreeBytes)),
	)

	if r1 == 0 {
		return nil, fmt.Errorf("could not check disk space on %s %v", volumeRoot, lastErr)
	}

	var usedBytes uint64 = 0
	if totalNumberOfBytes >= totalNumberOfFreeBytes {
		usedBytes = totalNumberOfBytes - totalNumberOfFreeBytes
	}

	var usedPercent float64 = 0
	if totalNumberOfBytes > 0 {
		usedPercent = (float64(usedBytes) / float64(totalNumberOfBytes)) * 100.0
	}

	return &DiskSpace{
		Drive:          driveName,
		FreeBytes:      totalNumberOfFreeBytes,
		TotalBytes:     totalNumberOfBytes,
		UsedBytes:      usedBytes,
		AvailableBytes: freeBytesAvailable,
		UsedPercent:    usedPercent,
	}, nil
}