package cache

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/cjstarcc/go-zero-RWCache/store/redis"
	"github.com/zeromicro/go-zero/core/stat"

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

	return cr.dotake(ctx, c.(cacheNode), val, key, query, func(v any) error {
		return cr.writeClient.(Cache).SetCtx(ctx, key, v)
	})

}

func (cr CacheRW) TakeWithExpire(val any, key string, query func(val any, expire time.Duration) error) error {

	return cr.TakeWithExpireCtx(context.Background(), val, key, query)
}

func (cr CacheRW) TakeWithExpireCtx(ctx context.Context, val any, key string, query func(val any, expire time.Duration) error) error {
	c, ok := cr.dispatcher.Get(key)
	if !ok {
		return cr.errNotFound
	}

	expire := cr.writeClient.(cacheNode).aroundDuration(cr.writeClient.(cacheNode).expiry)
	return cr.dotake(ctx, c.(cacheNode), val, key, func(v any) error {
		return query(v, expire)
	}, func(v any) error {
		return cr.writeClient.SetWithExpireCtx(ctx, key, v, expire)
	})

	// return c.(Cache).TakeWithExpireCtx(ctx, val, key, query)
}

func (cr CacheRW) dotake(ctx context.Context, c cacheNode, v any, key string,
	query func(v any) error, cacheVal func(v any) error) error {
	logger := logx.WithContext(ctx)
	val, fresh, err := c.barrier.DoEx(key, func() (any, error) {
		if err := cr.doGetCache(ctx, c, key, v); err != nil {
			if errors.Is(err, errPlaceholder) {
				return nil, cr.errNotFound
			} else if !errors.Is(err, cr.errNotFound) {
				// why we just return the error instead of query from db,
				// because we don't allow the disaster pass to the dbs.
				// fail fast, in case we bring down the dbs.
				return nil, err
			}

			if err = query(v); errors.Is(err, cr.errNotFound) {
				if err = cr.writeClient.(cacheNode).setCacheWithNotFound(ctx, key); err != nil {
					logger.Error(err)
				}

				return nil, cr.errNotFound
			} else if err != nil {
				c.stat.IncrementDbFails()
				return nil, err
			}

			if err = cacheVal(v); err != nil {
				logger.Error(err)
			}
		}

		return jsonx.Marshal(v)
	})
	if err != nil {
		return err
	}
	if fresh {
		return nil
	}

	// got the result from previous ongoing query.
	// why not call IncrementTotal at the beginning of this function?
	// because a shared error is returned, and we don't want to count.
	// for example, if the db is down, the query will be failed, we count
	// the shared errors with one db failure.
	c.stat.IncrementTotal()
	c.stat.IncrementHit()

	return jsonx.Unmarshal(val.([]byte), v)
}

func (cr CacheRW) doGetCache(ctx context.Context, c cacheNode, key string, v any) error {
	c.stat.IncrementTotal()
	data, err := c.rds.GetCtx(ctx, key)
	if err != nil {
		c.stat.IncrementMiss()
		return err
	}

	if len(data) == 0 {
		c.stat.IncrementMiss()
		return c.errNotFound
	}

	c.stat.IncrementHit()
	if data == notFoundPlaceholder {
		return errPlaceholder
	}

	return c.processCache(ctx, key, data, v)
}

func (cr CacheRW) processCache(ctx context.Context, c cacheNode, key, data string, v any) error {
	err := jsonx.Unmarshal([]byte(data), v)
	if err == nil {
		return nil
	}

	report := fmt.Sprintf("unmarshal cache, node: %s, key: %s, value: %s, error: %v",
		c.rds.Addr, key, data, err)
	logger := logx.WithContext(ctx)
	logger.Error(report)
	stat.Report(report)
	if _, e := cr.writeClient.(cacheNode).rds.DelCtx(ctx, key); e != nil {
		logger.Errorf("delete invalid cache, node: %s, key: %s, value: %s, error: %v",
			cr.writeClient.(cacheNode).rds.Addr, key, data, e)
	}

	// returns errNotFound to reload the value by the given queryFn
	return cr.errNotFound
}
