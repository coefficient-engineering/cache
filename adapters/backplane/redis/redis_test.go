package redis

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/coefficient-engineering/cache/backplane"
	goredis "github.com/redis/go-redis/v9"
)

func setupBP(t *testing.T, nodeID string) (*Backplane, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	bp := New(client, WithNodeID(nodeID), WithChannel("test:backplane"))
	return bp, mr
}

func setupBPWithAddr(t *testing.T, nodeID, addr string) *Backplane {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() { client.Close() })
	return New(client, WithNodeID(nodeID), WithChannel("test:backplane"))
}

func TestRedisBackplane_PublishSubscribe(t *testing.T) {
	bp, mr := setupBP(t, "node-1")
	defer bp.Close()

	bp2 := setupBPWithAddr(t, "node-2", mr.Addr())
	defer bp2.Close()

	var received backplane.Message
	var wg sync.WaitGroup
	wg.Add(1)

	cancel, err := bp2.Subscribe(func(msg backplane.Message) {
		received = msg
		wg.Done()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	time.Sleep(50 * time.Millisecond)

	msg := backplane.Message{
		Type:      backplane.MessageTypeSet,
		CacheName: "products",
		Key:       "product:42",
		SourceID:  "node-1",
	}
	if err := bp.Publish(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for message")
	}

	if received.Type != backplane.MessageTypeSet {
		t.Fatalf("type: got %v, want %v", received.Type, backplane.MessageTypeSet)
	}
	if received.Key != "product:42" {
		t.Fatalf("key: got %q, want %q", received.Key, "product:42")
	}
	if received.CacheName != "products" {
		t.Fatalf("cacheName: got %q, want %q", received.CacheName, "products")
	}
}

func TestRedisBackplane_SkipSelfMessages(t *testing.T) {
	bp, _ := setupBP(t, "node-1")
	defer bp.Close()

	received := make(chan backplane.Message, 10)

	cancel, err := bp.Subscribe(func(msg backplane.Message) {
		received <- msg
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	time.Sleep(50 * time.Millisecond)

	msg := backplane.Message{
		Type:      backplane.MessageTypeDelete,
		CacheName: "test",
		Key:       "k1",
	}
	if err := bp.Publish(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	select {
	case m := <-received:
		t.Fatalf("expected self-message to be filtered, got %+v", m)
	case <-time.After(200 * time.Millisecond):
		// Good — no message received.
	}
}

func TestRedisBackplane_MultipleSubscribers(t *testing.T) {
	bp1, mr := setupBP(t, "node-1")
	defer bp1.Close()

	bp2 := setupBPWithAddr(t, "node-2", mr.Addr())
	defer bp2.Close()

	var mu sync.Mutex
	var messagesOnBP1 []backplane.Message
	var messagesOnBP2 []backplane.Message

	cancel1, err := bp1.Subscribe(func(msg backplane.Message) {
		mu.Lock()
		messagesOnBP1 = append(messagesOnBP1, msg)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel1()

	cancel2, err := bp2.Subscribe(func(msg backplane.Message) {
		mu.Lock()
		messagesOnBP2 = append(messagesOnBP2, msg)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel2()

	time.Sleep(50 * time.Millisecond)

	if err := bp1.Publish(context.Background(), backplane.Message{
		Type:      backplane.MessageTypeSet,
		CacheName: "test",
		Key:       "from-1",
	}); err != nil {
		t.Fatal(err)
	}

	if err := bp2.Publish(context.Background(), backplane.Message{
		Type:      backplane.MessageTypeDelete,
		CacheName: "test",
		Key:       "from-2",
	}); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if len(messagesOnBP1) != 1 {
		t.Fatalf("bp1: expected 1 message, got %d: %+v", len(messagesOnBP1), messagesOnBP1)
	}
	if messagesOnBP1[0].Key != "from-2" {
		t.Fatalf("bp1: expected key 'from-2', got %q", messagesOnBP1[0].Key)
	}

	if len(messagesOnBP2) != 1 {
		t.Fatalf("bp2: expected 1 message, got %d: %+v", len(messagesOnBP2), messagesOnBP2)
	}
	if messagesOnBP2[0].Key != "from-1" {
		t.Fatalf("bp2: expected key 'from-1', got %q", messagesOnBP2[0].Key)
	}
}

func TestRedisBackplane_Cancel(t *testing.T) {
	bp, _ := setupBP(t, "node-1")
	defer bp.Close()

	received := make(chan backplane.Message, 10)

	cancel, err := bp.Subscribe(func(msg backplane.Message) {
		received <- msg
	})
	if err != nil {
		t.Fatal(err)
	}

	cancel()
	time.Sleep(50 * time.Millisecond)

	// Publish after cancel — handler should not receive anything.
	if err := bp.Publish(context.Background(), backplane.Message{
		Type:      backplane.MessageTypeSet,
		CacheName: "test",
		Key:       "k1",
		SourceID:  "other-node",
	}); err != nil {
		// Publish may fail because pubsub is closed — that's OK.
		return
	}

	select {
	case m := <-received:
		t.Fatalf("expected no message after cancel, got %+v", m)
	case <-time.After(200 * time.Millisecond):
		// Good — no message received.
	}
}

func TestRedisBackplane_Close(t *testing.T) {
	bp, _ := setupBP(t, "node-1")

	if err := bp.Close(); err != nil {
		t.Fatal(err)
	}

	if err := bp.Close(); err != nil {
		t.Fatal(err)
	}
}
