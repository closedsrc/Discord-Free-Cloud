package downloader

import (
	"archive/zip"
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
	"discord-free-cloud/internal/uploader"
	"github.com/google/uuid"
)

const (
	DefaultDownloadWorkers = 8
	DiskSafetyMarginBytes  = 50 * 1024 * 1024
)

type chunkCacheEntry struct {
	data       []byte
	accessedAt time.Time
}

type Engine struct {
	db          *db.Database
	discord     *discord.Client
	masterKey   []byte
	workerCount int

	mu         sync.RWMutex
	activeJobs map[string]*activeDownload
	listeners  []uploader.TelemetryListener

	chunkCacheMu sync.RWMutex
	chunkCache   map[string]*chunkCacheEntry
	cacheOrder   []string
	maxCacheSize int
}

type activeDownload struct {
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

func NewEngine(database *db.Database, discordClient *discord.Client, masterKey []byte, workers int) *Engine {
	if workers <= 0 {
		cpus := runtime.NumCPU()
		if cpus >= 8 {
			workers = 10
		} else if cpus >= 4 {
			workers = 8
		} else {
			workers = 4
		}
	}

	return &Engine{
		db:           database,
		discord:      discordClient,
		masterKey:    masterKey,
		workerCount:  workers,
		activeJobs:   make(map[string]*activeDownload),
		chunkCache:   make(map[string]*chunkCacheEntry),
		maxCacheSize: 32,
	}
}

func (e *Engine) getCachedChunk(cacheKey string) []byte {
	e.chunkCacheMu.Lock()
	defer e.chunkCacheMu.Unlock()
	if entry, ok := e.chunkCache[cacheKey]; ok {
		entry.accessedAt = time.Now()
		return entry.data
	}
	return nil
}

func (e *Engine) putCachedChunk(cacheKey string, data []byte) {
	if len(data) == 0 {
		return
	}
	e.chunkCacheMu.Lock()
	defer e.chunkCacheMu.Unlock()

	if e.chunkCache == nil {
		e.chunkCache = make(map[string]*chunkCacheEntry)
		e.maxCacheSize = 32
	}

	if _, exists := e.chunkCache[cacheKey]; !exists {
		if len(e.cacheOrder) >= e.maxCacheSize {
			oldestKey := e.cacheOrder[0]
			e.cacheOrder = e.cacheOrder[1:]
			delete(e.chunkCache, oldestKey)
		}
		e.cacheOrder = append(e.cacheOrder, cacheKey)
	}

	e.chunkCache[cacheKey] = &chunkCacheEntry{
		data:       data,
		accessedAt: time.Now(),
	}
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

func (e *Engine) AddTelemetryListener(listener uploader.TelemetryListener) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.listeners = append(e.listeners, listener)
}

func (e *Engine) broadcast(event uploader.TelemetryEvent) {
	e.mu.RLock()
	listeners := make([]uploader.TelemetryListener, len(e.listeners))
	copy(listeners, e.listeners)
	e.mu.RUnlock()

	for _, l := range listeners {
		l(event)
	}
}

func (e *Engine) fetchChunk(ctx context.Context, fileID string, chunk db.ChunkRecord, key []byte) ([]byte, error) {
	cacheKey := fmt.Sprintf("%s:%d", fileID, chunk.ChunkIndex)
	if cached := e.getCachedChunk(cacheKey); cached != nil {
		return cached, nil
	}

	bytes, err := e.fetchSingleChunk(ctx, fileID, chunk, key)
	if err == nil && len(bytes) > 0 {
		e.putCachedChunk(cacheKey, bytes)
		return bytes, nil
	}

	allChunks, err2 := e.db.GetChunksForFile(fileID)
	if err2 == nil {
		for _, alt := range allChunks {
			if alt.ID != chunk.ID && alt.ChunkIndex == chunk.ChunkIndex && alt.Status == db.StatusCompleted {
				altBytes, altErr := e.fetchSingleChunk(ctx, fileID, alt, key)
				if altErr == nil && len(altBytes) > 0 {
					e.putCachedChunk(cacheKey, altBytes)
					return altBytes, nil
				}
			}
		}
	}

	return nil, err
}

func (e *Engine) fetchSingleChunk(ctx context.Context, fileID string, chunk db.ChunkRecord, key []byte) ([]byte, error) {
	var encBytes []byte
	var err error

	if chunk.AttachmentURL != "" {
		encBytes, err = e.discord.DownloadChunk(ctx, chunk.AttachmentURL)
	}

	if (err != nil || len(encBytes) == 0) && chunk.ChannelID != "" && chunk.MessageID != "" {
		freshURL, freshErr := e.discord.AttachmentURL(ctx, chunk.ChannelID, chunk.MessageID, chunk.AttachmentID)
		if freshErr == nil && freshURL != "" {
			encBytes, err = e.discord.DownloadChunk(ctx, freshURL)
			if err == nil && len(encBytes) > 0 {
				_ = e.db.UpdateChunkWithGuild(chunk.ID, db.StatusCompleted, chunk.GuildID, chunk.ChannelID, chunk.MessageID, chunk.AttachmentID, freshURL)
			}
		}
	}

	if err != nil || len(encBytes) == 0 {
		return nil, fmt.Errorf("download error on part %d %w", chunk.ChunkIndex, err)
	}

	rawPayload := crypto.UnwrapPNG(encBytes)

	var candidates [][]byte
	candidates = append(candidates, []byte(fmt.Sprintf("%s:%d", fileID, chunk.ChunkIndex)))
	if chunk.FileID != "" && chunk.FileID != fileID {
		candidates = append(candidates, []byte(fmt.Sprintf("%s:%d", chunk.FileID, chunk.ChunkIndex)))
	}
	if chunk.SHA256 != "" {
		if origs, err := e.db.FindChunksByHash(chunk.SHA256); err == nil {
			for _, o := range origs {
				candidates = append(candidates, []byte(fmt.Sprintf("%s:%d", o.FileID, o.ChunkIndex)))
			}
		}
	}
	candidates = append(candidates, []byte("discord_chunk_v1"))
	candidates = append(candidates, []byte(fmt.Sprintf("chunk:%d", chunk.ChunkIndex)))
	candidates = append(candidates, nil)

	var decryptedBytes []byte
	for _, aad := range candidates {
		dec, decErr := crypto.DecryptChunk(rawPayload, key, aad)
		if decErr == nil && len(dec) > 0 {
			decryptedBytes = dec
			break
		}
	}

	if decryptedBytes == nil {
		return nil, fmt.Errorf("unlock error on part %d %w", chunk.ChunkIndex, crypto.ErrDecryptionFailed)
	}

	return decryptedBytes, nil
}

func (e *Engine) StreamDownloadRange(ctx context.Context, fileID string, start, end int64, w io.Writer) error {
	e.mu.RLock()
	key := e.masterKey
	e.mu.RUnlock()

	if len(key) != crypto.KeyLength {
		return errors.New("please enter your master password in Easy Setup first")
	}

	fileRec, err := e.db.GetFile(fileID)
	if err != nil || fileRec == nil {
		return fmt.Errorf("file not found %w", err)
	}

	allChunks, err := e.db.GetChunksForFile(fileID)
	if err != nil || len(allChunks) == 0 {
		return fmt.Errorf("no uploaded parts found for %s %w", fileRec.Name, err)
	}

	chunkMap := make(map[int]db.ChunkRecord)
	for _, ch := range allChunks {
		if _, exists := chunkMap[ch.ChunkIndex]; !exists {
			chunkMap[ch.ChunkIndex] = ch
		}
	}
	var chunks []db.ChunkRecord
	for i := 0; i < len(chunkMap); i++ {
		if ch, ok := chunkMap[i]; ok {
			chunks = append(chunks, ch)
		}
	}

	var neededChunks []db.ChunkRecord
	for _, chunk := range chunks {
		chunkStart := chunk.ByteOffset
		chunkEnd := chunkStart + int64(chunk.ChunkSize) - 1
		if chunkEnd >= start && chunkStart <= end {
			neededChunks = append(neededChunks, chunk)
		}
	}

	if len(neededChunks) == 0 {
		return nil
	}

	firstChunk := neededChunks[0]
	cacheKey0 := fmt.Sprintf("%s:%d", fileRec.ID, firstChunk.ChunkIndex)
	var firstDec []byte
	if cached := e.getCachedChunk(cacheKey0); cached != nil {
		firstDec = cached
	} else {
		dec, err := e.fetchChunk(ctx, fileRec.ID, firstChunk, key)
		if err != nil {
			return err
		}
		firstDec = dec
	}

	chunkStart := firstChunk.ByteOffset
	chunkEnd := chunkStart + int64(firstChunk.ChunkSize) - 1

	sliceStart := int64(0)
	if start > chunkStart {
		sliceStart = start - chunkStart
	}

	sliceEnd := int64(len(firstDec))
	if end < chunkEnd {
		sliceEnd = end - chunkStart + 1
	}

	if sliceStart < int64(len(firstDec)) && sliceEnd <= int64(len(firstDec)) && sliceStart < sliceEnd {
		if _, err := w.Write(firstDec[sliceStart:sliceEnd]); err != nil {
			return err
		}
	}

	if len(neededChunks) == 1 {
		if nextChunk, ok := chunkMap[firstChunk.ChunkIndex+1]; ok {
			nextCacheKey := fmt.Sprintf("%s:%d", fileRec.ID, nextChunk.ChunkIndex)
			if e.getCachedChunk(nextCacheKey) == nil {
				go func() {
					bgCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
					defer cancel()
					_, _ = e.fetchChunk(bgCtx, fileRec.ID, nextChunk, key)
				}()
			}
		}
		return nil
	}

	remainingChunks := neededChunks[1:]
	workerLimit := e.workerCount
	if workerLimit > len(remainingChunks) {
		workerLimit = len(remainingChunks)
	}
	if workerLimit < 1 {
		workerLimit = 1
	}

	chunkChan := make(chan db.ChunkRecord, len(remainingChunks))
	for _, ch := range remainingChunks {
		chunkChan <- ch
	}
	close(chunkChan)

	resMap := make(map[int][]byte)
	var mapMu sync.Mutex
	cond := sync.NewCond(&mapMu)
	var workerErr error

	var wg sync.WaitGroup
	for wIdx := 0; wIdx < workerLimit; wIdx++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case ch, ok := <-chunkChan:
					if !ok {
						return
					}

					cacheKey := fmt.Sprintf("%s:%d", fileRec.ID, ch.ChunkIndex)
					var decryptedBytes []byte
					if cached := e.getCachedChunk(cacheKey); cached != nil {
						decryptedBytes = cached
					} else {
						dec, err := e.fetchChunk(ctx, fileRec.ID, ch, key)
						if err != nil {
							mapMu.Lock()
							workerErr = err
							cond.Broadcast()
							mapMu.Unlock()
							return
						}
						decryptedBytes = dec
					}

					mapMu.Lock()
					resMap[ch.ChunkIndex] = decryptedBytes
					cond.Broadcast()
					mapMu.Unlock()
				}
			}
		}()
	}

	ctxDone := make(chan struct{})
	defer close(ctxDone)
	go func() {
		select {
		case <-ctx.Done():
			mapMu.Lock()
			if workerErr == nil {
				workerErr = ctx.Err()
			}
			cond.Broadcast()
			mapMu.Unlock()
		case <-ctxDone:
		}
	}()

	for i := 0; i < len(remainingChunks); i++ {
		chunk := remainingChunks[i]
		mapMu.Lock()
		for {
			if ctx.Err() != nil {
				mapMu.Unlock()
				return ctx.Err()
			}
			if workerErr != nil {
				mapMu.Unlock()
				return workerErr
			}
			if data, ok := resMap[chunk.ChunkIndex]; ok {
				mapMu.Unlock()

				cStart := chunk.ByteOffset
				cEnd := cStart + int64(chunk.ChunkSize) - 1

				sStart := int64(0)
				if start > cStart {
					sStart = start - cStart
				}

				sEnd := int64(len(data))
				if end < cEnd {
					sEnd = end - cStart + 1
				}

				if sStart < int64(len(data)) && sEnd <= int64(len(data)) && sStart < sEnd {
					if _, err := w.Write(data[sStart:sEnd]); err != nil {
						return err
					}
				}

				mapMu.Lock()
				delete(resMap, chunk.ChunkIndex)
				mapMu.Unlock()
				break
			}
			cond.Wait()
		}
	}

	wg.Wait()
	return nil
}

// GetFileBytes returns the whole decrypted file in memory. It exists for the
// thumbnailer, which must decode the image to scale it; the browser-facing
// download paths stream instead so they never hold a large file at once. Callers
// are expected to have bounded the size first.
func (e *Engine) GetFileBytes(ctx context.Context, fileID string) ([]byte, error) {
	e.mu.RLock()
	key := e.masterKey
	e.mu.RUnlock()
	if len(key) != crypto.KeyLength {
		return nil, errors.New("drive is locked")
	}

	fileRec, err := e.db.GetFile(fileID)
	if err != nil || fileRec == nil {
		return nil, fmt.Errorf("file not found %w", err)
	}
	allChunks, err := e.db.GetChunksForFile(fileID)
	if err != nil || len(allChunks) == 0 {
		return nil, fmt.Errorf("no uploaded parts found for %s %w", fileRec.Name, err)
	}

	// One row per chunk index; a part replicated to several servers is fetched
	// once. Order by index so the concatenation is the original byte stream.
	byIndex := make(map[int]db.ChunkRecord, len(allChunks))
	maxIdx := -1
	for _, ch := range allChunks {
		if _, seen := byIndex[ch.ChunkIndex]; !seen {
			byIndex[ch.ChunkIndex] = ch
			if ch.ChunkIndex > maxIdx {
				maxIdx = ch.ChunkIndex
			}
		}
	}
	out := make([]byte, 0, fileRec.Size)
	for i := 0; i <= maxIdx; i++ {
		ch, ok := byIndex[i]
		if !ok {
			return nil, fmt.Errorf("missing part %d of %s", i, fileRec.Name)
		}
		dec, err := e.fetchChunk(ctx, fileID, ch, key)
		if err != nil {
			return nil, err
		}
		out = append(out, dec...)
	}
	return out, nil
}

func (e *Engine) StreamDownload(ctx context.Context, fileID string, w io.Writer) error {
	e.mu.RLock()
	key := e.masterKey
	e.mu.RUnlock()

	if len(key) != crypto.KeyLength {
		return errors.New("please enter your master password in settings first")
	}

	fileRec, err := e.db.GetFile(fileID)
	if err != nil {
		return fmt.Errorf("file not found %w", err)
	}

	if fileRec.IsDir {
		return e.StreamZip(ctx, fileID, w)
	}

	allChunks, err := e.db.GetChunksForFile(fileID)
	if err != nil || len(allChunks) == 0 {
		return fmt.Errorf("no uploaded parts found for %s %w", fileRec.Name, err)
	}

	chunkMap := make(map[int]db.ChunkRecord)
	for _, ch := range allChunks {
		if _, exists := chunkMap[ch.ChunkIndex]; !exists {
			chunkMap[ch.ChunkIndex] = ch
		}
	}
	var chunks []db.ChunkRecord
	for i := 0; i < len(chunkMap); i++ {
		if ch, ok := chunkMap[i]; ok {
			chunks = append(chunks, ch)
		}
	}

	log.Printf("streaming %s (%.2f MB, %d parts)", fileRec.Name, float64(fileRec.Size)/(1024*1024), len(chunks))

	if len(chunks) == 1 {
		decryptedBytes, err := e.fetchChunk(ctx, fileRec.ID, chunks[0], key)
		if err != nil {
			log.Printf("single part fetch failed for %s: %v", fileRec.Name, err)
			return err
		}
		_, err = w.Write(decryptedBytes)
		if err == nil {
			log.Printf("sent %s to browser", fileRec.Name)
		}
		return err
	}

	workerLimit := e.workerCount
	if workerLimit > len(chunks) {
		workerLimit = len(chunks)
	}
	if workerLimit < 1 {
		workerLimit = 1
	}

	chunkChan := make(chan db.ChunkRecord, len(chunks))
	for _, ch := range chunks {
		chunkChan <- ch
	}
	close(chunkChan)

	resMap := make(map[int][]byte)
	var mapMu sync.Mutex
	cond := sync.NewCond(&mapMu)
	var workerErr error

	var wg sync.WaitGroup
	for wIdx := 0; wIdx < workerLimit; wIdx++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case ch, ok := <-chunkChan:
					if !ok {
						return
					}

					decryptedBytes, err := e.fetchChunk(ctx, fileRec.ID, ch, key)
					if err != nil {
						mapMu.Lock()
						workerErr = err
						cond.Broadcast()
						mapMu.Unlock()
						log.Printf("part %d fetch failed for %s: %v", ch.ChunkIndex+1, fileRec.Name, err)
						return
					}

					mapMu.Lock()
					resMap[ch.ChunkIndex] = decryptedBytes
					cond.Broadcast()
					mapMu.Unlock()
				}
			}
		}()
	}

	ctxDone := make(chan struct{})
	defer close(ctxDone)
	go func() {
		select {
		case <-ctx.Done():
			mapMu.Lock()
			if workerErr == nil {
				workerErr = ctx.Err()
			}
			cond.Broadcast()
			mapMu.Unlock()
		case <-ctxDone:
		}
	}()

	for i := 0; i < len(chunks); i++ {
		mapMu.Lock()
		for {
			if ctx.Err() != nil {
				mapMu.Unlock()
				return ctx.Err()
			}
			if workerErr != nil {
				mapMu.Unlock()
				return workerErr
			}
			if data, ok := resMap[i]; ok {
				mapMu.Unlock()
				if _, err := w.Write(data); err != nil {
					log.Printf("write interrupted on part %d: %v", i+1, err)
					return fmt.Errorf("browser write error on part %d %w", i, err)
				}
				log.Printf("part %d/%d sent (%.2f MB)", i+1, len(chunks), float64(len(data))/(1024*1024))
				mapMu.Lock()
				delete(resMap, i)
				mapMu.Unlock()
				break
			}
			cond.Wait()
		}
	}

	wg.Wait()
	log.Printf("finished streaming %s", fileRec.Name)
	return nil
}

func (e *Engine) StreamZip(ctx context.Context, folderID string, w io.Writer) error {
	folderRec, err := e.db.GetFile(folderID)
	if err != nil {
		return fmt.Errorf("folder not found %w", err)
	}

	allFiles, err := e.db.GetAllFiles()
	if err != nil {
		return fmt.Errorf("could not list files %w", err)
	}

	basePath := folderRec.Path
	if !strings.HasSuffix(basePath, "/") {
		basePath += "/"
	}

	zipWriter := zip.NewWriter(w)
	defer zipWriter.Close()

	for _, file := range allFiles {
		if file.IsDir {
			continue
		}
		if !strings.HasPrefix(file.Path, basePath) {
			continue
		}

		relPath := strings.TrimPrefix(file.Path, basePath)
		relPath = filepath.ToSlash(relPath)

		header := &zip.FileHeader{
			Name:     relPath,
			Method:   zip.Deflate,
			Modified: time.Unix(file.ModTime, 0),
		}

		entryWriter, err := zipWriter.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("could not create zip entry for %s %w", relPath, err)
		}

		if err := e.StreamDownload(ctx, file.ID, entryWriter); err != nil {
			return fmt.Errorf("could not stream file %s into zip %w", relPath, err)
		}
	}

	return nil
}

func (e *Engine) DownloadFile(ctx context.Context, fileID, localDestPath string) (string, error) {
	e.mu.RLock()
	key := e.masterKey
	e.mu.RUnlock()

	if len(key) != crypto.KeyLength {
		return "", errors.New("please enter your master password in settings first")
	}

	fileRec, err := e.db.GetFile(fileID)
	if err != nil {
		return "", fmt.Errorf("file not found %w", err)
	}

	if fileRec.IsDir {
		return "", errors.New("cannot download a folder as a single file")
	}

	destDir := filepath.Dir(localDestPath)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", fmt.Errorf("could not create destination folder %w", err)
	}

	diskSpace, err := syswin.GetDiskFreeSpace(destDir)
	if err == nil {
		requiredBytes := uint64(fileRec.Size) + DiskSafetyMarginBytes
		if diskSpace.AvailableBytes < requiredBytes {
			return "", fmt.Errorf("not enough disk space need %.2f MB but have %.2f MB",
				float64(requiredBytes)/(1024*1024), float64(diskSpace.AvailableBytes)/(1024*1024))
		}
	}

	chunks, err := e.db.GetChunksForFile(fileID)
	if err != nil || len(chunks) == 0 {
		return "", fmt.Errorf("no uploaded parts found for %s %w", fileRec.Name, err)
	}

	jobID := uuid.New().String()
	jobRec := &db.JobRecord{
		ID:              jobID,
		Type:            "DOWNLOAD",
		FileID:          fileRec.ID,
		FilePath:        localDestPath,
		TotalBytes:      fileRec.Size,
		TotalChunks:     len(chunks),
		CompletedChunks: 0,
		Status:          db.JobStatusActive,
		CreatedAt:       time.Now().Unix(),
	}
	_ = e.db.CreateJob(jobRec)

	jobCtx, cancel := context.WithCancel(ctx)
	tracker := &activeDownload{
		jobID:          jobID,
		fileID:         fileRec.ID,
		cancel:         cancel,
		processedBytes: 0,
		lastBytes:      0,
		lastTime:       time.Now(),
		startTime:      time.Now(),
	}

	e.mu.Lock()
	e.activeJobs[jobID] = tracker
	e.mu.Unlock()

	go e.runDownload(jobCtx, tracker, jobRec, fileRec, chunks, localDestPath, key)

	return jobID, nil
}

func (e *Engine) runDownload(ctx context.Context, tracker *activeDownload, job *db.JobRecord, file *db.FileRecord, chunks []db.ChunkRecord, localDestPath string, key []byte) {
	defer func() {
		e.mu.Lock()
		delete(e.activeJobs, job.ID)
		e.mu.Unlock()
	}()

	normDest := syswin.NormalizeLongPath(localDestPath)
	outFile, err := os.OpenFile(normDest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
	if err != nil {
		_ = e.db.UpdateJobStatus(job.ID, db.JobStatusFailed)
		e.broadcast(uploader.TelemetryEvent{
			JobID:  job.ID,
			FileID: file.ID,
			Status: db.JobStatusFailed,
			Error:  fmt.Sprintf("could not create output file %v", err),
		})
		return
	}
	defer outFile.Close()

	_ = outFile.Truncate(file.Size)

	chunkChan := make(chan db.ChunkRecord, len(chunks))
	for _, ch := range chunks {
		chunkChan <- ch
	}
	close(chunkChan)

	var wg sync.WaitGroup
	errChan := make(chan error, e.workerCount)
	var completedCount int64 = 0

	workers := e.workerCount
	if workers > len(chunks) {
		workers = len(chunks)
	}
	if workers < 1 {
		workers = 1
	}

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for {
				select {
				case <-ctx.Done():
					return
				case chunk, ok := <-chunkChan:
					if !ok {
						return
					}

					atomic.AddInt32(&tracker.activeWorkers, 1)

					decryptedBytes, err := e.fetchChunk(ctx, file.ID, chunk, key)
					if err != nil {
						atomic.AddInt32(&tracker.activeWorkers, -1)
						errChan <- err
						return
					}

					_, err = outFile.WriteAt(decryptedBytes, chunk.ByteOffset)
					if err != nil {
						atomic.AddInt32(&tracker.activeWorkers, -1)
						errChan <- fmt.Errorf("write error on part %d %w", chunk.ChunkIndex, err)
						return
					}

					curDone := atomic.AddInt64(&completedCount, 1)
					atomic.AddInt64(&tracker.processedBytes, int64(len(decryptedBytes)))
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

					e.broadcast(uploader.TelemetryEvent{
						JobID:           job.ID,
						FileID:          file.ID,
						FileName:        file.Name,
						Type:            "DOWNLOAD",
						TotalBytes:      job.TotalBytes,
						ProcessedBytes:  curBytes,
						TotalChunks:     job.TotalChunks,
						CompletedChunks: int(curDone),
						SpeedMBs:        speedMBs,
						ETASeconds:      etaSec,
						ActiveWorkers:   int(atomic.LoadInt32(&tracker.activeWorkers)),
						ProgressPercent: progressPct,
						Status:          db.JobStatusActive,
					})
				}
			}
		}(w)
	}

	wg.Wait()
	close(errChan)

	if err, ok := <-errChan; ok && err != nil {
		_ = e.db.UpdateJobStatus(job.ID, db.JobStatusFailed)
		e.broadcast(uploader.TelemetryEvent{
			JobID:  job.ID,
			FileID: file.ID,
			Status: db.JobStatusFailed,
			Error:  err.Error(),
		})
		return
	}

	if file.SHA256 != "" {
		_ = outFile.Sync()
		_, _ = outFile.Seek(0, 0)
		calculatedHash, err := crypto.ComputeStreamSHA256(outFile)
		if err == nil && calculatedHash != file.SHA256 {
			_ = e.db.UpdateJobStatus(job.ID, db.JobStatusFailed)
			e.broadcast(uploader.TelemetryEvent{
				JobID:  job.ID,
				FileID: file.ID,
				Status: db.JobStatusFailed,
				Error:  fmt.Sprintf("file verification mismatch expected %s got %s", file.SHA256, calculatedHash),
			})
			return
		}
	}

	if file.ModTime > 0 {
		mTime := time.Unix(file.ModTime, 0)
		_ = os.Chtimes(normDest, mTime, mTime)
	}

	_ = e.db.UpdateJobStatus(job.ID, db.JobStatusCompleted)
	e.broadcast(uploader.TelemetryEvent{
		JobID:           job.ID,
		FileID:          file.ID,
		FileName:        file.Name,
		Type:            "DOWNLOAD",
		TotalBytes:      job.TotalBytes,
		ProcessedBytes:  job.TotalBytes,
		TotalChunks:     job.TotalChunks,
		CompletedChunks: job.TotalChunks,
		SpeedMBs:        0,
		ETASeconds:      0,
		ActiveWorkers:   0,
		ProgressPercent: 100.0,
		Status:          db.JobStatusCompleted,
	})
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
