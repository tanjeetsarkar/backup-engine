package crypto

import (
	"bytes"
	"errors"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

func TestComputeCIDDeterministic(t *testing.T) {
	input := []byte("deterministic-cid")
	cid1 := ComputeCID(input)
	cid2 := ComputeCID(input)

	if cid1 != cid2 {
		t.Fatalf("expected deterministic CID")
	}
}

func TestDeriveStorageIDTenantIsolation(t *testing.T) {
	passphrase := []byte("correct horse battery staple")
	saltA := []byte("0123456789abcdef")
	saltB := []byte("fedcba9876543210")

	kmA, err := NewKeyManager(passphrase, saltA)
	if err != nil {
		t.Fatalf("NewKeyManager saltA: %v", err)
	}
	kmB, err := NewKeyManager(passphrase, saltB)
	if err != nil {
		t.Fatalf("NewKeyManager saltB: %v", err)
	}

	cid := ComputeCID([]byte("same-content"))
	sidA := kmA.DeriveStorageID(cid)
	sidB := kmB.DeriveStorageID(cid)

	if sidA == sidB {
		t.Fatalf("expected different StorageIDs for different tenant salts")
	}
}

func TestEncryptChunkDeterministicOutput(t *testing.T) {
	km, err := NewKeyManager([]byte("passphrase"), []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKeyManager: %v", err)
	}

	plaintext := []byte("chunk payload")
	cid := ComputeCID(plaintext)
	chunkKey := km.DeriveChunkKey(cid)
	storageID := km.DeriveStorageID(cid)

	cipher1, nonce1, err := EncryptChunk(plaintext, chunkKey, storageID)
	if err != nil {
		t.Fatalf("EncryptChunk first: %v", err)
	}
	cipher2, nonce2, err := EncryptChunk(plaintext, chunkKey, storageID)
	if err != nil {
		t.Fatalf("EncryptChunk second: %v", err)
	}

	if !bytes.Equal(nonce1, nonce2) {
		t.Fatalf("expected deterministic nonces")
	}
	if !bytes.Equal(cipher1, cipher2) {
		t.Fatalf("expected deterministic ciphertext")
	}

	decrypted, err := DecryptChunk(cipher1, nonce1, chunkKey, storageID)
	if err != nil {
		t.Fatalf("DecryptChunk: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("plaintext mismatch after decrypt")
	}
}

func TestDecryptChunkRejectsTamperedAAD(t *testing.T) {
	km, err := NewKeyManager([]byte("passphrase"), []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKeyManager: %v", err)
	}

	plaintext := []byte("authenticated payload")
	cid := ComputeCID(plaintext)
	chunkKey := km.DeriveChunkKey(cid)
	storageID := km.DeriveStorageID(cid)

	ciphertext, nonce, err := EncryptChunk(plaintext, chunkKey, storageID)
	if err != nil {
		t.Fatalf("EncryptChunk: %v", err)
	}

	tamperedStorageID := storageID
	tamperedStorageID[0] ^= 0xFF

	_, err = DecryptChunk(ciphertext, nonce, chunkKey, tamperedStorageID)
	if !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("expected ErrAuthenticationFailed, got %v", err)
	}
}

func TestEncryptChunkRejectsInvalidKeySize(t *testing.T) {
	storageID := ComputeCID([]byte("sid"))
	_, _, err := EncryptChunk([]byte("x"), []byte("short-key"), storageID)
	if !errors.Is(err, ErrInvalidKeySize) {
		t.Fatalf("expected ErrInvalidKeySize, got %v", err)
	}
}

func TestEncryptChunkNonceLength(t *testing.T) {
	km, err := NewKeyManager([]byte("passphrase"), []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKeyManager: %v", err)
	}

	plaintext := []byte("nonce length check")
	cid := ComputeCID(plaintext)
	chunkKey := km.DeriveChunkKey(cid)
	storageID := km.DeriveStorageID(cid)

	_, nonce, err := EncryptChunk(plaintext, chunkKey, storageID)
	if err != nil {
		t.Fatalf("EncryptChunk: %v", err)
	}
	if len(nonce) != chacha20poly1305.NonceSizeX {
		t.Fatalf("expected nonce length %d, got %d", chacha20poly1305.NonceSizeX, len(nonce))
	}
}

func TestDecryptMetadataWrongKeyFailsWithAuthError(t *testing.T) {
	kmA, err := NewKeyManager([]byte("passphrase-a"), []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKeyManager A: %v", err)
	}
	kmB, err := NewKeyManager([]byte("passphrase-b"), []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKeyManager B: %v", err)
	}

	ciphertext, nonce, err := kmA.EncryptMetadata([]byte("metadata"))
	if err != nil {
		t.Fatalf("EncryptMetadata: %v", err)
	}

	_, err = kmB.DecryptMetadata(ciphertext, nonce)
	if !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("expected ErrAuthenticationFailed, got %v", err)
	}
}
