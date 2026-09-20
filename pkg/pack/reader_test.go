package pack

import (
	"bytes"
	"testing"
)

func TestDecodeChunkRecordRoundTrip(t *testing.T) {
	builder := NewPackfileBuilder(1024)
	var sid [32]byte
	for i := 0; i < 32; i++ {
		sid[i] = byte(i + 1)
	}
	nonce := bytes.Repeat([]byte{0xAB}, packNonceSize)
	cipher := []byte{1, 2, 3, 4, 5}

	if err := builder.Append(sid, nonce, cipher); err != nil {
		t.Fatalf("Append: %v", err)
	}

	packBytes, _, entries, err := builder.Finalize()
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	recordOffset := int(entries[0].Offset)
	recordSize := int(RecordSizeFromCipherLen(entries[0].Length))
	raw := packBytes[recordOffset : recordOffset+recordSize]

	gotSID, gotNonce, gotCipher, err := DecodeChunkRecord(raw)
	if err != nil {
		t.Fatalf("DecodeChunkRecord: %v", err)
	}

	if gotSID != sid {
		t.Fatalf("storage id mismatch")
	}
	if !bytes.Equal(gotNonce, nonce) {
		t.Fatalf("nonce mismatch")
	}
	if !bytes.Equal(gotCipher, cipher) {
		t.Fatalf("ciphertext mismatch")
	}
}

func TestDecodeChunkRecordRejectsShortInput(t *testing.T) {
	_, _, _, err := DecodeChunkRecord([]byte{1, 2, 3})
	if err == nil {
		t.Fatalf("expected error for short record")
	}
}
