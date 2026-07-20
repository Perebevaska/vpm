package crypto

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

// TestCrossLangDecrypt: расшифровать в Go то, что зашифровал Python.
// Скип, если env не задан. Запуск:
//
//	WT_XLANG_PW=... WT_XLANG_BLOB=<hex> WT_XLANG_WANT=... go test ./internal/crypto/
func TestCrossLangDecrypt(t *testing.T) {
	blobHex := os.Getenv("WT_XLANG_BLOB")
	if blobHex == "" {
		t.Skip("WT_XLANG_BLOB unset")
	}
	blob, err := hex.DecodeString(blobHex)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := DecryptChunk(DeriveKey(os.Getenv("WT_XLANG_PW")), blob)
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != os.Getenv("WT_XLANG_WANT") {
		t.Fatalf("got %q want %q", pt, os.Getenv("WT_XLANG_WANT"))
	}
}

func TestDeriveKeyVector(t *testing.T) {
	got := DeriveKey("hunter2")
	want := sha256.Sum256([]byte("webdav-tunnel-v1:hunter2"))
	if !bytes.Equal(got, want[:]) {
		t.Fatalf("derive_key mismatch")
	}
	if len(got) != 32 {
		t.Fatalf("key len = %d, want 32", len(got))
	}
}

func TestRoundTrip(t *testing.T) {
	key := DeriveKey("s3cret")
	for _, pt := range [][]byte{nil, []byte("x"), bytes.Repeat([]byte("A"), 131071)} {
		blob, err := EncryptChunk(key, pt)
		if err != nil {
			t.Fatal(err)
		}
		if len(blob) < 12+16 {
			t.Fatalf("blob too short: %d", len(blob))
		}
		out, err := DecryptChunk(key, blob)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out, pt) {
			t.Fatalf("round-trip mismatch (len %d)", len(pt))
		}
	}
}

func TestDecryptRejectsShort(t *testing.T) {
	if _, err := DecryptChunk(DeriveKey("k"), []byte("short")); err == nil {
		t.Fatal("expected error on short ciphertext")
	}
}
