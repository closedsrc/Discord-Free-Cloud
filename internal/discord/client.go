package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"discord-free-cloud/internal/db"
)

const (
	DiscordAPIBase = "https://discord.com/api/v10"
	MaxRetries     = 5
)

var (
	ErrRateLimited = errors.New("discord rate limit reached please wait")
	ErrNotFound    = errors.New("discord item not found")
)

type RateLimitTracker struct {
	mu        sync.RWMutex
	blockedTo time.Time
}

func (r *RateLimitTracker) Wait() {
	r.mu.RLock()
	until := r.blockedTo
	r.mu.RUnlock()

	now := time.Now()
	if until.After(now) {
		time.Sleep(until.Sub(now))
	}
}

func (r *RateLimitTracker) Block(duration time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	target := time.Now().Add(duration)
	if target.After(r.blockedTo) {
		r.blockedTo = target
	}
}

type Client struct {
	botToken   string
	httpClient *http.Client
	limiters   sync.Map
	globalLock RateLimitTracker

	// category pool configuration
	poolPrefix   string
	baseCatsMu   sync.Mutex
	baseCats     map[string]string // guild id -> base category id ("" = auto-create)
}

func NewClient(botToken string) *Client {
	return &Client{
		botToken: botToken,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        300,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     120 * time.Second,
				DisableKeepAlives:   false,
				ForceAttemptHTTP2:   true,
				WriteBufferSize:     128 * 1024,
				ReadBufferSize:      128 * 1024,
			},
		},
		poolPrefix: "files",
		baseCats:   make(map[string]string),
	}
}

func (c *Client) SetBotToken(token string) {
	c.botToken = token
}

// SetPoolPrefix sets the category name prefix used when auto-creating
// storage categories (default "files").
func (c *Client) SetPoolPrefix(prefix string) {
	if strings.TrimSpace(prefix) != "" {
		c.poolPrefix = strings.TrimSpace(prefix)
	}
}

// SetBaseCategory pins the storage category for a guild. When empty the
// client auto-creates a category named PoolPrefix.
func (c *Client) SetBaseCategory(guildID, categoryID string) {
	c.baseCatsMu.Lock()
	defer c.baseCatsMu.Unlock()
	if categoryID == "" {
		delete(c.baseCats, guildID)
		return
	}
	c.baseCats[guildID] = categoryID
}

func (c *Client) baseCategoryFor(guildID string) string {
	c.baseCatsMu.Lock()
	defer c.baseCatsMu.Unlock()
	return c.baseCats[guildID]
}

// CloneForToken returns a copy of this client configured with a different bot
// token, carrying over the pool prefix and per-guild base category mapping.
func (c *Client) CloneForToken(token string) *Client {
	nc := NewClient(token)
	nc.poolPrefix = c.poolPrefix
	c.baseCatsMu.Lock()
	for g, id := range c.baseCats {
		nc.baseCats[g] = id
	}
	c.baseCatsMu.Unlock()
	return nc
}

func (c *Client) getLimiter(key string) *RateLimitTracker {
	val, _ := c.limiters.LoadOrStore(key, &RateLimitTracker{})
	return val.(*RateLimitTracker)
}

type rateLimitResponse struct {
	Message    string  `json:"message"`
	RetryAfter float64 `json:"retry_after"`
	Global     bool    `json:"global"`
}

type Message struct {
	ID          string       `json:"id"`
	ChannelID   string       `json:"channel_id"`
	Content     string       `json:"content"`
	Attachments []Attachment `json:"attachments"`
}

type Attachment struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	URL      string `json:"url"`
	ProxyURL string `json:"proxy_url"`
}

type Channel struct {
	ID       string `json:"id"`
	GuildID  string `json:"guild_id"`
	Name     string `json:"name"`
	Type     int    `json:"type"`
	ParentID string `json:"parent_id"`
}

type Webhook struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	Name      string `json:"name"`
	Token     string `json:"token"`
	URL       string `json:"url"`
}

type UploadChunkResult struct {
	MessageID     string
	AttachmentID  string
	AttachmentURL string
	ChannelID     string
}

func (c *Client) UploadChunk(ctx context.Context, channelID, webhookURL, filename string, chunkData []byte, metadataMsg string) (*UploadChunkResult, error) {
	var lastErr error

	for attempt := 0; attempt < MaxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		limiterKey := channelID
		if webhookURL != "" {
			limiterKey = webhookURL
		}

		c.globalLock.Wait()
		c.getLimiter(limiterKey).Wait()

		var body bytes.Buffer
		mw := multipart.NewWriter(&body)

		if err := mw.WriteField("content", metadataMsg); err != nil {
			return nil, fmt.Errorf("could not write message field %w", err)
		}

		part, err := mw.CreateFormFile("files[0]", filename)
		if err != nil {
			return nil, fmt.Errorf("could not create file part %w", err)
		}
		if _, err := part.Write(chunkData); err != nil {
			return nil, fmt.Errorf("could not write chunk data %w", err)
		}

		if err := mw.Close(); err != nil {
			return nil, fmt.Errorf("could not close multipart writer %w", err)
		}

		var req *http.Request
		if webhookURL != "" {
			url := webhookURL
			if strings.Contains(url, "?") {
				url += "&wait=true"
			} else {
				url += "?wait=true"
			}
			req, err = http.NewRequestWithContext(ctx, "POST", url, &body)
		} else {
			url := fmt.Sprintf("%s/channels/%s/messages", DiscordAPIBase, channelID)
			req, err = http.NewRequestWithContext(ctx, "POST", url, &body)
			if err == nil {
				req.Header.Set("Authorization", "Bot "+c.botToken)
			}
		}

		if err != nil {
			return nil, fmt.Errorf("could not build upload request %w", err)
		}

		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("User-Agent", "DiscordFreeCloud/1.0")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(100+rand.Intn(200)) * time.Millisecond)
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests {
			var rl rateLimitResponse
			_ = json.Unmarshal(respBody, &rl)

			backoffSec := rl.RetryAfter
			if backoffSec <= 0 {
				if val := resp.Header.Get("Retry-After"); val != "" {
					if f, err := strconv.ParseFloat(val, 64); err == nil {
						backoffSec = f
					}
				}
			}
			if backoffSec <= 0 {
				backoffSec = float64(attempt+1) * 1.5
			}

			jitter := time.Duration(50+rand.Intn(200)) * time.Millisecond
			sleepDuration := time.Duration(backoffSec*float64(time.Second)) + jitter

			if rl.Global {
				c.globalLock.Block(sleepDuration)
			} else {
				c.getLimiter(limiterKey).Block(sleepDuration)
			}

			lastErr = fmt.Errorf("rate limited waiting %.2f seconds", backoffSec)
			time.Sleep(sleepDuration)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var msg Message
			if err := json.Unmarshal(respBody, &msg); err != nil {
				return nil, fmt.Errorf("could not parse discord answer %w", err)
			}

			if len(msg.Attachments) == 0 {
				return nil, fmt.Errorf("upload finished but no file link came back")
			}

			resChannelID := msg.ChannelID
			if resChannelID == "" {
				resChannelID = channelID
			}

			return &UploadChunkResult{
				MessageID:     msg.ID,
				AttachmentID:  msg.Attachments[0].ID,
				AttachmentURL: msg.Attachments[0].URL,
				ChannelID:     resChannelID,
			}, nil
		}

		lastErr = fmt.Errorf("upload returned status %d", resp.StatusCode)
		if resp.StatusCode >= 500 {
			time.Sleep(time.Duration((attempt+1)*500) * time.Millisecond)
			continue
		}

		return nil, lastErr
	}

	if lastErr != nil {
		return nil, fmt.Errorf("%w %v", ErrRateLimited, lastErr)
	}
	return nil, ErrRateLimited
}

func (c *Client) AttachmentURL(ctx context.Context, channelID, messageID, attachmentID string) (string, error) {
	if channelID == "" {
		return "", errors.New("channel id required")
	}

	for attempt := 0; attempt < MaxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		c.globalLock.Wait()
		c.getLimiter(channelID).Wait()

		url := fmt.Sprintf("%s/channels/%s/messages/%s", DiscordAPIBase, channelID, messageID)
		if messageID == "" {
			url = fmt.Sprintf("%s/channels/%s/messages?limit=1", DiscordAPIBase, channelID)
		}
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return "", err
		}

		req.Header.Set("Authorization", "Bot "+c.botToken)
		req.Header.Set("User-Agent", "DiscordFreeCloud/1.0")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			time.Sleep(time.Duration(100+rand.Intn(200)) * time.Millisecond)
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests {
			var rl rateLimitResponse
			_ = json.Unmarshal(respBody, &rl)
			backoffSec := rl.RetryAfter
			if backoffSec <= 0 {
				backoffSec = float64(attempt+1) * 1.5
			}
			sleepDuration := time.Duration(backoffSec*float64(time.Second)) + time.Duration(50+rand.Intn(150))*time.Millisecond
			if rl.Global {
				c.globalLock.Block(sleepDuration)
			} else {
				c.getLimiter(channelID).Block(sleepDuration)
			}
			time.Sleep(sleepDuration)
			continue
		}

		if resp.StatusCode == http.StatusNotFound {
			return "", ErrNotFound
		}

		if resp.StatusCode == http.StatusOK {
			var msg Message
			if messageID == "" {
				var msgs []Message
				if err := json.Unmarshal(respBody, &msgs); err != nil {
					return "", fmt.Errorf("could not read messages json %w", err)
				}
				if len(msgs) == 0 {
					return "", ErrNotFound
				}
				msg = msgs[0]
			} else {
				if err := json.Unmarshal(respBody, &msg); err != nil {
					return "", fmt.Errorf("could not read message json %w", err)
				}
			}

			for _, att := range msg.Attachments {
				if attachmentID == "" || att.ID == attachmentID {
					return att.URL, nil
				}
			}

			if len(msg.Attachments) > 0 {
				return msg.Attachments[0].URL, nil
			}
			return "", errors.New("message has no files attached")
		}

		if resp.StatusCode >= 500 {
			time.Sleep(time.Duration((attempt+1)*300) * time.Millisecond)
			continue
		}

		return "", fmt.Errorf("discord returned status %d: %s", resp.StatusCode, string(respBody))
	}
	return "", ErrRateLimited
}

func (c *Client) DownloadChunk(ctx context.Context, directURL string) ([]byte, error) {
	for attempt := 0; attempt < MaxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		req, err := http.NewRequestWithContext(ctx, "GET", directURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "DiscordFreeCloud/1.0")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			time.Sleep(time.Duration(100+rand.Intn(200)) * time.Millisecond)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			data, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				return nil, fmt.Errorf("could not read file bytes %w", err)
			}
			return data, nil
		}

		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			time.Sleep(time.Duration((attempt+1)*300) * time.Millisecond)
			continue
		}

		return nil, fmt.Errorf("chunk download failed status %d", resp.StatusCode)
	}

	return nil, errors.New("could not download file bytes")
}

func (c *Client) DeleteMessage(ctx context.Context, channelID, messageID string) error {
	if channelID == "" || messageID == "" {
		return nil
	}

	for attempt := 0; attempt < MaxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		c.globalLock.Wait()
		c.getLimiter(channelID).Wait()

		url := fmt.Sprintf("%s/channels/%s/messages/%s", DiscordAPIBase, channelID, messageID)
		req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bot "+c.botToken)
		req.Header.Set("User-Agent", "DiscordFreeCloud/1.0")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			time.Sleep(time.Duration(100+rand.Intn(200)) * time.Millisecond)
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
			return nil
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			var rl rateLimitResponse
			_ = json.Unmarshal(respBody, &rl)
			backoffSec := rl.RetryAfter
			if backoffSec <= 0 {
				if val := resp.Header.Get("Retry-After"); val != "" {
					if f, err := strconv.ParseFloat(val, 64); err == nil {
						backoffSec = f
					}
				}
			}
			if backoffSec <= 0 {
				backoffSec = float64(attempt+1) * 1.5
			}

			jitter := time.Duration(50+rand.Intn(200)) * time.Millisecond
			sleepDuration := time.Duration(backoffSec*float64(time.Second)) + jitter

			if rl.Global {
				c.globalLock.Block(sleepDuration)
			} else {
				c.getLimiter(channelID).Block(sleepDuration)
			}

			time.Sleep(sleepDuration)
			continue
		}

		if resp.StatusCode >= 500 {
			time.Sleep(time.Duration((attempt+1)*300) * time.Millisecond)
			continue
		}

		return fmt.Errorf("delete message returned status %d", resp.StatusCode)
	}

	return fmt.Errorf("delete message failed after %d retries", MaxRetries)
}

func (c *Client) DeleteChannel(ctx context.Context, channelID string) error {
	if channelID == "" {
		return nil
	}

	for attempt := 0; attempt < MaxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		c.globalLock.Wait()
		c.getLimiter(channelID).Wait()

		url := fmt.Sprintf("%s/channels/%s", DiscordAPIBase, channelID)
		req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bot "+c.botToken)
		req.Header.Set("User-Agent", "DiscordFreeCloud/1.0")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			time.Sleep(time.Duration(100+rand.Intn(200)) * time.Millisecond)
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
			return nil
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			var rl rateLimitResponse
			_ = json.Unmarshal(respBody, &rl)
			backoffSec := rl.RetryAfter
			if backoffSec <= 0 {
				if val := resp.Header.Get("Retry-After"); val != "" {
					if f, err := strconv.ParseFloat(val, 64); err == nil {
						backoffSec = f
					}
				}
			}
			if backoffSec <= 0 {
				backoffSec = float64(attempt+1) * 1.5
			}

			jitter := time.Duration(50+rand.Intn(200)) * time.Millisecond
			sleepDuration := time.Duration(backoffSec*float64(time.Second)) + jitter

			if rl.Global {
				c.globalLock.Block(sleepDuration)
			} else {
				c.getLimiter(channelID).Block(sleepDuration)
			}

			time.Sleep(sleepDuration)
			continue
		}

		if resp.StatusCode >= 500 {
			time.Sleep(time.Duration((attempt+1)*300) * time.Millisecond)
			continue
		}

		return fmt.Errorf("delete channel returned status %d", resp.StatusCode)
	}

	return fmt.Errorf("delete channel failed after %d retries", MaxRetries)
}

func (c *Client) GetChannelParentID(ctx context.Context, channelID string) (string, error) {
	if channelID == "" {
		return "", nil
	}
	c.globalLock.Wait()
	c.getLimiter(channelID).Wait()

	url := fmt.Sprintf("%s/channels/%s", DiscordAPIBase, channelID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bot "+c.botToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("channel fetch status %d", resp.StatusCode)
	}

	var ch Channel
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return "", err
	}
	return ch.ParentID, nil
}

func (c *Client) CleanExistingStorageChannels(ctx context.Context, guildID string) (int, error) {
	if c.botToken == "" || guildID == "" {
		return 0, errors.New("bot token and server id needed")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/guilds/%s/channels", DiscordAPIBase, guildID), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bot "+c.botToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var channels []Channel
	_ = json.Unmarshal(body, &channels)

	deletedCount := 0
	var categoriesToDelete []string

	for _, ch := range channels {
		normName := strings.ToLower(strings.ReplaceAll(ch.Name, "-", " "))
		if strings.EqualFold(ch.Name, "general") || strings.Contains(normName, "metadata") || strings.Contains(normName, "catalog") {
			continue
		}

		if ch.Type == 0 {
			if strings.Contains(normName, "part") || strings.Contains(normName, "shard") || strings.Contains(normName, "chunk") || strings.Contains(normName, "storage") || ch.ParentID != "" {
				_ = c.DeleteChannel(ctx, ch.ID)
				deletedCount++
				time.Sleep(100 * time.Millisecond)
			}
		} else if ch.Type == 4 {
			categoriesToDelete = append(categoriesToDelete, ch.ID)
		}
	}

	for _, catID := range categoriesToDelete {
		_ = c.DeleteChannel(ctx, catID)
		deletedCount++
		time.Sleep(100 * time.Millisecond)
	}

	return deletedCount, nil
}

type SetupResult struct {
	StorageChannels []ProvisionItem
	MetadataChannel Channel
}

type ProvisionItem struct {
	Channel Channel `json:"channel"`
	Webhook Webhook `json:"webhook"`
}

func (c *Client) FindOrCreateCategory(ctx context.Context, guildID, categoryName string) (string, error) {
	if c.botToken == "" || guildID == "" {
		return "", errors.New("bot token and server id needed")
	}

	catName := strings.TrimSpace(categoryName)
	if len(catName) > 90 {
		catName = catName[:90]
	}

	reqList, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/guilds/%s/channels", DiscordAPIBase, guildID), nil)
	if err == nil {
		reqList.Header.Set("Authorization", "Bot "+c.botToken)
		if respList, err := c.httpClient.Do(reqList); err == nil {
			var channels []Channel
			_ = json.NewDecoder(respList.Body).Decode(&channels)
			respList.Body.Close()

			for _, ch := range channels {
				if ch.Type == 4 && strings.EqualFold(ch.Name, catName) {
					return ch.ID, nil
				}
			}
		}
	}

	categoryPayload := map[string]any{
		"name": catName,
		"type": 4,
	}
	categoryJSON, _ := json.Marshal(categoryPayload)

	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/guilds/%s/channels", DiscordAPIBase, guildID), bytes.NewReader(categoryJSON))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bot "+c.botToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not make category %w", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		log.Printf("create category %s failed with status %d: %s", catName, resp.StatusCode, string(respBody))
		return "", fmt.Errorf("category setup returned %d", resp.StatusCode)
	}

	var category Channel
	_ = json.Unmarshal(respBody, &category)
	return category.ID, nil
}

func (c *Client) CreateChannel(ctx context.Context, guildID, categoryID, channelName, topic string) (*ProvisionItem, error) {
	chPayload := map[string]any{
		"name":  channelName,
		"type":  0,
		"topic": topic,
	}
	if categoryID != "" {
		chPayload["parent_id"] = categoryID
	}
	chJSON, _ := json.Marshal(chPayload)

	reqCh, _ := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/guilds/%s/channels", DiscordAPIBase, guildID), bytes.NewReader(chJSON))
	reqCh.Header.Set("Authorization", "Bot "+c.botToken)
	reqCh.Header.Set("Content-Type", "application/json")

	respCh, err := c.httpClient.Do(reqCh)
	if err != nil {
		return nil, fmt.Errorf("could not make channel %s %w", channelName, err)
	}
	chRespBody, _ := io.ReadAll(respCh.Body)
	respCh.Body.Close()

	var createdCh Channel
	_ = json.Unmarshal(chRespBody, &createdCh)

	time.Sleep(100 * time.Millisecond)
	createdWh, err := c.CreateWebhook(ctx, createdCh.ID, channelName)
	if err != nil {
		return nil, err
	}

	return &ProvisionItem{
		Channel: createdCh,
		Webhook: *createdWh,
	}, nil
}

func (c *Client) CreateWebhook(ctx context.Context, channelID, channelName string) (*Webhook, error) {
	whPayload := map[string]string{
		"name": fmt.Sprintf("%s Uploader", channelName),
	}
	whJSON, _ := json.Marshal(whPayload)

	reqWh, _ := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/channels/%s/webhooks", DiscordAPIBase, channelID), bytes.NewReader(whJSON))
	reqWh.Header.Set("Authorization", "Bot "+c.botToken)
	reqWh.Header.Set("Content-Type", "application/json")

	respWh, err := c.httpClient.Do(reqWh)
	if err != nil {
		return nil, fmt.Errorf("could not make webhook for %s %w", channelName, err)
	}
	whRespBody, _ := io.ReadAll(respWh.Body)
	respWh.Body.Close()

	var createdWh Webhook
	_ = json.Unmarshal(whRespBody, &createdWh)
	return &createdWh, nil
}

func (c *Client) SetupChannels(ctx context.Context, guildID, filename string, partCount int) (string, []ProvisionItem, error) {
	catID, err := c.FindOrCreateCategory(ctx, guildID, filename)
	if err != nil {
		log.Printf("could not create category for %s: %v", filename, err)
	}

	var parts []ProvisionItem
	for i := 1; i <= partCount; i++ {
		chName := fmt.Sprintf("part %02d", i)
		prov, err := c.CreateChannel(ctx, guildID, catID, chName, fmt.Sprintf("Part %d for %s", i, filename))
		if err != nil {
			return catID, parts, err
		}
		parts = append(parts, *prov)
	}

	return catID, parts, nil
}

func (c *Client) SetupFolderChannels(ctx context.Context, guildID, folderCategoryID, filename string, partCount int) ([]ProvisionItem, error) {
	cleanName := strings.ReplaceAll(filename, ".", " ")
	if len(cleanName) > 20 {
		cleanName = cleanName[:20]
	}

	var parts []ProvisionItem
	for i := 1; i <= partCount; i++ {
		chName := fmt.Sprintf("%s part %02d", cleanName, i)
		prov, err := c.CreateChannel(ctx, guildID, folderCategoryID, chName, fmt.Sprintf("Part %d for %s", i, filename))
		if err != nil {
			return parts, err
		}
		parts = append(parts, *prov)
	}

	return parts, nil
}

func (c *Client) SetupServerWithToken(ctx context.Context, botToken, guildID string) (*SetupResult, error) {
	if botToken != "" {
		tempClient := NewClient(botToken)
		return tempClient.SetupServer(ctx, guildID)
	}
	return c.SetupServer(ctx, guildID)
}

func (c *Client) SetupServer(ctx context.Context, guildID string) (*SetupResult, error) {
	if c.botToken == "" || guildID == "" {
		return nil, errors.New("bot token and server id needed")
	}

	channels, err := c.listGuildChannels(ctx, guildID)
	if err != nil {
		return nil, err
	}

	var metaChannel *Channel
	for _, ch := range channels {
		if ch.Type == 0 {
			normName := strings.ToLower(strings.ReplaceAll(ch.Name, "-", " "))
			if strings.Contains(normName, "metadata") || strings.Contains(normName, "catalog") {
				metaChannel = &ch
				break
			}
		}
	}

	if metaChannel == nil {
		metaPayload := map[string]any{
			"name":  "metadata catalog",
			"type":  0,
			"topic": "Cloud backup snapshots for your files",
		}
		metaJSON, _ := json.Marshal(metaPayload)

		req, _ := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/guilds/%s/channels", DiscordAPIBase, guildID), bytes.NewReader(metaJSON))
		req.Header.Set("Authorization", "Bot "+c.botToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err == nil {
			metaResp, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var createdMeta Channel
			_ = json.Unmarshal(metaResp, &createdMeta)
			metaChannel = &createdMeta
		}
	}

	// Storage channels live inside categories (base category or auto-created
	// "files"), each with its own webhook, spilling into new categories as the
	// pool grows. See SyncPool.
	storageChannels, err := c.SyncPool(ctx, guildID, DefaultPoolTarget)
	if err != nil {
		return nil, err
	}

	res := &SetupResult{
		StorageChannels: storageChannels,
	}
	if metaChannel != nil {
		res.MetadataChannel = *metaChannel
	}
	return res, nil
}

func (c *Client) SetupGuilds(ctx context.Context, guildIDs []string) (*SetupResult, error) {
	if len(guildIDs) == 0 {
		return nil, errors.New("at least one server id is needed")
	}

	var allStorageChannels []ProvisionItem
	var metaChannel Channel

	for i, gID := range guildIDs {
		gID = strings.TrimSpace(gID)
		if gID == "" {
			continue
		}

		res, err := c.SetupServer(ctx, gID)
		if err == nil && res != nil {
			allStorageChannels = append(allStorageChannels, res.StorageChannels...)
			if (metaChannel.ID == "" || i == 0) && res.MetadataChannel.ID != "" {
				metaChannel = res.MetadataChannel
			}
		}
	}

	if len(allStorageChannels) == 0 {
		return nil, errors.New("could not setup storage channels on the configured servers")
	}

	return &SetupResult{
		StorageChannels: allStorageChannels,
		MetadataChannel: metaChannel,
	}, nil
}

type BotDetails struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Avatar    string `json:"avatar"`
	InviteURL string `json:"invite_url"`
}

type GuildItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon"`
}

func (c *Client) GetBotDetails(ctx context.Context, botToken string) (*BotDetails, error) {
	botToken = strings.TrimSpace(botToken)
	if botToken == "" {
		botToken = c.botToken
	}
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/users/@me", DiscordAPIBase), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bot "+botToken)
	req.Header.Set("User-Agent", "DiscordFreeCloud/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errors.New("invalid bot token (401 Unauthorized). Please check that you copied the full token without spaces")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bot authentication returned HTTP %d", resp.StatusCode)
	}

	var res struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Avatar   string `json:"avatar"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	inviteURL := fmt.Sprintf("https://discord.com/oauth2/authorize?client_id=%s&scope=bot%%20applications.commands&permissions=8", res.ID)

	return &BotDetails{
		ID:        res.ID,
		Username:  res.Username,
		Avatar:    res.Avatar,
		InviteURL: inviteURL,
	}, nil
}

func (c *Client) GetBotGuilds(ctx context.Context, botToken string) ([]GuildItem, error) {
	botToken = strings.TrimSpace(botToken)
	if botToken == "" {
		botToken = c.botToken
	}
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/users/@me/guilds", DiscordAPIBase), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bot "+botToken)
	req.Header.Set("User-Agent", "DiscordFreeCloud/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errors.New("invalid bot token (401 Unauthorized)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("could not fetch bot guilds: HTTP %d", resp.StatusCode)
	}

	var guilds []GuildItem
	if err := json.NewDecoder(resp.Body).Decode(&guilds); err != nil {
		return nil, err
	}
	return guilds, nil
}

func (c *Client) BotInfo(ctx context.Context, botToken string) (string, error) {
	details, err := c.GetBotDetails(ctx, botToken)
	if err != nil {
		return "", err
	}
	return details.Username, nil
}

func (c *Client) GuildInfo(ctx context.Context, botToken, guildID string) (string, int, error) {
	botToken = strings.TrimSpace(botToken)
	guildID = strings.TrimSpace(guildID)
	if botToken == "" {
		botToken = c.botToken
	}
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/guilds/%s", DiscordAPIBase, guildID), nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bot "+botToken)
	req.Header.Set("User-Agent", "DiscordFreeCloud/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", 0, errors.New("bot is not in this server (404 Not Found) or Server ID is invalid. Make sure your bot is invited to your Discord server first")
	}
	if resp.StatusCode == http.StatusForbidden {
		return "", 0, errors.New("bot lacks permission (403 Forbidden). Ensure the bot has 'Administrator' or 'Manage Channels' permission in that server")
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return "", 0, errors.New("invalid bot token (401 Unauthorized)")
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("server query returned HTTP %d", resp.StatusCode)
	}

	var guildInfo struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&guildInfo)

	chReq, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/guilds/%s/channels", DiscordAPIBase, guildID), nil)
	chReq.Header.Set("Authorization", "Bot "+botToken)
	chReq.Header.Set("User-Agent", "DiscordFreeCloud/1.0")

	chCount := 0
	if chResp, chErr := c.httpClient.Do(chReq); chErr == nil {
		var channels []any
		_ = json.NewDecoder(chResp.Body).Decode(&channels)
		chResp.Body.Close()
		chCount = len(channels)
	}

	return guildInfo.Name, chCount, nil
}

func (c *Client) VerifyNode(ctx context.Context, botToken, guildID string) (*db.BotNodeRecord, error) {
	botToken = strings.TrimSpace(botToken)
	guildID = strings.TrimSpace(guildID)
	if botToken == "" || guildID == "" {
		return nil, errors.New("bot token and server ID are required")
	}

	start := time.Now()
	botName, err := c.BotInfo(ctx, botToken)
	if err != nil {
		return nil, fmt.Errorf("could not verify bot token: %w", err)
	}

	guildName, chCount, err := c.GuildInfo(ctx, botToken, guildID)
	if err != nil {
		return nil, err
	}

	ping := time.Since(start).Milliseconds()

	return &db.BotNodeRecord{
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
	}, nil
}

func (c *Client) CheckNodes(ctx context.Context, nodes []db.BotNodeRecord) []db.BotNodeRecord {
	updated := make([]db.BotNodeRecord, len(nodes))
	copy(updated, nodes)

	var wg sync.WaitGroup
	for i := range updated {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			start := time.Now()
			botName, err := c.BotInfo(ctx, updated[idx].BotToken)
			if err == nil {
				updated[idx].BotName = botName
				updated[idx].Status = "Active"
				updated[idx].PingMs = time.Since(start).Milliseconds()
				updated[idx].LastSeen = time.Now().Unix()

				gName, chCount, gErr := c.GuildInfo(ctx, updated[idx].BotToken, updated[idx].GuildID)
				if gErr == nil && gName != "" {
					updated[idx].GuildName = gName
					updated[idx].ChannelCount = chCount
				}
			} else {
				updated[idx].Status = "Unreachable"
				updated[idx].PingMs = 999
			}
		}(i)
	}
	wg.Wait()
	return updated
}
