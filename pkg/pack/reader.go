package pack

import (
	"encoding/binary"
	"fmt"
)

const chunkRecordOverhead = 32 + packNonceSize + 4

// RecordSizeFromCipherLen returns the full on-disk chunk record size.
func RecordSizeFromCipherLen(cipherLen uint32) int64 {
	return int64(chunkRecordOverhead) + int64(cipherLen)
}

// DecodeChunkRecord decodes [StorageID][Nonce][CipherLen][Ciphertext] from raw bytes.
func DecodeChunkRecord(raw []byte) (storageID [32]byte, nonce []byte, ciphertext []byte, err error) {
	if len(raw) < chunkRecordOverhead {
		return storageID, nil, nil, fmt.Errorf("short chunk record: got %d bytes", len(raw))
	}

	copy(storageID[:], raw[0:32])
	nonce = make([]byte, packNonceSize)
	copy(nonce, raw[32:32+packNonceSize])

	cipherLen := binary.BigEndian.Uint32(raw[32+packNonceSize : chunkRecordOverhead])
	expectedLen := int(chunkRecordOverhead + int(cipherLen))
	if len(raw) != expectedLen {
		return storageID, nil, nil, fmt.Errorf("chunk record length mismatch: expected %d, got %d", expectedLen, len(raw))
	}

	ciphertext = make([]byte, cipherLen)
	copy(ciphertext, raw[chunkRecordOverhead:])
	return storageID, nonce, ciphertext, nil
}
