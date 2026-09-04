package server

// thumb.go adds GET /api/thumb: a small JPEG rendition of a stored image.
// Before this, every 28px list icon and 176px grid tile pulled the FULL file
// through Discord and decrypted it in the browser, so a folder of photos cost
// hundreds of megabytes of shard fetches just to paint. The server pays the
// Discord fetch once per file (the downloader's chunk cache absorbs repeats),
// scales it down with the standard library, and hands the browser a ~30KB JPEG
// it can cache for a day.

import (
	"bytes"
	"context"
	"image"
	_ "image/gif" // decode support for .gif shards
	"image/jpeg"
	_ "image/png" // decode support for .png shards
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	thumbMaxEdge     = 320              // longest side of the generated JPEG
	thumbMaxSrcBytes = 12 * 1024 * 1024 // refuse to decode absurd sources
	thumbMaxPixels   = 25_000_000       // 25M px is 100 MB of RGBA: the most this box should ever decode for one thumbnail
	thumbCacheTTL    = time.Hour
	thumbCacheCap    = 96
)

type thumbEntry struct {
	data  []byte
	at    time.Time
	modAt int64
}

type thumbCache struct {
	mu    sync.Mutex
	items map[string]*thumbEntry
	order []string
}

func (c *thumbCache) get(key string, modAt int64) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok || e.modAt != modAt || time.Since(e.at) > thumbCacheTTL {
		return nil, false
	}
	return e.data, true
}

func (c *thumbCache) put(key string, modAt int64, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[key]; !exists {
		if len(c.order) >= thumbCacheCap {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.items, oldest)
		}
		c.order = append(c.order, key)
	}
	c.items[key] = &thumbEntry{data: data, at: time.Now(), modAt: modAt}
}

var thumbs = &thumbCache{items: map[string]*thumbEntry{}}

// thumbGen limits how many thumbnails are decoded+scaled at once. This box is
// CPU-starved and reboots under load; a folder of fifty photos must not fire
// fifty full-image decodes simultaneously just because the browser requested
// them together. Excess requests queue on the semaphore (bounded by the
// per-request context deadline) instead of melting the host.
var thumbGen = make(chan struct{}, 2)

// handleThumb serves GET /api/thumb?file_id=… as image/jpeg. Non-images and
// anything that fails to decode answer 404; the frontend treats that as "keep
// the type icon", so a bad shard never paints a broken image.
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	fileID := r.URL.Query().Get("file_id")
	if fileID == "" {
		http.Error(w, "file_id is required", http.StatusBadRequest)
		return
	}
	fileRec, err := s.db.GetFile(fileID)
	if err != nil || fileRec == nil || fileRec.IsDir {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	switch strings.ToLower(filepath.Ext(fileRec.Name)) {
	case ".jpg", ".jpeg", ".png", ".gif":
	default:
		http.Error(w, "not a thumbnailable image", http.StatusNotFound)
		return
	}
	if fileRec.Size <= 0 || fileRec.Size > thumbMaxSrcBytes {
		http.Error(w, "too large", http.StatusNotFound)
		return
	}
	if !s.ensureMasterKeyUnlocked() {
		http.Error(w, "locked", http.StatusUnauthorized)
		return
	}

	cacheKey := fileID
	if data, ok := thumbs.get(cacheKey, fileRec.ModTime); ok {
		serveThumb(w, data)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	raw, err := s.downloader.GetFileBytes(ctx, fileID)
	if err != nil || len(raw) == 0 {
		http.Error(w, "could not read file", http.StatusBadGateway)
		return
	}

	// Acquire a generation slot only for the CPU-bound decode/scale/encode. The
	// shard fetch above is I/O and already bounded by the downloader's own
	// workers; holding a slot across it would starve the queue on a slow read.
	select {
	case thumbGen <- struct{}{}:
		defer func() { <-thumbGen }()
	case <-ctx.Done():
		http.Error(w, "thumbnail queue full", http.StatusServiceUnavailable)
		return
	}

	// Check the header dimensions BEFORE allocating the decoded image: a small
	// compressed PNG can expand to billions of pixels, and on this box a decode
	// bomb would OOM the process before any post-decode guard could run.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > thumbMaxPixels {
		http.Error(w, "image too large to scale", http.StatusUnprocessableEntity)
		return
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		http.Error(w, "could not decode image", http.StatusUnprocessableEntity)
		return
	}
	out := scaleToFit(src, thumbMaxEdge)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 72}); err != nil {
		log.Printf("thumb encode failed for %s: %v", fileRec.Name, err)
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	thumbs.put(cacheKey, fileRec.ModTime, buf.Bytes())
	serveThumb(w, buf.Bytes())
}

func serveThumb(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(data)
}

// scaleToFit downscales with a box filter (area averaging), which is the right
// kernel for shrinking and stays sharp where nearest would alias. It only ever
// shrinks; small images pass through untouched.
func scaleToFit(src image.Image, maxEdge int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxEdge && h <= maxEdge {
		return src
	}
	nw, nh := w, h
	if w >= h {
		nw = maxEdge
		nh = maxEdge * h / w
	} else {
		nh = maxEdge
		nw = maxEdge * w / h
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	sx := float64(w) / float64(nw)
	sy := float64(h) / float64(nh)
	for y := 0; y < nh; y++ {
		y0 := b.Min.Y + int(float64(y)*sy)
		y1 := b.Min.Y + int(float64(y+1)*sy)
		if y1 <= y0 {
			y1 = y0 + 1
		}
		if y1 > b.Max.Y {
			y1 = b.Max.Y
		}
		for x := 0; x < nw; x++ {
			x0 := b.Min.X + int(float64(x)*sx)
			x1 := b.Min.X + int(float64(x+1)*sx)
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if x1 > b.Max.X {
				x1 = b.Max.X
			}
			var rs, gs, bs, as uint64
			n := 0
			for sy2 := y0; sy2 < y1; sy2++ {
				for sx2 := x0; sx2 < x1; sx2++ {
					r, g, bb, a := src.At(sx2, sy2).RGBA()
					rs += uint64(r)
					gs += uint64(g)
					bs += uint64(bb)
					as += uint64(a)
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			off := dst.PixOffset(x, y)
			dst.Pix[off+0] = uint8(rs / uint64(n) >> 8)
			dst.Pix[off+1] = uint8(gs / uint64(n) >> 8)
			dst.Pix[off+2] = uint8(bs / uint64(n) >> 8)
			dst.Pix[off+3] = uint8(as / uint64(n) >> 8)
		}
	}
	return dst
}
