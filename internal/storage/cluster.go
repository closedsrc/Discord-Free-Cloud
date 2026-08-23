package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"discord-free-cloud/internal/discord"
)

type BotNode struct {
	ID           string `json:"id"`
	BotToken     string `json:"bot_token"`
	GuildID      string `json:"guild_id"`
	BotName      string `json:"bot_name"`
	GuildName    string `json:"guild_name"`
	Status       string `json:"status"`
	PingMs       int64  `json:"ping_ms"`
	ChannelCount int    `json:"channel_count"`
	StorageBytes int64  `json:"storage_bytes"`
	CreatedAt    int64  `json:"created_at"`
	LastSeen     int64  `json:"last_seen"`
}

type ClusterManager struct {
	mu         sync.RWMutex
	nodes      []BotNode
	httpClient *http.Client
}

func NewClusterManager() *ClusterManager {
	return &ClusterManager{
		nodes: make([]BotNode, 0),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (cm *ClusterManager) VerifyNode(ctx context.Context, botToken, guildID string) (*BotNode, error) {
	if botToken == "" || guildID == "" {
		return nil, errors.New("bot token and server id are required")
	}

	start := time.Now()
	botName, err := cm.FetchBotInfo(ctx, botToken)
	if err != nil {
		return nil, fmt.Errorf("could not verify bot token %w", err)
	}

	guildName, chCount, err := cm.FetchGuildInfo(ctx, botToken, guildID)
	if err != nil {
		return nil, fmt.Errorf("could not verify server id %w", err)
	}

	ping := time.Since(start).Milliseconds()

	node := &BotNode{
		ID:           guildID,
		BotToken:     botToken,
		GuildID:      guildID,
		BotName:      botName,
		GuildName:    guildName,
		Status:       "Active",
		PingMs:       ping,
		ChannelCount: chCount,
		CreatedAt:    time.Now().Unix(),
		LastSeen:     time.Now().Unix(),
	}

	return node, nil
}

func (cm *ClusterManager) FetchBotInfo(ctx context.Context, botToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/users/@me", discord.DiscordAPIBase), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bot "+botToken)

	resp, err := cm.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bot authentication returned %d", resp.StatusCode)
	}

	var res struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}

	return res.Username, nil
}

func (cm *ClusterManager) FetchGuildInfo(ctx context.Context, botToken, guildID string) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/guilds/%s", discord.DiscordAPIBase, guildID), nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bot "+botToken)

	resp, err := cm.httpClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("server query returned %d", resp.StatusCode)
	}

	var guildInfo struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&guildInfo)

	chReq, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/guilds/%s/channels", discord.DiscordAPIBase, guildID), nil)
	chReq.Header.Set("Authorization", "Bot "+botToken)

	chCount := 0
	chResp, chErr := cm.httpClient.Do(chReq)
	if chErr == nil {
		var channels []any
		_ = json.NewDecoder(chResp.Body).Decode(&channels)
		chResp.Body.Close()
		chCount = len(channels)
	}

	return guildInfo.Name, chCount, nil
}

func (cm *ClusterManager) SetNodes(nodes []BotNode) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.nodes = nodes
}

func (cm *ClusterManager) GetNodes() []BotNode {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	copied := make([]BotNode, len(cm.nodes))
	copy(copied, cm.nodes)
	return copied
}

func (cm *ClusterManager) CheckAllNodesHealth(ctx context.Context) []BotNode {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	var updated []BotNode
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, n := range cm.nodes {
		wg.Add(1)
		go func(node BotNode) {
			defer wg.Done()
			start := time.Now()
			botName, err := cm.FetchBotInfo(ctx, node.BotToken)
			if err == nil {
				node.BotName = botName
				node.Status = "Active"
				node.PingMs = time.Since(start).Milliseconds()
				node.LastSeen = time.Now().Unix()

				gName, chCount, gErr := cm.FetchGuildInfo(ctx, node.BotToken, node.GuildID)
				if gErr == nil && gName != "" {
					node.GuildName = gName
					node.ChannelCount = chCount
				}
			} else {
				node.Status = "Unreachable"
				node.PingMs = 999
			}

			mu.Lock()
			updated = append(updated, node)
			mu.Unlock()
		}(n)
	}

	wg.Wait()
	cm.nodes = updated
	return updated
}
