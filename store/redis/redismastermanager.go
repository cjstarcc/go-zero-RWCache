package redis

import (
	"crypto/tls"
	"io"
	"runtime"

	red "github.com/go-redis/redis/v8"
	"github.com/zeromicro/go-zero/core/syncx"
)

var (
	masterManager = syncx.NewResourceManager()
	// nodePoolSize is default pool size for node type of redis.
	masterPoolSize = 10 * runtime.GOMAXPROCS(0)
)

func GetRWRedisClient(r *Redis) (RedisNode, error) {
	val, err := masterManager.GetResource(r.Addr, func() (io.Closer, error) {
		var tlsConfig *tls.Config
		if r.tls {
			tlsConfig = &tls.Config{
				InsecureSkipVerify: true,
			}
		}
		store := red.NewClient(&red.Options{
			Addr:         r.Addr,
			Password:     r.Pass,
			DB:           defaultDatabase,
			MaxRetries:   maxRetries,
			MinIdleConns: idleConns,
			TLSConfig:    tlsConfig,
		})
		store.AddHook(durationHook)
		for _, hook := range r.hooks {
			store.AddHook(hook)
		}

		connCollector.registerClient(&statGetter{
			clientType: MasterType,
			key:        r.Addr,
			poolSize:   masterPoolSize,
			poolStats: func() *red.PoolStats {
				return store.PoolStats()
			},
		})

		return store, nil
	})
	if err != nil {
		return nil, err
	}

	return val.(*red.Client), nil
}
