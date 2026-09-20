package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkBackupPathDetailed measures end-to-end chunk+compress+encrypt+pack throughput against
// local storage, establishing a baseline before any chunker/dedup optimization work.
func BenchmarkBackupPathDetailed(b *testing.B) {
	const fileSize = 16 * 1024 * 1024 // 16 MiB, larger than one default 16 MiB pack target
	data := make([]byte, fileSize)
	for i := range data {
		data[i] = byte(i * 2654435761 >> 13)
	}

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		repo := b.TempDir()
		source := b.TempDir()
		if err := os.WriteFile(filepath.Join(source, "bench.bin"), data, 0640); err != nil {
			b.Fatal(err)
		}
		engine, err := Open(EngineConfig{RepoDir: repo, Passphrase: []byte("passphrase"), Salt: []byte("0123456789abcdef")})
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()

		result, err := engine.BackupPathDetailed(context.Background(), source, nil, []string{"BENCH"}, nil)
		if err != nil {
			b.Fatal(err)
		}
		b.SetBytes(result.LogicalBytes)

		b.StopTimer()
		engine.Close()
		b.StartTimer()
	}
}
