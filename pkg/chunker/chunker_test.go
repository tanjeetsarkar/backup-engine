package chunker

import (
	"bytes"
	"io"
	"math/rand"
	"testing"
)

// syntheticData returns deterministic pseudo-random bytes so that both the "before" and "after"
// runs of BenchmarkFastCDCThroughput operate on identical content.
func syntheticData(size int) []byte {
	data := make([]byte, size)
	_, _ = rand.New(rand.NewSource(1)).Read(data)
	return data
}

// BenchmarkFastCDCThroughput measures sustained chunking throughput (bytes/sec via b.SetBytes)
// against synthetic random data, establishing a baseline for the roadmap's >1.5GB/s/core target
// before any allocation-reduction work.
func BenchmarkFastCDCThroughput(b *testing.B) {
	const dataSize = 64 * 1024 * 1024 // 64 MiB
	data := syntheticData(dataSize)

	b.ResetTimer()
	b.SetBytes(int64(dataSize))
	for i := 0; i < b.N; i++ {
		c := NewFastCDC(bytes.NewReader(data))
		for {
			_, err := c.NextChunk()
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatalf("NextChunk: %v", err)
			}
		}
	}
}
