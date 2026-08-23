package storage

import (
	"context"
	"time"
)

type ClusterHealthStatus struct {
	TotalNodes     int       `json:"total_nodes"`
	ActiveNodes    int       `json:"active_nodes"`
	HealthyPercent float64   `json:"healthy_percent"`
	LastScrubTime  int64     `json:"last_scrub_time"`
	Nodes          []BotNode `json:"nodes"`
}

type HealthScrubber struct {
	clusterManager *ClusterManager
	interval       time.Duration
	stopChan       chan struct{}
}

func NewHealthScrubber(cm *ClusterManager, interval time.Duration) *HealthScrubber {
	if interval < 30*time.Second {
		interval = 60 * time.Second
	}
	return &HealthScrubber{
		clusterManager: cm,
		interval:       interval,
		stopChan:       make(chan struct{}),
	}
}

func (s *HealthScrubber) Start(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	go func() {
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-s.stopChan:
				ticker.Stop()
				return
			case <-ticker.C:
				_ = s.clusterManager.CheckAllNodesHealth(ctx)
			}
		}
	}()
}

func (s *HealthScrubber) Stop() {
	close(s.stopChan)
}

func (s *HealthScrubber) GetHealthSummary(ctx context.Context) ClusterHealthStatus {
	nodes := s.clusterManager.CheckAllNodesHealth(ctx)
	activeCount := 0
	for _, n := range nodes {
		if n.Status == "Active" {
			activeCount++
		}
	}

	healthyPct := 0.0
	if len(nodes) > 0 {
		healthyPct = (float64(activeCount) / float64(len(nodes))) * 100.0
	}

	return ClusterHealthStatus{
		TotalNodes:     len(nodes),
		ActiveNodes:    activeCount,
		HealthyPercent: healthyPct,
		LastScrubTime:  time.Now().Unix(),
		Nodes:          nodes,
	}
}
