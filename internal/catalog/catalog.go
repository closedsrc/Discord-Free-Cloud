package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"discord-free-cloud/internal/crypto"
	"discord-free-cloud/internal/db"
	"discord-free-cloud/internal/discord"
)

type ManifestSnapshot struct {
	Version         string             `json:"version"`
	CreatedAt       int64              `json:"created_at"`
	Files           []db.FileRecord    `json:"files"`
	Chunks          []db.ChunkRecord   `json:"chunks"`
	StorageChannels []db.ChannelRecord `json:"storage_channels"`
}

type SyncManager struct {
	db        *db.Database
	discord   *discord.Client
	masterKey []byte
}

func NewSyncManager(database *db.Database, discordClient *discord.Client, masterKey []byte) *SyncManager {
	return &SyncManager{
		db:        database,
		discord:   discordClient,
		masterKey: masterKey,
	}
}

func (s *SyncManager) SetMasterKey(key []byte) {
	s.masterKey = key
}

func (s *SyncManager) ExportAndSyncToDiscord(ctx context.Context) (string, error) {
	if len(s.masterKey) != crypto.KeyLength {
		return "", errors.New("please enter your master password in Easy Setup first")
	}

	metaChannel, err := s.db.GetMetadataChannel()
	if err != nil || metaChannel == nil {
		guildID, _ := s.db.GetSetting("guild_id")
		if guildID != "" {
			res, err := s.discord.AutoSetupServer(ctx, guildID)
			if err == nil && res.MetadataChannel.ID != "" {
				_ = s.db.AddChannel(res.MetadataChannel.ID, res.MetadataChannel.Name, "", true)
				metaChannel = &db.ChannelRecord{
					ChannelID:   res.MetadataChannel.ID,
					ChannelName: res.MetadataChannel.Name,
					IsMetadata:  true,
				}
			}
		}
	}

	if metaChannel == nil {
		return "", errors.New("backup channel is not ready please enter your server ID in Easy Setup first")
	}

	files, err := s.db.GetAllFiles()
	if err != nil {
		return "", fmt.Errorf("could not get files %w", err)
	}

	var allChunks []db.ChunkRecord
	for _, f := range files {
		chunks, err := s.db.GetChunksForFile(f.ID)
		if err == nil {
			allChunks = append(allChunks, chunks...)
		}
	}

	channels, err := s.db.GetStorageChannels()
	if err != nil {
		return "", fmt.Errorf("could not get channels %w", err)
	}

	manifest := ManifestSnapshot{
		Version:         "1.0",
		CreatedAt:       time.Now().Unix(),
		Files:           files,
		Chunks:          allChunks,
		StorageChannels: channels,
	}

	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("could not encode manifest %w", err)
	}

	aad := []byte("discord drive backup checkpoint v1")
	encryptedManifest, err := crypto.EncryptChunk(manifestJSON, s.masterKey, aad)
	if err != nil {
		return "", fmt.Errorf("could not lock backup snapshot %w", err)
	}

	pngWrapped := crypto.WrapInPNGContainer(encryptedManifest)
	timestampStr := time.Now().Format("2006 01 02 15 04 05")
	filename := fmt.Sprintf("backup_checkpoint_%s.png", timestampStr)
	metaMsg := fmt.Sprintf("Backup Image Snapshot with %d files", len(files))

	res, err := s.discord.UploadChunk(ctx, metaChannel.ChannelID, metaChannel.WebhookURL, filename, pngWrapped, metaMsg)
	if err != nil {
		return "", fmt.Errorf("could not upload backup to discord %w", err)
	}

	_ = s.db.SetSetting("last_catalog_sync", fmt.Sprintf("%d", time.Now().Unix()))
	return res.MessageID, nil
}

func (s *SyncManager) RestoreFromDiscord(ctx context.Context, metaChannelID string) (*ManifestSnapshot, error) {
	if len(s.masterKey) != crypto.KeyLength {
		return nil, errors.New("please enter your master password in Easy Setup first")
	}
	if metaChannelID == "" {
		metaChannel, err := s.db.GetMetadataChannel()
		if err == nil && metaChannel != nil {
			metaChannelID = metaChannel.ChannelID
		} else {
			guildID, _ := s.db.GetSetting("guild_id")
			if guildID != "" {
				res, err := s.discord.AutoSetupServer(ctx, guildID)
				if err == nil && res.MetadataChannel.ID != "" {
					_ = s.db.AddChannel(res.MetadataChannel.ID, res.MetadataChannel.Name, "", true)
					metaChannelID = res.MetadataChannel.ID
				}
			}
		}
	}
	if metaChannelID == "" {
		return nil, errors.New("backup channel ID needed for restore")
	}

	freshURL, err := s.discord.GetFreshAttachmentURL(ctx, metaChannelID, "", "")
	if err != nil {
		return nil, fmt.Errorf("could not find backup in discord channel %w", err)
	}

	encBytes, err := s.discord.DownloadChunkBytes(ctx, freshURL)
	if err != nil {
		return nil, fmt.Errorf("could not download backup file %w", err)
	}

	unwrapped := crypto.UnwrapPNGContainer(encBytes)
	aad := []byte("discord drive backup checkpoint v1")
	decryptedJSON, err := crypto.DecryptChunk(unwrapped, s.masterKey, aad)
	if err != nil {
		return nil, fmt.Errorf("could not open backup check your password %w", err)
	}

	var manifest ManifestSnapshot
	if err := json.Unmarshal(decryptedJSON, &manifest); err != nil {
		return nil, fmt.Errorf("invalid backup format %w", err)
	}

	for _, f := range manifest.Files {
		_ = s.db.UpsertFile(&f)
	}
	for _, ch := range manifest.Chunks {
		if ch.JobID != "" {
			_ = s.db.CreateJob(&db.JobRecord{
				ID:              ch.JobID,
				Type:            "UPLOAD",
				FileID:          ch.FileID,
				FilePath:        "",
				TotalBytes:      int64(ch.ChunkSize),
				TotalChunks:     1,
				CompletedChunks: 1,
				Status:          db.JobStatusCompleted,
				CreatedAt:       time.Now().Unix(),
			})
		}
		_ = s.db.CreateChunk(&ch)
	}
	for _, ch := range manifest.StorageChannels {
		_ = s.db.AddChannel(ch.ChannelID, ch.ChannelName, ch.WebhookURL, false)
	}

	return &manifest, nil
}
