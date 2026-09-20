package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"github.com/zeebo/blake3"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

var (
	ErrAuthenticationFailed = errors.New("cryptographic authentication failed: corrupt or tampered data")
	ErrInvalidKeySize       = errors.New("invalid key size: must be 32 bytes")
)

// KeyManager coordinates zero-knowledge key derivation and tenant isolation.
type KeyManager struct {
	masterKey   []byte
	chunkSecret []byte
	metaSecret  []byte
}

// NewKeyManager derives the primary key hierarchy from a user passphrase and salt.
// Argon2id parameters follow RFC 9106: T=3, M=64MB, P=4.
func NewKeyManager(passphrase, salt []byte) (*KeyManager, error) {
	if len(salt) < 16 {
		return nil, errors.New("salt must be at least 16 bytes")
	}

	masterKey := argon2.IDKey(passphrase, salt, 3, 64*1024, 4, 32)

	km := &KeyManager{
		masterKey: masterKey,
	}

	if err := km.deriveSubkeys(); err != nil {
		return nil, err
	}

	return km, nil
}

func (km *KeyManager) deriveSubkeys() error {
	km.chunkSecret = make([]byte, 32)
	hkdfChunk := hkdf.Expand(sha256.New, km.masterKey, []byte("backup-chunk-hmac-v1"))
	if _, err := io.ReadFull(hkdfChunk, km.chunkSecret); err != nil {
		return fmt.Errorf("failed to derive chunk subkey: %w", err)
	}

	km.metaSecret = make([]byte, 32)
	hkdfMeta := hkdf.Expand(sha256.New, km.masterKey, []byte("backup-metadata-v1"))
	if _, err := io.ReadFull(hkdfMeta, km.metaSecret); err != nil {
		return fmt.Errorf("failed to derive metadata subkey: %w", err)
	}

	return nil
}

// ComputeCID calculates the collision-resistant Content Identifier using BLAKE3.
func ComputeCID(data []byte) [32]byte {
	return blake3.Sum256(data)
}

// DeriveChunkKey derives a deterministic chunk encryption key from the CID.
func (km *KeyManager) DeriveChunkKey(cid [32]byte) []byte {
	mac := hmac.New(sha256.New, km.chunkSecret)
	mac.Write(cid[:])
	return mac.Sum(nil)
}

// DeriveStorageID derives the blinded storage address to prevent cross-tenant tracking.
func (km *KeyManager) DeriveStorageID(cid [32]byte) [32]byte {
	mac := hmac.New(sha256.New, km.metaSecret)
	mac.Write(cid[:])
	blinded := mac.Sum(nil)
	return blake3.Sum256(blinded)
}

// EncryptChunk encrypts chunk data using XChaCha20-Poly1305 with an authenticated StorageID.
// Nonce is deterministically generated to guarantee convergent ciphertext per tenant.
func EncryptChunk(plaintext []byte, chunkKey []byte, storageID [32]byte) (ciphertext []byte, nonce []byte, err error) {
	if len(chunkKey) != chacha20poly1305.KeySize {
		return nil, nil, ErrInvalidKeySize
	}

	aead, err := chacha20poly1305.NewX(chunkKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize cipher: %w", err)
	}

	// Derive deterministic 24-byte synthetic nonce: BLAKE3(ChunkKey || Plaintext)[0:24]
	nonceInput := make([]byte, 0, len(chunkKey)+len(plaintext))
	nonceInput = append(nonceInput, chunkKey...)
	nonceInput = append(nonceInput, plaintext...)
	fullNonce := blake3.Sum256(nonceInput)
	nonce = fullNonce[:chacha20poly1305.NonceSizeX]

	// Encrypt and authenticate with storageID as Additional Authenticated Data (AAD)
	ciphertext = aead.Seal(nil, nonce, plaintext, storageID[:])
	return ciphertext, nonce, nil
}

// DecryptChunk authenticates and decrypts an encrypted chunk.
func DecryptChunk(ciphertext []byte, nonce []byte, chunkKey []byte, storageID [32]byte) ([]byte, error) {
	if len(chunkKey) != chacha20poly1305.KeySize {
		return nil, ErrInvalidKeySize
	}

	aead, err := chacha20poly1305.NewX(chunkKey)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize cipher: %w", err)
	}

	plaintext, err := aead.Open(nil, nonce, ciphertext, storageID[:])
	if err != nil {
		return nil, ErrAuthenticationFailed
	}

	return plaintext, nil
}

// EncryptMetadata encrypts arbitrary metadata using the system metadata key.
func (km *KeyManager) EncryptMetadata(data []byte) (ciphertext, nonce []byte, err error) {
	aead, err := chacha20poly1305.NewX(km.metaSecret)
	if err != nil {
		return nil, nil, err
	}

	nonce = make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}

	ciphertext = aead.Seal(nil, nonce, data, nil)
	return ciphertext, nonce, nil
}

// DecryptMetadata decrypts arbitrary metadata using the system metadata key.
func (km *KeyManager) DecryptMetadata(ciphertext, nonce []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(km.metaSecret)
	if err != nil {
		return nil, err
	}

	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrAuthenticationFailed
	}

	return plaintext, nil
}
