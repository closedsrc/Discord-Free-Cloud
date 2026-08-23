package syswin

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modKernel32            = windows.NewLazySystemDLL("kernel32.dll")
	procGetDiskFreeSpaceExW = modKernel32.NewProc("GetDiskFreeSpaceExW")
)

type DiskSpace struct {
	Drive          string  `json:"drive"`
	FreeBytes      uint64  `json:"free_bytes"`
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsedPercent    float64 `json:"used_percent"`
}

func GetDiskFreeSpace(path string) (*DiskSpace, error) {
	if runtime.GOOS != "windows" {
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

func NormalizeLongPath(path string) string {
	cleaned := filepath.Clean(path)
	if runtime.GOOS != "windows" {
		return cleaned
	}
	if strings.HasPrefix(cleaned, `\\?\`) || strings.HasPrefix(cleaned, `\\.\`) {
		return cleaned
	}
	if filepath.IsAbs(cleaned) {
		return `\\?\` + cleaned
	}
	return cleaned
}

type MemoryTelemetry struct {
	AllocMB      float64 `json:"alloc_mb"`
	TotalAllocMB float64 `json:"total_alloc_mb"`
	SysMB        float64 `json:"sys_mb"`
	NumGC        uint32  `json:"num_gc"`
	Goroutines   int     `json:"goroutines"`
}

func GetMemoryTelemetry() MemoryTelemetry {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return MemoryTelemetry{
		AllocMB:      float64(m.Alloc) / (1024 * 1024),
		TotalAllocMB: float64(m.TotalAlloc) / (1024 * 1024),
		SysMB:        float64(m.Sys) / (1024 * 1024),
		NumGC:        m.NumGC,
		Goroutines:   runtime.NumGoroutine(),
	}
}

func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	} else {
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
