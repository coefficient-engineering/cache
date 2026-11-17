package redis

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// lostInitializationReply discards an actual server reply after a candidate
// command executes. A fresh connection must not resend the same candidate.
type lostInitializationReply struct {
	net.Conn
	pending bool
	armed   *atomic.Bool
	after   func()
}

func (c *lostInitializationReply) Write(p []byte) (int, error) {
	// The initialization command's only user argument is a 128-bit hex ID.
	if bytes.Contains(p, []byte("$32\r\n")) && bytes.Contains(p, []byte("test:fc:v2:control")) && c.armed.CompareAndSwap(true, false) {
		c.pending = true
	}
	return c.Conn.Write(p)
}

func (c *lostInitializationReply) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 && c.pending {
		c.pending = false
		c.after()
		return 0, io.EOF
	}
	return n, err
}

func TestRedisL2_AmbiguousRecoveryNeverReusesAnIncarnation(t *testing.T) {
	mr := miniredis.RunT(t)
	admin := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = admin.Close() })
	witness, err := New(admin, WithKeyPrefix("test:"))
	if err != nil {
		t.Fatal(err)
	}
	var armed atomic.Bool
	var injected atomic.Bool
	var faultErr atomic.Pointer[error]
	ctx := t.Context()
	after := func() {
		injected.Store(true)
		err := witness.Set(ctx, "witness", []byte("must-stay-invalid"), 0, []string{"tag"})
		if err == nil {
			err = witness.DeleteByTag(ctx, "tag")
		}
		if err != nil {
			faultErr.Store(&err)
		}
		mr.Del("test:fc:v2:control")
	}
	client := goredis.NewClient(&goredis.Options{
		Addr: mr.Addr(), MinRetryBackoff: time.Millisecond, MaxRetryBackoff: time.Millisecond,
		Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return &lostInitializationReply{Conn: conn, armed: &armed, after: after}, nil
		},
	})
	t.Cleanup(func() { _ = client.Close() })
	a, err := New(client, WithKeyPrefix("test:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Set(ctx, "old", []byte("old"), 0, []string{"tag"}); err != nil {
		t.Fatal(err)
	}
	oldKey := "test:fc:v2:data:" + base64.RawURLEncoding.EncodeToString([]byte("old"))
	original, err := admin.Get(ctx, oldKey).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	mr.Del("test:fc:v2:control")
	armed.Store(true)
	if err := a.Set(ctx, "fresh", []byte("fresh"), 0, []string{"tag"}); err != nil {
		t.Fatal(err)
	}
	if !injected.Load() {
		t.Fatal("lost-reply fault was not exercised")
	}
	if err := faultErr.Load(); err != nil {
		t.Fatal(*err)
	}
	for _, key := range []string{"old", "witness"} {
		if value, err := a.Get(ctx, key); err != nil || value != nil {
			t.Fatalf("%s after recovery = (%q,%v), want miss", key, value, err)
		}
	}
	if err := admin.Set(ctx, oldKey, original, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if value, err := a.Get(ctx, "old"); err != nil || value != nil {
		t.Fatalf("late original write = (%q,%v), want miss", value, err)
	}
	if value, err := a.Get(ctx, "fresh"); err != nil || string(value) != "fresh" {
		t.Fatalf("fresh record = (%q,%v)", value, err)
	}
}
