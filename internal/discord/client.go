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
}

func NewClient(botToken string) *Client {
	return &Client{
		botToken: botToken,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        200,
				MaxIdleConnsPerHost: 50,
				IdleConnTimeout:     90 * time.Second,
				DisableKeepAlives:   false,
				ForceAttemptHTTP2:   true,
				WriteBufferSize:     64 * 1024,
				ReadBufferSize:      64 * 1024,
			},
		},
	}
}

func (c *Client) SetBotToken(token string) {
	c.botToken = token
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
			if bytes.Contains([]byte(url), []byte("?")) {
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
		req.Header.Set("User-Agent", "DiscordDrive/1.0")

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

func (c *Client) GetFreshAttachmentURL(ctx context.Context, channelID, messageID, attachmentID string) (string, error) {
	if channelID == "" || messageID == "" {
		return "", errors.New("channel and message id required")
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
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return "", err
		}

		req.Header.Set("Authorization", "Bot "+c.botToken)
		req.Header.Set("User-Agent", "DiscordDrive/1.0")

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
			if err := json.Unmarshal(respBody, &msg); err != nil {
				return "", fmt.Errorf("could not read message json %w", err)
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

		return "", fmt.Errorf("could not get message status %d", resp.StatusCode)
	}

	return "", errors.New("could not get fresh download link")
}

func (c *Client) DownloadChunkBytes(ctx context.Context, directURL string) ([]byte, error) {
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
		req.Header.Set("User-Agent", "DiscordDrive/1.0")

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
	c.globalLock.Wait()
	c.getLimiter(channelID).Wait()

	url := fmt.Sprintf("%s/channels/%s/messages/%s", DiscordAPIBase, channelID, messageID)
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+c.botToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("delete message returned status %d", resp.StatusCode)
}

func (c *Client) DeleteChannel(ctx context.Context, channelID string) error {
	if channelID == "" {
		return nil
	}
	c.globalLock.Wait()
	c.getLimiter(channelID).Wait()

	url := fmt.Sprintf("%s/channels/%s", DiscordAPIBase, channelID)
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+c.botToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("delete channel returned status %d", resp.StatusCode)
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

type AutoProvisionResult struct {
	CategoryID      string          `json:"category_id"`
	MetadataChannel Channel         `json:"metadata_channel"`
	StorageChannels []ProvisionItem `json:"storage_channels"`
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
		log.Printf("[DISCORD ERROR] Create category %s failed with status %d: %s", catName, resp.StatusCode, string(respBody))
		return "", fmt.Errorf("category setup returned %d", resp.StatusCode)
	}

	var category Channel
	_ = json.Unmarshal(respBody, &category)
	return category.ID, nil
}

func (c *Client) CreateChannelUnderCategory(ctx context.Context, guildID, categoryID, channelName, topic string) (*ProvisionItem, error) {
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

	whPayload := map[string]string{
		"name": fmt.Sprintf("%s Uploader", channelName),
	}
	whJSON, _ := json.Marshal(whPayload)

	reqWh, _ := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/channels/%s/webhooks", DiscordAPIBase, createdCh.ID), bytes.NewReader(whJSON))
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

	return &ProvisionItem{
		Channel: createdCh,
		Webhook: createdWh,
	}, nil
}

func (c *Client) CreateFileCategoryAndChannels(ctx context.Context, guildID, filename string, partCount int) (string, []ProvisionItem, error) {
	catID, err := c.FindOrCreateCategory(ctx, guildID, filename)
	if err != nil {
		log.Printf("[DISCORD WARN] Could not create category for %s: %v", filename, err)
	}

	var parts []ProvisionItem
	for i := 1; i <= partCount; i++ {
		chName := fmt.Sprintf("part %02d", i)
		prov, err := c.CreateChannelUnderCategory(ctx, guildID, catID, chName, fmt.Sprintf("Part %d for %s", i, filename))
		if err != nil {
			return catID, parts, err
		}
		parts = append(parts, *prov)
	}

	return catID, parts, nil
}

func (c *Client) CreateFileInFolderChannels(ctx context.Context, guildID, folderCategoryID, filename string, partCount int) ([]ProvisionItem, error) {
	cleanName := strings.ReplaceAll(filename, ".", " ")
	if len(cleanName) > 20 {
		cleanName = cleanName[:20]
	}

	var parts []ProvisionItem
	for i := 1; i <= partCount; i++ {
		chName := fmt.Sprintf("%s part %02d", cleanName, i)
		prov, err := c.CreateChannelUnderCategory(ctx, guildID, folderCategoryID, chName, fmt.Sprintf("Part %d for %s", i, filename))
		if err != nil {
			return parts, err
		}
		parts = append(parts, *prov)
	}

	return parts, nil
}

func (c *Client) AutoSetupServerWithToken(ctx context.Context, botToken, guildID string) (*AutoProvisionResult, error) {
	if botToken != "" {
		tempClient := NewClient(botToken)
		return tempClient.AutoSetupServer(ctx, guildID)
	}
	return c.AutoSetupServer(ctx, guildID)
}

func (c *Client) AutoSetupServer(ctx context.Context, guildID string) (*AutoProvisionResult, error) {
	if c.botToken == "" || guildID == "" {
		return nil, errors.New("bot token and server id needed")
	}

	getChReq, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/guilds/%s/channels", DiscordAPIBase, guildID), nil)
	if err != nil {
		return nil, err
	}
	getChReq.Header.Set("Authorization", "Bot "+c.botToken)

	resp, err := c.httpClient.Do(getChReq)
	if err != nil {
		return nil, fmt.Errorf("could not check discord channels %w", err)
	}
	chBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var existingChannels []Channel
	_ = json.Unmarshal(chBody, &existingChannels)

	var metaChannel *Channel
	var storageChannels []ProvisionItem

	for _, ch := range existingChannels {
		if ch.Type == 0 {
			normName := strings.ToLower(strings.ReplaceAll(ch.Name, "-", " "))
			if strings.Contains(normName, "metadata") || strings.Contains(normName, "catalog") {
				metaChannel = &ch
			} else if strings.HasPrefix(normName, "storage") || strings.HasPrefix(normName, "part") || strings.HasPrefix(normName, "shard") {
				prov, _ := c.CreateChannelUnderCategory(ctx, guildID, "", ch.Name, "Fast cloud storage shard")
				if prov != nil && prov.Webhook.URL != "" {
					storageChannels = append(storageChannels, *prov)
				}
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

		resp, err = c.httpClient.Do(req)
		if err == nil {
			metaResp, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var createdMeta Channel
			_ = json.Unmarshal(metaResp, &createdMeta)
			metaChannel = &createdMeta
		}
	}

	if len(storageChannels) < 4 {
		needed := 4 - len(storageChannels)
		startIndex := len(storageChannels) + 1
		for i := 0; i < needed; i++ {
			chName := fmt.Sprintf("storage %02d", startIndex+i)
			prov, err := c.CreateChannelUnderCategory(ctx, guildID, "", chName, "Fast cloud storage shard")
			if err == nil && prov != nil {
				storageChannels = append(storageChannels, *prov)
			}
		}
	}

	res := &AutoProvisionResult{
		StorageChannels: storageChannels,
	}
	if metaChannel != nil {
		res.MetadataChannel = *metaChannel
	}
	return res, nil
}

func (c *Client) AutoSetupMultiGuilds(ctx context.Context, guildIDs []string) (*AutoProvisionResult, error) {
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

		res, err := c.AutoSetupServer(ctx, gID)
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

	return &AutoProvisionResult{
		StorageChannels: allStorageChannels,
		MetadataChannel: metaChannel,
	}, nil
}
