package chunker

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
)

const (
	DefaultMinSize = 8 * 1024 * 1024
	DefaultAvgSize = 20 * 1024 * 1024
	DefaultMaxSize = 24 * 1024 * 1024

	MaskBits = 24
)

var gearTable [256]uint64

func init() {
	var g uint64 = 0x9E3779B97F4A7C15
	for i := 0; i < 256; i++ {
		g = (g * 0x5851F42D4C957F2D) + 0x14057B7E76E1149B
		gearTable[i] = g
	}
}

type Chunk struct {
	Index      int
	Offset     int64
	Length     int
	Data       []byte
	SHA256Hash string
}

type FastCDC struct {
	minSize int
	avgSize int
	maxSize int
	mask    uint64
}

func NewFastCDC(minSize, avgSize, maxSize int) *FastCDC {
	if minSize <= 0 {
		minSize = DefaultMinSize
	}
	if avgSize <= minSize {
		avgSize = DefaultAvgSize
	}
	if maxSize <= avgSize {
		maxSize = DefaultMaxSize
	}

	return &FastCDC{
		minSize: minSize,
		avgSize: avgSize,
		maxSize: maxSize,
		mask:    (1 << MaskBits) - 1,
	}
}

func (c *FastCDC) NextCut(data []byte) int {
	n := len(data)
	if n <= c.minSize {
		return n
	}
	if n > c.maxSize {
		n = c.maxSize
	}

	var fingerprint uint64 = 0
	for i := c.minSize; i < n; i++ {
		fingerprint = (fingerprint << 1) + gearTable[data[i]]
		if (fingerprint & c.mask) == 0 {
			return i + 1
		}
	}

	return n
}

func (c *FastCDC) Split(r io.Reader) ([]Chunk, error) {
	buf := make([]byte, c.maxSize*2)
	var chunks []Chunk
	var totalOffset int64 = 0
	bufLen := 0
	chunkIndex := 0

	for {
		n, err := io.ReadFull(r, buf[bufLen:cap(buf)])
		bufLen += n

		if bufLen == 0 {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return nil, err
		}

		cut := c.NextCut(buf[:bufLen])
		chunkData := make([]byte, cut)
		copy(chunkData, buf[:cut])

		h := sha256.Sum256(chunkData)
		hashStr := hex.EncodeToString(h[:])

		chunks = append(chunks, Chunk{
			Index:      chunkIndex,
			Offset:     totalOffset,
			Length:     cut,
			Data:       chunkData,
			SHA256Hash: hashStr,
		})

		chunkIndex++
		totalOffset += int64(cut)

		copy(buf, buf[cut:bufLen])
		bufLen -= cut

		if err == io.EOF || err == io.ErrUnexpectedEOF {
			for bufLen > 0 {
				remainCut := c.NextCut(buf[:bufLen])
				remData := make([]byte, remainCut)
				copy(remData, buf[:remainCut])

				rh := sha256.Sum256(remData)
				rHashStr := hex.EncodeToString(rh[:])

				chunks = append(chunks, Chunk{
					Index:      chunkIndex,
					Offset:     totalOffset,
					Length:     remainCut,
					Data:       remData,
					SHA256Hash: rHashStr,
				})

				chunkIndex++
				totalOffset += int64(remainCut)
				copy(buf, buf[remainCut:bufLen])
				bufLen -= remainCut
			}
			break
		}
	}

	return chunks, nil
}
