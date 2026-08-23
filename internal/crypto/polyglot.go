package crypto

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
)

var (
	pngMagic = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	ihdrType = []byte("IHDR")
	idatType = []byte("IDAT")
	ddatType = []byte("dDat")
	iendType = []byte("IEND")

	ihdrData = []byte{
		0x00, 0x00, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x01,
		0x08,
		0x06,
		0x00,
		0x00,
		0x00,
	}

	idatData = []byte{
		0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4,
	}
)

func writePNGChunk(buf *bytes.Buffer, chunkType []byte, data []byte) {
	var lengthBytes [4]byte
	binary.BigEndian.PutUint32(lengthBytes[:], uint32(len(data)))
	buf.Write(lengthBytes[:])

	buf.Write(chunkType)
	buf.Write(data)

	crcVal := crc32.ChecksumIEEE(chunkType)
	if len(data) > 0 {
		crcVal = crc32.Update(crcVal, crc32.IEEETable, data)
	}
	var crcBytes [4]byte
	binary.BigEndian.PutUint32(crcBytes[:], crcVal)
	buf.Write(crcBytes[:])
}

func WrapInPNGContainer(encryptedPayload []byte) []byte {
	var buf bytes.Buffer
	buf.Grow(len(pngMagic) + 33 + 30 + len(encryptedPayload) + 20)

	buf.Write(pngMagic)
	writePNGChunk(&buf, ihdrType, ihdrData)
	writePNGChunk(&buf, idatType, idatData)
	writePNGChunk(&buf, ddatType, encryptedPayload)
	writePNGChunk(&buf, iendType, nil)

	return buf.Bytes()
}

func UnwrapPNGContainer(data []byte) []byte {
	if len(data) < 8 || !bytes.Equal(data[:8], pngMagic) {
		return data
	}

	offset := 8
	var extracted []byte
	var firstSlice []byte
	matchCount := 0

	for offset+8 <= len(data) {
		chunkLen := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		chunkType := string(data[offset+4 : offset+8])
		chunkDataStart := offset + 8
		chunkDataEnd := chunkDataStart + chunkLen
		chunkCrcEnd := chunkDataEnd + 4

		if chunkCrcEnd > len(data) {
			break
		}

		if chunkType == "dDat" || chunkType == "caPt" || chunkType == "tEXt" {
			matchCount++
			if matchCount == 1 {
				firstSlice = data[chunkDataStart:chunkDataEnd]
			} else if matchCount == 2 {
				extracted = make([]byte, 0, len(firstSlice)+chunkLen*2)
				extracted = append(extracted, firstSlice...)
				extracted = append(extracted, data[chunkDataStart:chunkDataEnd]...)
			} else {
				extracted = append(extracted, data[chunkDataStart:chunkDataEnd]...)
			}
		}

		offset = chunkCrcEnd
	}

	if matchCount == 1 {
		return firstSlice
	}

	if len(extracted) > 0 {
		return extracted
	}

	return data
}
