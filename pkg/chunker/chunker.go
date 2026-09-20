package chunker

import (
	"io"
)

const (
	MinChunkSize    = 256 * 1024      // 256 KiB
	TargetChunkSize = 1024 * 1024     // 1 MiB
	MaxChunkSize    = 4 * 1024 * 1024 // 4 MiB

	// Normalized mask values for FastCDC
	MaskStrict = 0x001FFFFF // 21 bits (Min to Target)
	MaskNormal = 0x0007FFFF // 19 bits (Target to Max)
)

// Gear lookup table initialized with pseudo-random 32-bit integers
var gearTable [256]uint32

func init() {
	// Linear congruential generator to populate deterministic gear table
	var state uint32 = 0x12345678
	for i := 0; i < 256; i++ {
		state = state*1664525 + 1013904223
		gearTable[i] = state
	}
}

// Chunk represents an emitted variable-length slice of data.
type Chunk struct {
	Data   []byte
	Length int
	Offset int64
}

// FastCDC implements normalized content-defined chunking over an io.Reader.
type FastCDC struct {
	reader     io.Reader
	buffer     []byte
	bufStart   int
	bufEnd     int
	streamPos  int64
	sourceDone bool
	chunk      Chunk
}

// NewFastCDC instantiates an optimized FastCDC chunker.
func NewFastCDC(r io.Reader) *FastCDC {
	return &FastCDC{
		reader: r,
		buffer: make([]byte, MaxChunkSize*2),
	}
}

// NextChunk cuts and returns the next content-defined chunk.
//
// The returned Chunk.Data aliases the chunker's internal buffer and is only valid until the next
// call to NextChunk; copy it if it must outlive that call.
func (c *FastCDC) NextChunk() (*Chunk, error) {
	// Replenish internal buffer if remaining bytes fall below MaxChunkSize.
	available := c.bufEnd - c.bufStart
	if available < MaxChunkSize && !c.sourceDone {
		c.shiftAndRefill()
		available = c.bufEnd - c.bufStart
	}

	if available == 0 {
		return nil, io.EOF
	}

	// Handle remaining tail bytes when stream completes.
	if available <= MinChunkSize {
		chunkData := c.buffer[c.bufStart:c.bufEnd]
		chunkOffset := c.streamPos

		c.streamPos += int64(available)
		c.bufStart = c.bufEnd

		c.chunk = Chunk{Data: chunkData, Length: available, Offset: chunkOffset}
		return &c.chunk, nil
	}

	// Execute FastCDC normalized gear hashing.
	cutPoint := c.findCutPoint(available)
	chunkData := c.buffer[c.bufStart : c.bufStart+cutPoint]
	chunkOffset := c.streamPos

	c.bufStart += cutPoint
	c.streamPos += int64(cutPoint)

	c.chunk = Chunk{Data: chunkData, Length: cutPoint, Offset: chunkOffset}
	return &c.chunk, nil
}

// findCutPoint determines the chunk boundary using normalized gear hashing.
func (c *FastCDC) findCutPoint(available int) int {
	maxScan := MaxChunkSize
	if available < maxScan {
		maxScan = available
	}

	if maxScan <= MinChunkSize {
		return maxScan
	}

	var fingerPrint uint32 = 0
	i := MinChunkSize

	// Phase 1: Scan MinChunkSize to TargetChunkSize using strict mask
	targetScan := TargetChunkSize
	if maxScan < targetScan {
		targetScan = maxScan
	}

	for ; i < targetScan; i++ {
		b := c.buffer[c.bufStart+i]
		fingerPrint = (fingerPrint << 1) + gearTable[b]
		if (fingerPrint & MaskStrict) == 0 {
			return i
		}
	}

	// Phase 2: Scan TargetChunkSize to MaxChunkSize using normal mask
	for ; i < maxScan; i++ {
		b := c.buffer[c.bufStart+i]
		fingerPrint = (fingerPrint << 1) + gearTable[b]
		if (fingerPrint & MaskNormal) == 0 {
			return i
		}
	}

	return maxScan
}

func (c *FastCDC) shiftAndRefill() {
	// Preserve unconsumed buffer content
	remaining := c.bufEnd - c.bufStart
	if remaining > 0 && c.bufStart > 0 {
		copy(c.buffer, c.buffer[c.bufStart:c.bufEnd])
	}
	c.bufStart = 0
	c.bufEnd = remaining

	// Fill buffer from source reader
	n, err := c.reader.Read(c.buffer[c.bufEnd:])
	if n > 0 {
		c.bufEnd += n
	}
	if err != nil {
		c.sourceDone = true
	}
}
