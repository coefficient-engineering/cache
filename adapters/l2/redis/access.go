package redis

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/redis/go-redis/v9"
)

// PrimaryAccess locates authoritative connections and enumerates payload
// primaries. Custom clients must preserve primary routing for ordinary EVAL
// commands too. Returned clients remain owned by the caller.
type PrimaryAccess interface {
	PrimaryForKey(context.Context, string) (*redis.Client, error)
	ForEachPrimary(context.Context, func(context.Context, *redis.Client) error) error
}

// WithPrimaryAccess supplies primary access for a wrapped payload client.
// Its routing must agree with the payload client's routing.
func WithPrimaryAccess(access PrimaryAccess) Option {
	return func(a *Adapter) { a.access = access }
}

// WithControlClient routes metadata through a coherent standalone or Cluster
// client. It is required for Ring payloads. The caller owns this client.
func WithControlClient(client redis.Cmdable) Option {
	return func(a *Adapter) { a.controlClient = client }
}

type standaloneAccess struct{ client *redis.Client }

func (p standaloneAccess) PrimaryForKey(_ context.Context, _ string) (*redis.Client, error) {
	return p.client, nil
}

func (p standaloneAccess) ForEachPrimary(ctx context.Context, fn func(context.Context, *redis.Client) error) error {
	return fn(ctx, p.client)
}

type clusterAccess struct{ client *redis.ClusterClient }

func (p clusterAccess) PrimaryForKey(ctx context.Context, key string) (*redis.Client, error) {
	return p.client.MasterForKey(ctx, key)
}

func (p clusterAccess) ForEachPrimary(ctx context.Context, fn func(context.Context, *redis.Client) error) error {
	return p.client.ForEachMaster(ctx, fn)
}

type ringAccess struct{ client *redis.Ring }

func (p ringAccess) PrimaryForKey(_ context.Context, key string) (*redis.Client, error) {
	return p.client.GetShardClientForKey(key)
}

func (p ringAccess) ForEachPrimary(ctx context.Context, fn func(context.Context, *redis.Client) error) error {
	return p.client.ForEachShard(ctx, fn)
}

func nilClient(client any) bool {
	if client == nil {
		return true
	}
	value := reflect.ValueOf(client)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

func nativeAccess(client redis.Cmdable) (PrimaryAccess, error) {
	if nilClient(client) {
		return nil, errors.New("redis l2: a non-nil Redis client is required")
	}
	switch client := client.(type) {
	case *redis.Client:
		return standaloneAccess{client}, nil
	case *redis.ClusterClient:
		return clusterAccess{client}, nil
	case *redis.Ring:
		return ringAccess{client}, nil
	default:
		return nil, fmt.Errorf("redis l2: %T requires WithPrimaryAccess for primary routing and collection", client)
	}
}

func (a *Adapter) configureAccess() error {
	if nilClient(a.client) {
		return errors.New("redis l2: a non-nil Redis client is required")
	}
	if a.access != nil && nilClient(a.access) {
		return errors.New("redis l2: PrimaryAccess must not be a typed nil")
	}
	var err error
	if a.access == nil {
		a.access, err = nativeAccess(a.client)
		if err != nil {
			return err
		}
	}
	if a.controlClient == nil {
		if _, ring := a.client.(*redis.Ring); ring {
			return errors.New("redis l2: Ring requires WithControlClient with coherent standalone or Cluster routing")
		}
		a.controlClient, a.controlAccess = a.client, a.access
		return nil
	}
	if _, ring := a.controlClient.(*redis.Ring); ring {
		return errors.New("redis l2: a Ring cannot own authoritative control metadata")
	}
	a.controlAccess, err = nativeAccess(a.controlClient)
	return err
}
