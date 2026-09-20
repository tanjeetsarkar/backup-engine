package pipeline

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/tanjeetsarkar/backup-engine/pkg/index"
)

// BenchmarkBackupNewChunksInLargeRepo measures backing up entirely new content against a
// repository that already has many unrelated chunk/CID mappings, the scenario the dedup bloom
// filter targets: every new chunk is a definite miss that should short-circuit the bbolt lookup
// instead of paying for a real B-tree read.
func BenchmarkBackupNewChunksInLargeRepo(b *testing.B) {
	const preexistingChunks = 20_000
	const fileSize = 8 * 1024 * 1024 // 8 MiB of new content per iteration

	repo := b.TempDir()
	engine, err := Open(EngineConfig{RepoDir: repo, Passphrase: []byte("passphrase"), Salt: []byte("0123456789abcdef")})
	if err != nil {
		b.Fatal(err)
	}
	defer engine.Close()

	mappings := make([]index.ChunkMapping, 0, preexistingChunks)
	for i := 0; i < preexistingChunks; i++ {
		var cid, sid [32]byte
		_, _ = rand.Read(cid[:])
		_, _ = rand.Read(sid[:])
		mappings = append(mappings, index.ChunkMapping{CID: cid, StorageID: sid, Location: index.ChunkLocation{PackID: sid, Offset: 0, Length: 1}})
	}
	if err := engine.idx.PutChunkMappings(mappings); err != nil {
		b.Fatal(err)
	}
	// Rebuild the dedup filter now that the index has the large pre-existing chunk set, matching
	// what a fresh Open() against this repository would compute.
	filter, err := buildDedupFilter(engine.idx)
	if err != nil {
		b.Fatal(err)
	}
	engine.dedupFilter = filter

	source := b.TempDir()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		data := make([]byte, fileSize)
		_, _ = rand.Read(data)
		path := filepath.Join(source, "bench.bin")
		if err := os.WriteFile(path, data, 0640); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()

		result, err := engine.BackupPathDetailed(context.Background(), source, nil, []string{"BENCH"}, nil)
		if err != nil {
			b.Fatal(err)
		}
		b.SetBytes(result.LogicalBytes)
	}
}
