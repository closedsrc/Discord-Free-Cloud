package uploader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"discord-free-cloud/internal/crypto"
	"discord-free-cloud/internal/db"
	"discord-free-cloud/internal/discord"
	"discord-free-cloud/internal/syswin"
	"github.com/google/uuid"
)

const (
	DefaultChunkSize = 7500 * 1024
	DefaultWorkers   = 6
)

type TelemetryEvent struct {
	JobID           string  `json:"job_id"`
	FileID          string  `json:"file_id"`
	FileName        string  `json:"file_name"`
	Type            string  `json:"type"`
	TotalBytes      int64   `json:"total_bytes"`
	ProcessedBytes  int64   `json:"processed_bytes"`
	TotalChunks     int     `json:"total_chunks"`
	CompletedChunks int     `json:"completed_chunks"`
	SpeedMBs        float64 `json:"speed_mbs"`
	ETASeconds      int64   `json:"eta_seconds"`
	ActiveWorkers   int     `json:"active_workers"`
	ActiveShard     string  `json:"active_shard,omitempty"`
	ProgressPercent float64 `json:"progress_percent"`
	Status          string  `json:"status"`
	Error           string  `json:"error,omitempty"`
	LogMessage      string  `json:"log_message,omitempty"`
}

type TelemetryListener func(event TelemetryEvent)

type Engine struct {
	db          *db.Database
	discord     *discord.Client
	masterKey   []byte
	chunkSize   int
	workerCount int
	bufferPool  sync.Pool

	mu         sync.RWMutex
	activeJobs map[string]*activeUpload
	listeners  []TelemetryListener
}

type activeUpload struct {
	jobID          string
	fileID         string
	cancel         context.CancelFunc
	processedBytes int64
	lastBytes      int64
	lastTime       time.Time
	startTime      time.Time
	smoothSpeed    float64
	activeWorkers  int32
}

func NewEngine(database *db.Database, discordClient *discord.Client, masterKey []byte, chunkSize int, workers int) *Engine {
	if chunkSize <= 0 || chunkSize > 8*1024*1024 {
		chunkSize = DefaultChunkSize
	}
	if workers <= 0 {
		cpus := runtime.NumCPU()
		if cpus >= 8 {
			workers = 8
		} else if cpus >= 4 {
			workers = 6
		} else {
			workers = 4
		}
	}

	e := &Engine{
		db:          database,
		discord:     discordClient,
		masterKey:   masterKey,
		chunkSize:   chunkSize,
		workerCount: workers,
		activeJobs:  make(map[string]*activeUpload),
		bufferPool: sync.Pool{
			New: func() any {
				b := make([]byte, chunkSize)
				return &b
			},
		},
	}
	return e
}

func (e *Engine) SetMasterKey(key []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.masterKey = key
}

func (e *Engine) HasMasterKey() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.masterKey) == crypto.KeyLength
}

func (e *Engine) AddTelemetryListener(listener TelemetryListener) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.listeners = append(e.listeners, listener)
}

func (e *Engine) broadcast(event TelemetryEvent) {
	e.mu.RLock()
	listeners := make([]TelemetryListener, len(e.listeners))
	copy(listeners, e.listeners)
	e.mu.RUnlock()

	for _, l := range listeners {
		l(event)
	}
}

type ServerTarget struct {
	GuildID   string
	GuildName string
	BotToken  string
}

type partWorkItem struct {
	chunk    db.ChunkRecord
	webhook  string
	chName   string
	guildID  string
	botToken string
}

func (e *Engine) UploadFile(ctx context.Context, localFilePath, virtualPath, parentID string, targetGuildIDs ...string) (string, error) {
	e.mu.RLock()
	key := e.masterKey
	e.mu.RUnlock()

	if len(key) != crypto.KeyLength {
		return "", errors.New("drive is locked")
	}

	normPath := syswin.NormalizeLongPath(localFilePath)
	fileInfo, err := os.Stat(normPath)
	if err != nil {
		return "", fmt.Errorf("could not find file %s %w", localFilePath, err)
	}

	if fileInfo.IsDir() {
		return "", errors.New("this item is a folder use folder upload instead")
	}

	fileSize := fileInfo.Size()
	totalChunks := int(math.Ceil(float64(fileSize) / float64(e.chunkSize)))
	if totalChunks == 0 {
		totalChunks = 1
	}

	fileName := filepath.Base(localFilePath)
	if virtualPath == "" {
		virtualPath = "/" + fileName
	}

	var targets []ServerTarget
	botNodes, _ := e.db.GetAllBotNodes()
	defaultGuildID, _ := e.db.GetSetting("guild_id")
	defaultBotToken, _ := e.db.GetSetting("bot_token")

	var specificGuilds []string
	for _, g := range targetGuildIDs {
		g = strings.TrimSpace(g)
		if g != "" && g != "all" {
			specificGuilds = append(specificGuilds, g)
		}
	}

	if len(specificGuilds) > 0 {
		for _, sg := range specificGuilds {
			found := false
			for _, n := range botNodes {
				if n.GuildID == sg {
					targets = append(targets, ServerTarget{GuildID: n.GuildID, GuildName: n.GuildName, BotToken: n.BotToken})
					found = true
					break
				}
			}
			if !found {
				targets = append(targets, ServerTarget{GuildID: sg, GuildName: "Discord Server", BotToken: defaultBotToken})
			}
		}
	} else if len(botNodes) > 0 {
		for _, n := range botNodes {
			targets = append(targets, ServerTarget{GuildID: n.GuildID, GuildName: n.GuildName, BotToken: n.BotToken})
		}
	} else if defaultGuildID != "" {
		targets = append(targets, ServerTarget{GuildID: defaultGuildID, GuildName: "Main Server", BotToken: defaultBotToken})
	}

	if len(targets) == 0 {
		existingCh, _ := e.db.GetStorageChannels()
		if len(existingCh) > 0 {
			targets = append(targets, ServerTarget{GuildID: "default", GuildName: "Main Server", BotToken: defaultBotToken})
		} else {
			return "", errors.New("please enter your Discord Bot Token and Server ID in Easy Setup first")
		}
	}

	type serverChannelSet struct {
		target   ServerTarget
		channels []db.ChannelRecord
	}
	var serverChannelSets []serverChannelSet

	for _, t := range targets {
		storageChannels, err := e.db.GetStorageChannelsForGuild(t.GuildID)
		if err != nil || len(storageChannels) == 0 {
			bToken := t.BotToken
			if bToken == "" {
				bToken = defaultBotToken
			}
			if t.GuildID != "" && bToken != "" {
				res, setupErr := e.discord.SetupServerWithToken(ctx, bToken, t.GuildID)
				if setupErr == nil && res != nil && len(res.StorageChannels) > 0 {
					for _, ch := range res.StorageChannels {
						_ = e.db.AddChannelWithGuild(ch.Channel.ID, ch.Channel.Name, ch.Webhook.URL, t.GuildID, bToken, false)
					}
					if res.MetadataChannel.ID != "" {
						_ = e.db.AddChannelWithGuild(res.MetadataChannel.ID, res.MetadataChannel.Name, "", t.GuildID, bToken, true)
					}
					storageChannels, _ = e.db.GetStorageChannelsForGuild(t.GuildID)
				}
			}
		}

		if len(storageChannels) > 0 {
			serverChannelSets = append(serverChannelSets, serverChannelSet{
				target:   t,
				channels: storageChannels,
			})
		}
	}

	if len(serverChannelSets) == 0 {
		allCh, _ := e.db.GetStorageChannels()
		if len(allCh) > 0 {
			serverChannelSets = append(serverChannelSets, serverChannelSet{
				target:   ServerTarget{GuildID: defaultGuildID, GuildName: "Default Server", BotToken: defaultBotToken},
				channels: allCh,
			})
		}
	}

	if len(serverChannelSets) == 0 {
		return "", errors.New("no storage channels available. Please click Set Up Everything in Easy Setup or add a server")
	}

	var fileRec *db.FileRecord
	existingFile, _ := e.db.GetFileByPath(virtualPath)
	if existingFile != nil {
		fileRec = existingFile
		fileRec.Size = fileSize
		fileRec.ModTime = fileInfo.ModTime().Unix()
		fileRec.ParentID = parentID
	} else {
		fileRec = &db.FileRecord{
			ID:        uuid.New().String(),
			ParentID:  parentID,
			Name:      fileName,
			Path:      virtualPath,
			Size:      fileSize,
			IsDir:     false,
			ModTime:   fileInfo.ModTime().Unix(),
			SHA256:    "",
			MimeType:  "application/octet-stream",
			CreatedAt: time.Now().Unix(),
		}
	}

	if err := e.db.UpsertFile(fileRec); err != nil {
		return "", fmt.Errorf("could not save file info %w", err)
	}

	var workItems []partWorkItem
	jobID := uuid.New().String()

	totalWorkItems := 0
	for _, scSet := range serverChannelSets {
		if len(scSet.channels) > 0 {
			totalWorkItems += totalChunks
		}
	}

	jobRec := &db.JobRecord{
		ID:              jobID,
		Type:            "UPLOAD",
		FileID:          fileRec.ID,
		FilePath:        localFilePath,
		TotalBytes:      fileSize * int64(len(serverChannelSets)),
		TotalChunks:     totalWorkItems,
		CompletedChunks: 0,
		Status:          db.JobStatusActive,
		CreatedAt:       time.Now().Unix(),
	}

	if err := e.db.CreateJob(jobRec); err != nil {
		return "", fmt.Errorf("could not create job %w", err)
	}

	for _, scSet := range serverChannelSets {
		srv := scSet.target
		chList := scSet.channels
		if len(chList) == 0 {
			continue
		}

		for i := 0; i < totalChunks; i++ {
			offset := int64(i * e.chunkSize)
			cSize := e.chunkSize
			if offset+int64(cSize) > fileSize {
				cSize = int(fileSize - offset)
			}

			sh := chList[i%len(chList)]

			chunkRec := &db.ChunkRecord{
				ID:         uuid.New().String(),
				JobID:      jobID,
				FileID:     fileRec.ID,
				ChunkIndex: i,
				ByteOffset: offset,
				ChunkSize:  cSize,
				SHA256:     "",
				GuildID:    srv.GuildID,
				ChannelID:  sh.ChannelID,
				Status:     db.StatusPending,
				CreatedAt:  time.Now().Unix(),
			}
			if err := e.db.CreateChunk(chunkRec); err != nil {
				return "", fmt.Errorf("could not prepare part %d on server %s %w", i, srv.GuildName, err)
			}

			dispName := sh.ChannelName
			if srv.GuildName != "" {
				dispName = fmt.Sprintf("%s (%s)", sh.ChannelName, srv.GuildName)
			}

			workItems = append(workItems, partWorkItem{
				chunk:    *chunkRec,
				webhook:  sh.WebhookURL,
				chName:   dispName,
				guildID:  srv.GuildID,
				botToken: srv.BotToken,
			})
		}
	}

	jobCtx, cancel := context.WithCancel(ctx)
	uploadTracker := &activeUpload{
		jobID:          jobID,
		fileID:         fileRec.ID,
		cancel:         cancel,
		processedBytes: 0,
		lastBytes:      0,
		lastTime:       time.Now(),
		startTime:      time.Now(),
	}

	e.mu.Lock()
	e.activeJobs[jobID] = uploadTracker
	e.mu.Unlock()

	log.Printf("starting upload %s (%.2f MB, %d parts, %d servers)", fileName, float64(fileSize)/(1024*1024), totalChunks, len(serverChannelSets))

	go e.runUpload(jobCtx, uploadTracker, jobRec, fileRec, normPath, workItems, key)

	return jobID, nil
}

func (e *Engine) runUpload(ctx context.Context, tracker *activeUpload, job *db.JobRecord, file *db.FileRecord, normPath string, items []partWorkItem, key []byte) {
	defer func() {
		e.mu.Lock()
		delete(e.activeJobs, job.ID)
		e.mu.Unlock()
	}()

	f, err := os.Open(normPath)
	if err != nil {
		_ = e.db.UpdateJobStatus(job.ID, db.JobStatusFailed)
		log.Printf("could not read file %s: %v", normPath, err)
		e.broadcast(TelemetryEvent{
			JobID:      job.ID,
			FileID:     file.ID,
			Status:     db.JobStatusFailed,
			Error:      fmt.Sprintf("could not read file %v", err),
			LogMessage: fmt.Sprintf("Error opening local file: %v", err),
		})
		return
	}
	defer f.Close()

	if file.SHA256 == "" {
		if hash, hashErr := crypto.ComputeStreamSHA256(f); hashErr == nil && hash != "" {
			file.SHA256 = hash
			_ = e.db.UpsertFile(file)
		}
		_, _ = f.Seek(0, 0)
	}

	chunkChan := make(chan partWorkItem, len(items))
	for _, it := range items {
		chunkChan <- it
	}
	close(chunkChan)

	var wg sync.WaitGroup
	errChan := make(chan error, e.workerCount)
	var completedCount int64 = int64(job.CompletedChunks)
	atomic.StoreInt64(&tracker.processedBytes, completedCount*int64(e.chunkSize))

	workers := e.workerCount
	if workers > len(items) {
		workers = len(items)
	}
	if workers < 1 {
		workers = 1
	}

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			bufPtr := e.bufferPool.Get().(*[]byte)
			defer e.bufferPool.Put(bufPtr)
			rawBuf := *bufPtr

			for {
				select {
				case <-ctx.Done():
					return
				case item, ok := <-chunkChan:
					if !ok {
						return
					}

					chunk := item.chunk
					atomic.AddInt32(&tracker.activeWorkers, 1)

					chunkSlice := rawBuf[:chunk.ChunkSize]
					n, err := f.ReadAt(chunkSlice, chunk.ByteOffset)
					if err != nil && err != io.EOF && n == 0 {
						atomic.AddInt32(&tracker.activeWorkers, -1)
						log.Printf("read failed at byte offset %d for %s: %v", chunk.ByteOffset, file.Name, err)
						errChan <- fmt.Errorf("read error on part %d %w", chunk.ChunkIndex, err)
						return
					}

					actualChunkBytes := chunkSlice[:n]
					chunkHash := crypto.ComputeSHA256(actualChunkBytes)

					existingChunk, _ := e.db.FindChunkByHash(chunkHash)
					if existingChunk != nil && existingChunk.AttachmentURL != "" {
						_ = e.db.UpdateChunkStatus(chunk.ID, db.StatusCompleted, existingChunk.ChannelID, existingChunk.MessageID, existingChunk.AttachmentID, existingChunk.AttachmentURL)
						_ = e.db.UpdateChunkHash(chunk.ID, chunkHash)
						_ = e.db.IncrementJobCompletedChunks(job.ID)

						curDone := atomic.AddInt64(&completedCount, 1)
						atomic.AddInt64(&tracker.processedBytes, int64(n))
						atomic.AddInt32(&tracker.activeWorkers, -1)

						e.broadcast(TelemetryEvent{
							JobID:           job.ID,
							FileID:          file.ID,
							FileName:        file.Name,
							Type:            "UPLOAD",
							TotalBytes:      job.TotalBytes,
							ProcessedBytes:  atomic.LoadInt64(&tracker.processedBytes),
							TotalChunks:     job.TotalChunks,
							CompletedChunks: int(curDone),
							ActiveWorkers:   int(atomic.LoadInt32(&tracker.activeWorkers)),
							ActiveShard:     item.chName,
							Status:          db.JobStatusActive,
							LogMessage:      fmt.Sprintf("Reused instant duplicate part %d of %d", chunk.ChunkIndex+1, job.TotalChunks),
						})
						continue
					}

					aad := []byte(fmt.Sprintf("%s:%d", file.ID, chunk.ChunkIndex))
					encryptedPayload, err := crypto.EncryptChunk(actualChunkBytes, key, aad)
					if err != nil {
						atomic.AddInt32(&tracker.activeWorkers, -1)
						log.Printf("encryption error on part %d: %v", chunk.ChunkIndex, err)
						errChan <- fmt.Errorf("lock error on part %d %w", chunk.ChunkIndex, err)
						return
					}

					pngWrapped := crypto.WrapPNG(encryptedPayload)

					e.broadcast(TelemetryEvent{
						JobID:           job.ID,
						FileID:          file.ID,
						FileName:        file.Name,
						Type:            "UPLOAD",
						TotalBytes:      job.TotalBytes,
						ProcessedBytes:  atomic.LoadInt64(&tracker.processedBytes),
						TotalChunks:     job.TotalChunks,
						CompletedChunks: int(atomic.LoadInt64(&completedCount)),
						ActiveWorkers:   int(atomic.LoadInt32(&tracker.activeWorkers)),
						ActiveShard:     item.chName,
						Status:          db.JobStatusActive,
						LogMessage:      fmt.Sprintf("Uploading part %d of %d (%.2f MB) to %s", chunk.ChunkIndex+1, job.TotalChunks, float64(n)/(1024*1024), item.chName),
					})

					metadataMsg := fmt.Sprintf("Image Part %d of %d", chunk.ChunkIndex+1, job.TotalChunks)
					chunkFilename := fmt.Sprintf("image_part_%05d.png", chunk.ChunkIndex)

					res, err := e.discord.UploadChunk(ctx, chunk.ChannelID, item.webhook, chunkFilename, pngWrapped, metadataMsg)
					if err != nil {
						atomic.AddInt32(&tracker.activeWorkers, -1)
						_ = e.db.MarkChunkFailed(chunk.ID)
						log.Printf("discord rejected part %d on %s: %v", chunk.ChunkIndex+1, item.chName, err)
						errChan <- fmt.Errorf("upload error on part %d %w", chunk.ChunkIndex, err)
						return
					}

					_ = e.db.UpdateChunkWithGuild(chunk.ID, db.StatusCompleted, item.guildID, res.ChannelID, res.MessageID, res.AttachmentID, res.AttachmentURL)
					_ = e.db.UpdateChunkHash(chunk.ID, chunkHash)
					_ = e.db.IncrementJobCompletedChunks(job.ID)

					curDone := atomic.AddInt64(&completedCount, 1)
					atomic.AddInt64(&tracker.processedBytes, int64(n))
					atomic.AddInt32(&tracker.activeWorkers, -1)

					now := time.Now()
					dt := now.Sub(tracker.lastTime).Seconds()
					curBytes := atomic.LoadInt64(&tracker.processedBytes)

					if dt >= 0.25 {
						bytesDiff := curBytes - tracker.lastBytes
						instantSpeed := (float64(bytesDiff) / (1024 * 1024)) / dt
						if tracker.smoothSpeed <= 0 {
							tracker.smoothSpeed = instantSpeed
						} else {
							tracker.smoothSpeed = (0.7 * instantSpeed) + (0.3 * tracker.smoothSpeed)
						}
						tracker.lastBytes = curBytes
						tracker.lastTime = now
					}

					elapsed := now.Sub(tracker.startTime).Seconds()
					var overallSpeed float64 = 0
					if elapsed > 0 {
						overallSpeed = (float64(curBytes) / (1024 * 1024)) / elapsed
					}

					speedMBs := tracker.smoothSpeed
					if speedMBs <= 0 {
						speedMBs = overallSpeed
					} else if overallSpeed > 0 {
						speedMBs = (0.75 * tracker.smoothSpeed) + (0.25 * overallSpeed)
					}

					remBytes := job.TotalBytes - curBytes
					var etaSec int64 = 0
					if speedMBs > 0 && remBytes > 0 {
						etaSec = int64(math.Ceil(float64(remBytes) / (speedMBs * 1024 * 1024)))
					}

					progressPct := 0.0
					if job.TotalBytes > 0 {
						progressPct = (float64(curBytes) / float64(job.TotalBytes)) * 100.0
					} else if job.TotalChunks > 0 {
						progressPct = (float64(curDone) / float64(job.TotalChunks)) * 100.0
					}
					if progressPct > 100.0 {
						progressPct = 100.0
					}

					log.Printf("part %d/%d (%.2f MB) done on %s (%.2f MB/s, ETA: %ds)", chunk.ChunkIndex+1, job.TotalChunks, float64(n)/(1024*1024), item.chName, speedMBs, etaSec)

					e.broadcast(TelemetryEvent{
						JobID:           job.ID,
						FileID:          file.ID,
						FileName:        file.Name,
						Type:            "UPLOAD",
						TotalBytes:      job.TotalBytes,
						ProcessedBytes:  tracker.processedBytes,
						TotalChunks:     job.TotalChunks,
						CompletedChunks: int(curDone),
						SpeedMBs:        speedMBs,
						ETASeconds:      etaSec,
						ActiveWorkers:   int(atomic.LoadInt32(&tracker.activeWorkers)),
						ActiveShard:     item.chName,
						ProgressPercent: progressPct,
						Status:          db.JobStatusActive,
						LogMessage:      fmt.Sprintf("Part %d of %d finished on %s", chunk.ChunkIndex+1, job.TotalChunks, item.chName),
					})
				}
			}
		}(w)
	}

	wg.Wait()
	close(errChan)

	if err, ok := <-errChan; ok && err != nil {
		_ = e.db.UpdateJobStatus(job.ID, db.JobStatusFailed)
		log.Printf("job %s failed: %v", job.ID, err)
		e.broadcast(TelemetryEvent{
			JobID:      job.ID,
			FileID:     file.ID,
			Status:     db.JobStatusFailed,
			Error:      err.Error(),
			LogMessage: fmt.Sprintf("Upload stopped: %v", err),
		})
		return
	}

	_ = e.db.UpsertFile(file)
	_ = e.db.UpdateJobStatus(job.ID, db.JobStatusCompleted)

	log.Printf("uploaded %s (SHA256: %s)", file.Name, file.SHA256)

	e.broadcast(TelemetryEvent{
		JobID:           job.ID,
		FileID:          file.ID,
		FileName:        file.Name,
		Type:            "UPLOAD",
		TotalBytes:      job.TotalBytes,
		ProcessedBytes:  job.TotalBytes,
		TotalChunks:     job.TotalChunks,
		CompletedChunks: job.TotalChunks,
		SpeedMBs:        0,
		ETASeconds:      0,
		ActiveWorkers:   0,
		ProgressPercent: 100.0,
		Status:          db.JobStatusCompleted,
		LogMessage:      fmt.Sprintf("Uploaded %s", file.Name),
	})
}

func (e *Engine) UploadDir(ctx context.Context, localDirPath, virtualParentPath string, targetGuildIDs ...string) ([]string, error) {
	e.mu.RLock()
	key := e.masterKey
	e.mu.RUnlock()

	if len(key) != crypto.KeyLength {
		return nil, errors.New("drive is locked")
	}

	normDir := syswin.NormalizeLongPath(localDirPath)
	var jobIDs []string
	folderName := filepath.Base(localDirPath)

	parentID := ""
	if virtualParentPath != "" && virtualParentPath != "/" {
		parentRec, err := e.db.GetFileByPath(virtualParentPath)
		if err == nil && parentRec != nil {
			parentID = parentRec.ID
		}
	}

	rootFolderPath := filepath.ToSlash(filepath.Join(virtualParentPath, folderName))
	dirRec := &db.FileRecord{
		ID:        uuid.New().String(),
		ParentID:  parentID,
		Name:      folderName,
		Path:      rootFolderPath,
		Size:      0,
		IsDir:     true,
		ModTime:   time.Now().Unix(),
		SHA256:    "",
		MimeType:  "folder",
		CreatedAt: time.Now().Unix(),
	}
	_ = e.db.UpsertFile(dirRec)

	dirMap := make(map[string]string)
	dirMap["."] = dirRec.ID

	err := filepath.Walk(normDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(normDir, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		vPath := filepath.ToSlash(filepath.Join(virtualParentPath, folderName, relPath))
		parentRel := filepath.ToSlash(filepath.Dir(relPath))
		immediateParentID := dirMap[parentRel]
		if immediateParentID == "" {
			immediateParentID = dirRec.ID
		}

		if info.IsDir() {
			subDirRec := &db.FileRecord{
				ID:        uuid.New().String(),
				ParentID:  immediateParentID,
				Name:      info.Name(),
				Path:      vPath,
				Size:      0,
				IsDir:     true,
				ModTime:   info.ModTime().Unix(),
				SHA256:    "",
				MimeType:  "folder",
				CreatedAt: time.Now().Unix(),
			}
			_ = e.db.UpsertFile(subDirRec)
			dirMap[filepath.ToSlash(relPath)] = subDirRec.ID
			return nil
		}

		jobID, err := e.UploadFile(ctx, path, vPath, immediateParentID, targetGuildIDs...)
		if err != nil {
			return fmt.Errorf("could not upload %s %w", path, err)
		}
		jobIDs = append(jobIDs, jobID)
		return nil
	})

	return jobIDs, err
}

func (e *Engine) CancelJob(jobID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if active, exists := e.activeJobs[jobID]; exists {
		active.cancel()
		delete(e.activeJobs, jobID)
	}
	_ = e.db.UpdateJobStatus(jobID, db.JobStatusPaused)
}

func (e *Engine) CancelAllJobs() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for jobID, active := range e.activeJobs {
		active.cancel()
		delete(e.activeJobs, jobID)
		_ = e.db.UpdateJobStatus(jobID, db.JobStatusPaused)
	}
}
