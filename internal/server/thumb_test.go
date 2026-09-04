package server

import (
	"image"
	"image/color"
	"testing"
	"time"
)

func TestScaleToFitShrinksOnly(t *testing.T) {
	big := image.NewRGBA(image.Rect(0, 0, 2000, 1000))
	for y := 0; y < 1000; y++ {
		for x := 0; x < 2000; x++ {
			big.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 80, A: 255})
		}
	}
	out := scaleToFit(big, thumbMaxEdge)
	b := out.Bounds()
	if b.Dx() != thumbMaxEdge || b.Dy() != thumbMaxEdge*1000/2000 {
		t.Errorf("2000x1000 scaled to %dx%d, want %dx%d", b.Dx(), b.Dy(), thumbMaxEdge, thumbMaxEdge*1000/2000)
	}

	small := image.NewRGBA(image.Rect(0, 0, 100, 50))
	if got := scaleToFit(small, thumbMaxEdge); got != image.Image(small) {
		t.Error("an image already under the cap must pass through untouched")
	}
}

func TestScaleToFitAveragesBlock(t *testing.T) {
	// A 4x4 checkerboard: every 2x2 block holds two black and two white pixels,
	// so a correct box filter must land near grey at every output pixel. A
	// nearest-neighbour sampler would return pure black or pure white.
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if (x+y)%2 == 0 {
				src.Set(x, y, color.Black)
			} else {
				src.Set(x, y, color.White)
			}
		}
	}
	out := scaleToFit(src, 2)
	for _, p := range []image.Point{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
		r, _, _, _ := out.At(p.X, p.Y).RGBA()
		if r < 0x6000 || r > 0xA000 {
			t.Errorf("pixel %v averaged to %d, want mid-grey (~0x8000)", p, r)
		}
	}
}

func TestThumbCacheInvalidatesOnModTime(t *testing.T) {
	c := &thumbCache{items: map[string]*thumbEntry{}}
	c.put("f1", 100, []byte("old"))
	if _, ok := c.get("f1", 100); !ok {
		t.Fatal("fresh entry must hit")
	}
	if _, ok := c.get("f1", 200); ok {
		t.Error("a re-uploaded file (new mod time) must miss the cache")
	}
	c.put("f1", 200, []byte("new"))
	data, ok := c.get("f1", 200)
	if !ok || string(data) != "new" {
		t.Error("cache must hold the replacement")
	}
}

func TestThumbCacheEvictsOldest(t *testing.T) {
	c := &thumbCache{items: map[string]*thumbEntry{}}
	for i := 0; i < thumbCacheCap+5; i++ {
		c.put(string(rune('a'+i%26))+string(rune('A'+i)), int64(i), []byte("x"))
	}
	if len(c.items) > thumbCacheCap {
		t.Errorf("cache grew to %d entries, cap is %d", len(c.items), thumbCacheCap)
	}
}

func TestThumbCacheTTL(t *testing.T) {
	c := &thumbCache{items: map[string]*thumbEntry{}}
	c.put("f", 1, []byte("x"))
	c.mu.Lock()
	c.items["f"].at = time.Now().Add(-2 * thumbCacheTTL)
	c.mu.Unlock()
	if _, ok := c.get("f", 1); ok {
		t.Error("an entry older than the TTL must miss")
	}
}
