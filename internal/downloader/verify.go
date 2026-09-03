package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"

	"discord-free-cloud/internal/crypto"
	"discord-free-cloud/internal/db"
)

type ChunkVerdict struct {
	Index    int    `json:"index"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Size     int    `json:"size"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

type VerificationResult struct {
	OK           bool           `json:"ok"`
	FileID       string         `json:"file_id"`
	Name         string         `json:"name"`
	ExpectedSize int64          `json:"expected_size"`
	ActualSize   int64          `json:"actual_size"`
	ExpectedHash string         `json:"expected_hash"`
	ActualHash   string         `json:"actual_hash"`
	Chunks       []ChunkVerdict `json:"chunks"`
	Error        string         `json:"error,omitempty"`
}

// VerifyFile re-fetches every completed chunk, decrypts it, and checks the
// plaintext SHA-256 against the recorded chunk hash. It also reassembles the
// decrypted stream to confirm the whole-file SHA-256 and byte length. This is
// the "is my backup actually recoverable" check that the backup tooling and the
// dashboard both need. Read scope is enough: nothing is written to Discord or
// the file.
func (e *Engine) VerifyFile(ctx context.Context, fileID string) (*VerificationResult, error) {
	e.mu.RLock()
	key := e.masterKey
	e.mu.RUnlock()

	res := &VerificationResult{FileID: fileID}
	if len(key) != crypto.KeyLength {
		return nil, errors.New("drive is locked — unlock with the master password first")
	}

	fileRec, err := e.db.GetFile(fileID)
	if err != nil || fileRec == nil {
		return nil, fmt.Errorf("file not found %w", err)
	}
	if fileRec.IsDir {
		return nil, errors.New("folders cannot be verified directly")
	}
	res.Name = fileRec.Name
	res.ExpectedSize = fileRec.Size
	res.ExpectedHash = fileRec.SHA256

	chunks, err := completedChunksForFile(e.db, fileID)
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 {
		return nil, errors.New("no completed parts found for this file")
	}

	verdicts := make([]ChunkVerdict, len(chunks))
	fetched := make([][]byte, len(chunks))
	var wg sync.WaitGroup
	work := make(chan int, len(chunks))
	for i := range chunks {
		work <- i
	}
	close(work)

	workers := e.workerCount
	if workers < 1 {
		workers = 1
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case i, ok := <-work:
					if !ok {
						return
					}
					ch := chunks[i]
					v := ChunkVerdict{Index: ch.ChunkIndex, Expected: ch.SHA256, Size: ch.ChunkSize}
					dec, ferr := e.fetchChunk(ctx, fileID, ch, key)
					if ferr != nil {
						v.Error = ferr.Error()
					} else {
						sum := sha256.Sum256(dec)
						v.Actual = hex.EncodeToString(sum[:])
						v.OK = ch.SHA256 == "" || v.Actual == ch.SHA256
						if !v.OK {
							v.Error = "checksum mismatch"
						}
						fetched[i] = dec
					}
					verdicts[i] = v
				}
			}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	res.Chunks = verdicts
	allOK := true
	h := sha256.New()
	var total int64
	for i, v := range verdicts {
		if !v.OK {
			allOK = false
			continue
		}
		if _, werr := h.Write(fetched[i]); werr != nil {
			return nil, werr
		}
		total += int64(len(fetched[i]))
	}
	res.ActualSize = total
	res.ActualHash = hex.EncodeToString(h.Sum(nil))
	if fileRec.SHA256 != "" && res.ActualHash != fileRec.SHA256 {
		allOK = false
	}
	if total != fileRec.Size {
		allOK = false
		res.Error = fmt.Sprintf("size mismatch: expected %d, got %d", fileRec.Size, total)
	}
	res.OK = allOK
	return res, nil
}

func completedChunksForFile(database *db.Database, fileID string) ([]db.ChunkRecord, error) {
	all, err := database.GetChunksForFile(fileID)
	if err != nil {
		return nil, err
	}
	byIndex := make(map[int]db.ChunkRecord)
	for _, ch := range all {
		if ch.Status != db.StatusCompleted {
			continue
		}
		if _, exists := byIndex[ch.ChunkIndex]; !exists {
			byIndex[ch.ChunkIndex] = ch
		}
	}
	out := make([]db.ChunkRecord, 0, len(byIndex))
	for _, ch := range byIndex {
		out = append(out, ch)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChunkIndex < out[j].ChunkIndex })
	for i := range out {
		if out[i].ChunkIndex != i {
			return nil, fmt.Errorf("missing chunk %d (parts are not contiguous)", i)
		}
	}
	return out, nil
}
