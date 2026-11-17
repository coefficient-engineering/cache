package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/coefficient-engineering/cache"
	redisbp "github.com/coefficient-engineering/cache/adapters/backplane/redis"
	redisl2 "github.com/coefficient-engineering/cache/adapters/l2/redis"
	"github.com/coefficient-engineering/cache/adapters/serializer/json"
	"github.com/coefficient-engineering/cache/backplane"
)

const (
	remoteTagRunTimeout  = 30 * time.Second
	remoteTagWaitTimeout = 5 * time.Second
	remoteTagCacheName   = "verify-remote-tags"
	remoteTagBarrier     = "receipt-barrier"
)

type remoteTagNode struct {
	cache    cache.Cache
	receipts chan cache.EventBackplaneReceived
}

type remoteTagConfig struct {
	id, name, prefix string
	adapter          *redisl2.Adapter
}

type remoteTagMode struct {
	label, prefix, channel                               string
	client                                               *redis.Client
	adapter                                              *redisl2.Adapter
	report                                               *report
	a, b, otherName, otherPrefix, nameProbe, prefixProbe remoteTagNode
}

type remoteTagEntry struct {
	key     string
	tags    []string
	removed bool
}

// runRemoteTags proves remote-only tag eviction over real Redis pub/sub.
func runRemoteTags(ctx context.Context, r *report) (result error) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		return errors.New("REDIS_ADDR is not set; see docs/agents/verification/redis-multinode/README.md")
	}
	ctx, cancel := context.WithTimeout(ctx, remoteTagRunTimeout)
	defer cancel()
	client := redis.NewClient(&redis.Options{Addr: addr})
	runID := fmt.Sprintf("verify:remote-tags:%d:", time.Now().UnixNano())
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), remoteTagWaitTimeout)
		defer cleanupCancel()
		cleanupErr := cleanupRun(cleanupCtx, client, runID)
		keys, keysErr := client.Keys(cleanupCtx, runID+"*").Result()
		r.record(check{
			Name:     "run-prefixed-redis-data-and-control-keys-cleaned",
			OK:       cleanupErr == nil && keysErr == nil && len(keys) == 0,
			Observed: map[string]any{"remaining_keys": len(keys), errorKey: errString(errors.Join(cleanupErr, keysErr))},
		})
		result = errors.Join(result, cleanupErr, keysErr, client.Close())
	}()
	if err := client.Ping(ctx).Err(); err != nil {
		return err
	}
	for _, label := range []string{"l1-only", "redis-l2"} {
		mode := remoteTagMode{
			label: label, prefix: runID + label + ":", channel: runID + "backplane", client: client, report: r,
		}
		if label == "redis-l2" {
			adapter, err := redisl2.New(client, redisl2.WithKeyPrefix(mode.prefix+"l2:"))
			if err != nil {
				return err
			}
			mode.adapter = adapter
		}
		if err := mode.run(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

func newRemoteTagNode(client *redis.Client, channel string, cfg remoteTagConfig) (remoteTagNode, error) {
	bp := redisbp.New(client, redisbp.WithChannel(channel), redisbp.WithNodeID(cfg.id))
	opts := []cache.Option{
		cache.WithCacheName(cfg.name), cache.WithKeyPrefix(cfg.prefix), cache.WithNodeID(cfg.id),
		cache.WithBackplane(bp),
	}
	if cfg.adapter != nil {
		opts = append(opts, cache.WithL2(cfg.adapter), cache.WithSerializer(&json.Serializer{}))
	}
	c, err := cache.New(opts...)
	if err != nil {
		return remoteTagNode{}, errors.Join(err, bp.Close())
	}
	n := remoteTagNode{cache: c, receipts: make(chan cache.EventBackplaneReceived, 2)}
	c.Events().On(func(e cache.Event) {
		if received, ok := e.(cache.EventBackplaneReceived); ok && received.Type == backplane.MessageTypeDeleteByTags {
			n.receipts <- received
		}
	})
	return n, nil
}

func (m *remoteTagMode) run(ctx context.Context, r *report) (result error) {
	defer func() {
		for _, n := range []*remoteTagNode{&m.a, &m.b, &m.otherName, &m.otherPrefix, &m.nameProbe, &m.prefixProbe} {
			if n.cache != nil {
				result = errors.Join(result, n.cache.Close())
			}
		}
	}()
	if err := m.open(); err != nil {
		return err
	}
	for _, operation := range []string{"single", "batch"} {
		if err := m.invalidate(ctx, r, operation); err != nil {
			return err
		}
	}
	return nil
}

func (m *remoteTagMode) open() error {
	specs := []struct {
		target *remoteTagNode
		cfg    remoteTagConfig
	}{
		{&m.a, remoteTagConfig{id: "a", name: remoteTagCacheName, prefix: m.prefix, adapter: m.adapter}},
		{&m.b, remoteTagConfig{id: "b", name: remoteTagCacheName, prefix: m.prefix, adapter: m.adapter}},
		{&m.otherName, remoteTagConfig{id: "other-name", name: "isolated-name", prefix: m.prefix}},
		{&m.otherPrefix, remoteTagConfig{id: "other-prefix", name: remoteTagCacheName, prefix: m.prefix + "isolated:"}},
		{&m.nameProbe, remoteTagConfig{id: "name-probe", name: "isolated-name", prefix: m.prefix}},
		{&m.prefixProbe, remoteTagConfig{id: "prefix-probe", name: remoteTagCacheName, prefix: m.prefix + "isolated:"}},
	}
	for _, spec := range specs {
		n, err := newRemoteTagNode(m.client, m.channel, spec.cfg)
		if err != nil {
			return err
		}
		*spec.target = n
	}
	return nil
}

func (m *remoteTagMode) invalidate(ctx context.Context, r *report, operation string) error {
	entries := []remoteTagEntry{
		{key: operation + "/red", tags: []string{tagRed, tagRed}, removed: true},
		{key: operation + "/blue", tags: []string{tagBlue}, removed: operation == "batch"},
		{key: operation + "/overlap", tags: []string{tagRed, tagBlue}, removed: true},
		{key: operation + "/unrelated", tags: []string{tagGreen}},
		{key: operation + "/untagged"},
	}
	if err := m.seed(ctx, r, entries); err != nil {
		return err
	}
	var err error
	if operation == "single" {
		err = m.a.cache.DeleteByTag(ctx, tagRed)
	} else {
		err = m.a.cache.DeleteByTags(ctx, []string{tagRed, tagBlue, tagRed})
	}
	if err != nil {
		return err
	}
	if err := remoteTagReceipt(ctx, r, m.label+"-"+operation+"-node-b-type5-received", m.b); err != nil {
		return err
	}
	if err := m.isolationBarriers(ctx, r, operation); err != nil {
		return err
	}
	for _, entry := range entries {
		m.read(ctx, m.b, entry.key+"-after", entry.key, !entry.removed)
		m.read(ctx, m.otherName, entry.key+"-cache-name-isolated", entry.key, true)
		m.read(ctx, m.otherPrefix, entry.key+"-key-prefix-isolated", entry.key, true)
		if m.adapter != nil {
			m.readL2(ctx, r, entry.key+"-l2-after", entry.key, !entry.removed)
		}
	}
	// Exercise an ordinary consumer Get too: L1 eviction must not be
	// hidden by refilling the invalidated value from L2.
	value, found, getErr := cache.Get[string](ctx, m.b.cache, operation+"/red")
	r.record(check{
		Name:     m.label + "-" + operation + "-ordinary-get-cannot-return-invalidated-value",
		OK:       getErr == nil && !found,
		Observed: map[string]any{valueKey: value, foundKey: found, errorKey: errString(getErr)},
	})
	freshKey := operation + "/red"
	if err := m.b.cache.Set(ctx, freshKey, "seed:"+freshKey, remoteTagSilent, cache.WithTags(tagRed)); err != nil {
		return err
	}
	m.read(ctx, m.b, operation+"-fresh-write-after-delivery-readable", freshKey, true)
	return nil
}

func (m *remoteTagMode) seed(ctx context.Context, r *report, entries []remoteTagEntry) error {
	for _, entry := range entries {
		for _, n := range []remoteTagNode{m.b, m.otherName, m.otherPrefix} {
			if err := n.cache.Set(ctx, entry.key, "seed:"+entry.key, remoteTagSilent, cache.WithTags(entry.tags...)); err != nil {
				return err
			}
		}
		// A has never seeded a key. These reads cannot populate A from L2.
		m.read(ctx, m.a, entry.key+"-sender-has-no-l1-seed", entry.key, false)
		// A successful read with L2 bypassed is a fresh B L1 hit.
		m.read(ctx, m.b, entry.key+"-fresh-node-b-l1-hit-before", entry.key, true)
		if m.adapter != nil {
			m.readL2(ctx, r, entry.key+"-l2-seeded", entry.key, true)
		}
	}
	return nil
}

// Isolation fences use matched public cache publishers after B's receipt.
// Redis delivers each subscription's messages in order, so the fence receipt
// proves the isolated receiver has also processed the earlier foreign message.
func (m *remoteTagMode) isolationBarriers(ctx context.Context, r *report, operation string) error {
	for _, pair := range []struct {
		name             string
		sender, receiver remoteTagNode
	}{
		{name: "cache-name", sender: m.nameProbe, receiver: m.otherName},
		{name: "key-prefix", sender: m.prefixProbe, receiver: m.otherPrefix},
	} {
		if err := pair.sender.cache.DeleteByTag(ctx, remoteTagBarrier); err != nil {
			return err
		}
		if err := remoteTagReceipt(ctx, r, m.label+"-"+operation+"-"+pair.name+"-receipt-barrier", pair.receiver); err != nil {
			return err
		}
	}
	return nil
}

func remoteTagReceipt(ctx context.Context, r *report, name string, n remoteTagNode) error {
	waitCtx, cancel := context.WithTimeout(ctx, remoteTagWaitTimeout)
	defer cancel()
	select {
	case received := <-n.receipts:
		r.record(check{
			Name: name, OK: received.Key == "" && received.Type == backplane.MessageTypeDeleteByTags,
			Observed: map[string]any{"message_type": received.Type, entryKey: received.Key},
		})
		return nil
	case <-waitCtx.Done():
		r.record(check{Name: name, Observed: map[string]any{errorKey: waitCtx.Err().Error()}})
		return fmt.Errorf("%s: %w", name, waitCtx.Err())
	}
}

func (m *remoteTagMode) read(ctx context.Context, n remoteTagNode, name, key string, found bool) {
	value, ok, err := cache.Get[string](ctx, n.cache, key, remoteTagL1Read)
	m.report.record(check{
		Name: m.label + "-" + name, OK: err == nil && ok == found && (!found || value == "seed:"+key),
		Observed: map[string]any{valueKey: value, foundKey: ok, errorKey: errString(err)},
	})
}

func (m *remoteTagMode) readL2(ctx context.Context, r *report, name, key string, found bool) {
	payload, err := m.adapter.Get(ctx, m.prefix+key)
	r.record(check{
		Name: m.label + "-" + name, OK: err == nil && (payload != nil) == found,
		Observed: map[string]any{"payload_bytes": len(payload), foundKey: payload != nil, errorKey: errString(err)},
	})
}

func remoteTagSilent(o *cache.EntryOptions) { o.SkipBackplaneNotifications = true }

func remoteTagL1Read(o *cache.EntryOptions) { o.SkipL2Read = true }
