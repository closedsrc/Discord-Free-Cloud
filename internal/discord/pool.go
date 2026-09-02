package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// MaxChannelsPerCategory caps how many storage channels we place in one
	// category before spilling into a fresh "<prefix> N" category.
	MaxChannelsPerCategory = 40
	// DefaultPoolTarget is the starting pool size per server when no target
	// is supplied.
	DefaultPoolTarget = 8
	// MaxPoolTarget guards against runaway growth (server-wide channel cap).
	MaxPoolTarget = 200
)

func (c *Client) listGuildChannels(ctx context.Context, guildID string) ([]Channel, error) {
	url := fmt.Sprintf("%s/guilds/%s/channels", DiscordAPIBase, guildID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bot "+c.botToken)
	req.Header.Set("User-Agent", "DiscordFreeCloud/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("channel list returned status %d", resp.StatusCode)
	}

	var channels []Channel
	if err := json.NewDecoder(resp.Body).Decode(&channels); err != nil {
		return nil, err
	}
	return channels, nil
}

// managedCategory describes one storage category and how full it is.
type managedCategory struct {
	id    string
	name  string
	order int // 1 = base category, 2 = "<base> 2", ...
	count int // storage channels currently inside it
}

// buildManagedCategories returns the ordered storage categories for a guild:
// the base category (pinned id, else existing "<prefix>", else created)
// followed by any "<baseName> N" overflow categories already present.
func (c *Client) buildManagedCategories(ctx context.Context, guildID string, channels []Channel) ([]managedCategory, error) {
	prefix := c.poolPrefix

	baseID := c.baseCategoryFor(guildID)
	baseName := ""
	if baseID != "" {
		for _, ch := range channels {
			if ch.ID == baseID {
				baseName = ch.Name
				break
			}
		}
		if baseName == "" {
			return nil, fmt.Errorf("configured base category %s not found in server %s", baseID, guildID)
		}
	} else {
		for _, ch := range channels {
			if ch.Type == 4 && strings.EqualFold(strings.TrimSpace(ch.Name), prefix) {
				baseID = ch.ID
				baseName = ch.Name
				break
			}
		}
		if baseID == "" {
			id, err := c.FindOrCreateCategory(ctx, guildID, prefix)
			if err != nil {
				return nil, err
			}
			baseID = id
			baseName = prefix
		}
	}

	var cats []managedCategory
	seen := make(map[string]bool)
	cats = append(cats, managedCategory{id: baseID, name: baseName, order: 1})
	seen[baseID] = true

	for _, ch := range channels {
		if ch.Type != 4 || seen[ch.ID] {
			continue
		}
		idx := overflowIndex(baseName, ch.Name)
		if idx <= 0 {
			continue
		}
		seen[ch.ID] = true
		cats = append(cats, managedCategory{id: ch.ID, name: ch.Name, order: idx})
	}

	sort.SliceStable(cats, func(i, j int) bool { return cats[i].order < cats[j].order })

	byID := make(map[string]int)
	for _, ch := range channels {
		if ch.Type == 0 && seen[ch.ParentID] {
			byID[ch.ParentID]++
		}
	}
	for i := range cats {
		cats[i].count = byID[cats[i].id]
	}
	return cats, nil
}

// overflowIndex returns N when name == "<base> N" (N >= 2), else 0.
func overflowIndex(baseName, name string) int {
	lowerBase := strings.ToLower(strings.TrimSpace(baseName))
	lowerName := strings.ToLower(strings.TrimSpace(name))
	if lowerName == lowerBase {
		return 1
	}
	if !strings.HasPrefix(lowerName, lowerBase+" ") {
		return 0
	}
	suffix := strings.TrimSpace(lowerName[len(lowerBase)+1:])
	n, err := strconv.Atoi(suffix)
	if err != nil || n < 2 {
		return 0
	}
	return n
}

// SyncPool ensures the guild has at least `desired` storage channels spread
// across its storage categories (spilling into fresh "<baseName> N" categories
// when one fills up), guarantees each storage channel has exactly one webhook
// (reusing an existing webhook when present), and returns the full pool.
func (c *Client) SyncPool(ctx context.Context, guildID string, desired int) ([]ProvisionItem, error) {
	if c.botToken == "" || guildID == "" {
		return nil, errors.New("bot token and server id needed")
	}
	if desired < 1 {
		desired = DefaultPoolTarget
	}
	if desired > MaxPoolTarget {
		desired = MaxPoolTarget
	}

	channels, err := c.listGuildChannels(ctx, guildID)
	if err != nil {
		return nil, err
	}

	cats, err := c.buildManagedCategories(ctx, guildID, channels)
	if err != nil {
		return nil, err
	}

	managed := make(map[string]bool)
	for _, cat := range cats {
		managed[cat.id] = true
	}

	storageCount := 0
	for _, cat := range cats {
		storageCount += cat.count
	}

	// fresh tracks channels created during this call (with their webhooks).
	fresh := make(map[string]ProvisionItem)
	seq := storageCount + 1
	nextOverflowOrder := 1
	for _, cat := range cats {
		if cat.order > nextOverflowOrder {
			nextOverflowOrder = cat.order
		}
	}

	for storageCount < desired {
		var target *managedCategory
		for i := range cats {
			if cats[i].count < MaxChannelsPerCategory {
				target = &cats[i]
				break
			}
		}
		if target == nil {
			nextOverflowOrder++
			newName := fmt.Sprintf("%s %d", cats[0].name, nextOverflowOrder)
			newID, err := c.FindOrCreateCategory(ctx, guildID, newName)
			if err != nil {
				return nil, fmt.Errorf("could not open category %s: %w", newName, err)
			}
			cats = append(cats, managedCategory{id: newID, name: newName, order: nextOverflowOrder})
			managed[newID] = true
			target = &cats[len(cats)-1]
		}

		chName := fmt.Sprintf("storage %03d", seq)
		prov, err := c.CreateChannel(ctx, guildID, target.id, chName, "Encrypted cloud storage shard")
		if err != nil {
			return nil, err
		}
		fresh[prov.Channel.ID] = *prov
		target.count++
		storageCount++
		seq++
		time.Sleep(150 * time.Millisecond)
	}

	if n := len(fresh); n > 0 {
		log.Printf("provisioned %d new storage channel(s) on server %s (pool now %d)", n, guildID, storageCount)
	}

	// Build the full authoritative pool in id order: existing channels from
	// the listing first, then any channels just created.
	var result []ProvisionItem
	seenID := make(map[string]bool)
	for _, ch := range channels {
		if ch.Type != 0 || !managed[ch.ParentID] {
			continue
		}
		seenID[ch.ID] = true
		item := ProvisionItem{Channel: ch}
		if prov, ok := fresh[ch.ID]; ok {
			item.Webhook = prov.Webhook
		} else {
			wh, err := c.ensureSingleWebhook(ctx, ch.ID, ch.Name)
			if err != nil {
				log.Printf("webhook sync failed for channel %s: %v", ch.ID, err)
				continue
			}
			item.Webhook = *wh
		}
		result = append(result, item)
	}
	// Channels created in this call were not part of the original listing.
	createdIDs := make([]string, 0, len(fresh))
	for id := range fresh {
		createdIDs = append(createdIDs, id)
	}
	sort.Strings(createdIDs)
	for _, id := range createdIDs {
		if seenID[id] {
			continue
		}
		prov := fresh[id]
		seenID[id] = true
		result = append(result, prov)
	}

	return result, nil
}

// ensureSingleWebhook returns the first existing webhook on a channel, or
// creates one if the channel has none (keeps one webhook per channel).
func (c *Client) ensureSingleWebhook(ctx context.Context, channelID, channelName string) (*Webhook, error) {
	url := fmt.Sprintf("%s/channels/%s/webhooks", DiscordAPIBase, channelID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bot "+c.botToken)
	req.Header.Set("User-Agent", "DiscordFreeCloud/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var hooks []Webhook
		if err := json.Unmarshal(body, &hooks); err == nil && len(hooks) > 0 {
			return &hooks[0], nil
		}
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return nil, fmt.Errorf("webhook list returned status %d", resp.StatusCode)
	}

	return c.CreateWebhook(ctx, channelID, channelName)
}
