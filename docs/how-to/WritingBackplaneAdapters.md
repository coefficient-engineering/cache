# Writing Backplane Adapters

The `backplane.Backplane` interface defines the contract for inter-node cache invalidation. The backplane transports messages between cache nodes so that mutations on one node cause L1 evictions on others.

## The interface

```go
package backplane

type Backplane interface {
    Publish(ctx context.Context, msg Message) error
    Subscribe(handler Handler) (cancel func(), err error)
    Close() error
}

type Handler func(msg Message)
```

## Method contracts

### `Publish(ctx context.Context, msg Message) error`

Send `msg` to all nodes subscribed to this backplane. If `msg.SourceID` is empty, set it to this node's identifier before publishing.

### `Subscribe(handler Handler) (cancel func(), err error)`

Register `handler` to receive inbound messages. The implementation must call `handler` from a goroutine it manages. Never call `handler` on the caller's goroutine.

The returned `cancel` function stops the subscription and frees resources. After `cancel` is called, the handler must not be invoked again.

### `Close() error`

Shut down the transport connection. After `Close`, no more messages should be published or received.

## The Message type

```go
type MessageType uint8

const (
    MessageTypeSet    MessageType = 1
    MessageTypeDelete MessageType = 2
    MessageTypeExpire MessageType = 3
    MessageTypeClear  MessageType = 4
)

type Message struct {
    Type      MessageType
    CacheName string
    Key       string
    SourceID  string
}
```

- `CacheName` allows multiple named cache instances to share one backplane channel. Receivers filter by cache name.
- `Key` is empty for `MessageTypeClear`.
- `SourceID` identifies the sender. Receivers use this to skip self-messages.

## Self-message filtering

The cache handles self-message filtering internally by comparing `msg.SourceID` with its own `NodeID`. However, if your transport naturally delivers messages back to the sender (e.g., Redis pub/sub), the built-in memory backplane adapter also filters self-messages in the subscribe goroutine as an optimization. Either approach works.

## Three rules

1. **Set `SourceID` in `Publish`** if it has not been set. The cache typically sets it, but your adapter should handle the case where it is empty.
2. **Call `handler` from a managed goroutine** in `Subscribe`. The handler must not block the caller of `Subscribe`.
3. **Use a buffered channel or drop policy** to prevent a slow handler from blocking message delivery. The built-in memory adapter uses a buffered channel of 64.

## Example: wrapping a pub/sub client

```go
package mypubsub

import (
    "context"
    "encoding/json"
    "sync"

    "github.com/coefficient-engineering/cache/backplane"
    "github.com/example/somepubsub"
)

type Backplane struct {
    client  *somepubsub.Client
    channel string
    nodeID  string
}

func New(client *somepubsub.Client, channel, nodeID string) *Backplane {
    return &Backplane{client: client, channel: channel, nodeID: nodeID}
}

func (b *Backplane) Publish(ctx context.Context, msg backplane.Message) error {
    if msg.SourceID == "" {
        msg.SourceID = b.nodeID
    }
    data, err := json.Marshal(msg)
    if err != nil {
        return err
    }
    return b.client.Publish(ctx, b.channel, data)
}

func (b *Backplane) Subscribe(handler backplane.Handler) (cancel func(), err error) {
    ctx, cancel := context.WithCancel(context.Background())

    go func() {
        msgs := b.client.Subscribe(ctx, b.channel)
        for {
            select {
            case raw, ok := <-msgs:
                if !ok {
                    return
                }
                var msg backplane.Message
                if err := json.Unmarshal(raw, &msg); err != nil {
                    continue // skip malformed messages
                }
                handler(msg)
            case <-ctx.Done():
                return
            }
        }
    }()

    return cancel, nil
}

func (b *Backplane) Close() error {
    return b.client.Close()
}

// Compile-time check.
var _ backplane.Backplane = (*Backplane)(nil)
```

## Concurrency

All methods must be safe for concurrent use. `Publish` may be called from multiple goroutines concurrently.

## Reference implementations

- `adapters/backplane/memory/memory.go` for an in-process implementation using Go channels and a `Hub` for multi-node testing
- `adapters/backplane/noop/noop.go` for a no-op implementation
- `adapters/backplane/redis/redis.go` for a production Redis pub/sub implementation

## See also

- [Adding a Backplane](AddingBackplane.md) for wiring a backplane adapter
- [Backplane and Multi-Node Caching](../explanation/BackplaneAndMultiNode.md) for the design rationale
