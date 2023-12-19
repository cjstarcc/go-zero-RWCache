package cache

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/cjstarcc/go-zero-RWCache/store/redis"

	"github.com/zeromicro/go-zero/core/hash"
	"github.com/zeromicro/go-zero/core/jsonx"
	"github.com/zeromicro/go-zero/core/logx"
)

type CacheRW struct {
	writeClient Cache // 写库 client
	dispatcher  *hash.ConsistentHash
	errNotFound error
}

func (c CacheRW) Del(keys ...string) error {
	return c.writeClient.DelCtx(context.Background(), keys...)
}

func (c CacheRW) DelCtx(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}

	logger := logx.WithContext(ctx)
	if len(keys) > 1 && c.writeClient.(cacheNode).rds.Type == redis.ClusterType {
		for _, key := range keys {
			if _, err := c.writeClient.(cacheNode).rds.DelCtx(ctx, key); err != nil {
				logger.Errorf("failed to clear cache with key: %q, error: %v", key, err)
				c.writeClient.(cacheNode).asyncRetryDelCache(key)
			}
		}
	} else if _, err := c.writeClient.(cacheNode).rds.DelCtx(ctx, keys...); err != nil {
		logger.Errorf("failed to clear cache with keys: %q, error: %v", formatKeys(keys), err)
		c.writeClient.(cacheNode).asyncRetryDelCache(keys...)
	}

	return nil

}

func (c CacheRW) Get(key string, val any) error {
	return c.GetCtx(context.Background(), key, val)
}

func (cr CacheRW) GetCtx(ctx context.Context, key string, val any) error {
	c, ok := cr.dispatcher.Get(key)
	if !ok {
		return cr.errNotFound
	}

	return c.(Cache).GetCtx(ctx, key, val)
}

func (c CacheRW) IsNotFound(err error) bool {
	return errors.Is(err, c.writeClient.(cacheNode).errNotFound)
}

func (c CacheRW) Set(key string, val any) error {
	return c.SetCtx(context.Background(), key, val)
}

func (c CacheRW) SetCtx(ctx context.Context, key string, val any) error {
	return c.writeClient.SetWithExpireCtx(ctx, key, val, c.writeClient.(cacheNode).aroundDuration(c.writeClient.(cacheNode).expiry))
}

func (c CacheRW) SetWithExpire(key string, val any, expire time.Duration) error {
	return c.SetWithExpireCtx(context.Background(), key, val, expire)
}

func (c CacheRW) SetWithExpireCtx(ctx context.Context, key string, val any, expire time.Duration) error {
	data, err := jsonx.Marshal(val)
	if err != nil {
		return err
	}

	return c.writeClient.(cacheNode).rds.SetexCtx(ctx, key, string(data), int(math.Ceil(expire.Seconds())))
}

func (cr CacheRW) Take(val any, key string, query func(val any) error) error {
	return cr.TakeCtx(context.Background(), val, key, query)
}

func (cr CacheRW) TakeCtx(ctx context.Context, val any, key string, query func(val any) error) error {
	c, ok := cr.dispatcher.Get(key)
	if !ok {
		return cr.errNotFound
	}

	return c.(Cache).TakeCtx(ctx, val, key, query)
}

func (cr CacheRW) TakeWithExpire(val any, key string, query func(val any, expire time.Duration) error) error {
	return cr.TakeWithExpireCtx(context.Background(), val, key, query)
}

func (cr CacheRW) TakeWithExpireCtx(ctx context.Context, val any, key string, query func(val any, expire time.Duration) error) error {
	c, ok := cr.dispatcher.Get(key)
	if !ok {
		return cr.errNotFound
	}

	return c.(Cache).TakeWithExpireCtx(ctx, val, key, query)
}
