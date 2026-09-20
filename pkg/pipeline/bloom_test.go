package pipeline

import (
	"crypto/rand"
	"testing"
)

func randomCID(t *testing.T) [32]byte {
	t.Helper()
	var cid [32]byte
	if _, err := rand.Read(cid[:]); err != nil {
		t.Fatal(err)
	}
	return cid
}

func TestBloomFilterNoFalseNegatives(t *testing.T) {
	filter := newBloomFilter(1000)
	added := make([][32]byte, 0, 500)
	for i := 0; i < 500; i++ {
		cid := randomCID(t)
		filter.Add(cid)
		added = append(added, cid)
	}
	for _, cid := range added {
		if !filter.MightContain(cid) {
			t.Fatalf("false negative for added cid %x", cid)
		}
	}
}

func TestBloomFilterDefiniteMissesAreCommon(t *testing.T) {
	filter := newBloomFilter(1000)
	for i := 0; i < 500; i++ {
		filter.Add(randomCID(t))
	}
	misses := 0
	const trials = 2000
	for i := 0; i < trials; i++ {
		if !filter.MightContain(randomCID(t)) {
			misses++
		}
	}
	// At this fill ratio false positives should be rare; most never-added CIDs must report as
	// definite misses, or the filter would provide no benefit at all.
	if misses < trials*9/10 {
		t.Fatalf("expected at least 90%% definite misses, got %d/%d", misses, trials)
	}
}
