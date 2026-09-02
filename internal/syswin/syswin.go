package syswin

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type DiskSpace struct {
	Drive          string  `json:"drive"`
	FreeBytes      uint64  `json:"free_bytes"`
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsedPercent    float64 `json:"used_percent"`
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

type MemStats struct {
	AllocMB      float64 `json:"alloc_mb"`
	TotalAllocMB float64 `json:"total_alloc_mb"`
	SysMB        float64 `json:"sys_mb"`
	NumGC        uint32  `json:"num_gc"`
	Goroutines   int     `json:"goroutines"`
}

func GetMemStats() MemStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return MemStats{
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
