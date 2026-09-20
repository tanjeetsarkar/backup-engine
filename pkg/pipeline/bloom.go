package pipeline

import (
	"encoding/binary"

	"github.com/tanjeetsarkar/backup-engine/pkg/index"
)

// buildDedupFilter loads every existing CID from idx into a fresh bloomFilter, sized to the
// current mapping count.
func buildDedupFilter(idx *index.DB) (*bloomFilter, error) {
	mappings, err := idx.ListCIDMappings()
	if err != nil {
		return nil, err
	}
	filter := newBloomFilter(len(mappings))
	for _, mapping := range mappings {
		filter.Add(mapping.CID)
	}
	return filter, nil
}

// bloomFilter is a small fixed-size Bloom filter used only to short-circuit definite-miss
// deduplication lookups before falling through to the authoritative bbolt read. A CID's 32 bytes
// are already a uniformly distributed cryptographic hash, so its four 8-byte windows are reused
// directly as independent hash values instead of computing additional hashes. False positives fall
// through to the real lookup, so correctness never depends on the filter; it only skips work on a
// definite miss.
type bloomFilter struct {
	bits    []uint64
	numBits uint64
}

// newBloomFilter sizes the filter for expectedItems at roughly 10 bits/item and 4 hash functions,
// which keeps the false-positive rate low (~1-2%) without needing a dynamic resize strategy.
func newBloomFilter(expectedItems int) *bloomFilter {
	if expectedItems < 1024 {
		expectedItems = 1024
	}
	words := (uint64(expectedItems)*10 + 63) / 64
	return &bloomFilter{bits: make([]uint64, words), numBits: words * 64}
}

func (f *bloomFilter) hashes(cid [32]byte) [4]uint64 {
	var out [4]uint64
	for i := range out {
		out[i] = binary.LittleEndian.Uint64(cid[i*8 : i*8+8])
	}
	return out
}

// Add records cid as present.
func (f *bloomFilter) Add(cid [32]byte) {
	for _, h := range f.hashes(cid) {
		idx := h % f.numBits
		f.bits[idx/64] |= 1 << (idx % 64)
	}
}

// MightContain reports whether cid could be present. false is a definite negative; true may be a
// false positive.
func (f *bloomFilter) MightContain(cid [32]byte) bool {
	for _, h := range f.hashes(cid) {
		idx := h % f.numBits
		if f.bits[idx/64]&(1<<(idx%64)) == 0 {
			return false
		}
	}
	return true
}
