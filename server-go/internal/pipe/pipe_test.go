package pipe

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Perebevaska/vpm/server-go/internal/crypto"
	"github.com/Perebevaska/vpm/server-go/internal/dav"
)

func readN(p *Pipe, n int) []byte {
	out := make([]byte, 0, n)
	buf := make([]byte, 65536)
	for len(out) < n {
		m, err := p.Read(buf)
		if m > 0 {
			out = append(out, buf[:m]...)
		}
		if err != nil {
			break
		}
	}
	return out
}

// Двусторонний loopback через живой Яндекс.Диск (два зеркальных пайпа), с enc.
func TestLiveLoopback(t *testing.T) {
	url := os.Getenv("WEBDAV_URL")
	login := os.Getenv("WEBDAV_LOGIN")
	pw := os.Getenv("WEBDAV_PASSWORD")
	if url == "" || login == "" || pw == "" {
		t.Skip("WEBDAV_* unset")
	}
	ctx := context.Background()
	c := dav.NewClient(url, login, pw, 60*time.Second)
	key := crypto.DeriveKey(pw)
	sidb := make([]byte, 8)
	rand.Read(sidb)
	sid := hex.EncodeToString(sidb)
	for _, d := range []string{"tunnel", "tunnel/" + sid, "tunnel/" + sid + "/c2s", "tunnel/" + sid + "/s2c"} {
		c.Mkcol(ctx, d)
	}
	defer c.Delete(ctx, "tunnel/"+sid)

	A := New(c, c, sid, "c2s", "s2c", key) // клиент: пишет c2s, читает s2c
	B := New(c, c, sid, "s2c", "c2s", key) // сервер: пишет s2c, читает c2s
	A.Start()
	B.Start()

	pA := make([]byte, 300000)
	pB := make([]byte, 180000)
	rand.Read(pA)
	rand.Read(pB)

	var gotB, gotA []byte
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); gotB = readN(B, len(pA)) }() // B читает c2s
	go func() { defer wg.Done(); gotA = readN(A, len(pB)) }() // A читает s2c
	A.Write(pA)
	B.Write(pB)
	wg.Wait()

	if !bytes.Equal(gotB, pA) {
		t.Fatalf("c2s A->B mismatch: got %d want %d", len(gotB), len(pA))
	}
	if !bytes.Equal(gotA, pB) {
		t.Fatalf("s2c B->A mismatch: got %d want %d", len(gotA), len(pB))
	}
	A.Close()
	B.Close()
}
