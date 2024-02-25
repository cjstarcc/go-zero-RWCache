# feature
添加 `go-zero` 读写分离模式
# 使用
首先现在分离库
```bash
go get https://github.com/cjstarcc/go-zero-RWCache.git
go get https://github.com/cjstarcc/gorm-zero.git
```

1. 随后在业务的配置文件中添加，相关配置
``` yaml
BizRedis:
  - Host: $YOUR_MASTER_IP # 你的master ip
    Pass: $YOUR_PASSWORD
    Type: master   #模式 master读写分离，cluster 集群模式，node 单节点模式
    RWMode: master # 如果是读写分离模式，那么这个用于标记master节点和slave节点;
  - Host: $YOUR_SLAVE_IP # 你的slave ip
    Pass: $YOUR_PASSWORD
    Type: slave   #读写分离模式
    RWMode: master # 如果是读写分离模式，那么这个用于标记master节点和slave节点;
  - Host: $YOUR_SLAVE_IP # 你的slave ip
    Pass: $YOUR_PASSWORD
    Type: master   #读写分离模式
    RWMode: slave # 如果是读写分离模式，那么这个用于标记master节点和slave节点;
```

2. 然后在`config.go`文件中添加`Redis`配置
``` go
package config

import (
	"sdp-access/commonlib/zapx"

	"github.com/cjstarcc/go-zero-RWCache/store/cache"  // 导入我的包
	"github.com/cjstarcc/go-zero-RWCache/store/redis"  // 导入我的包
	"github.com/cjstarcc/gorm-zero/gormc/config/mysql" // 导入我的包
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/zrpc"
    ...
)

type Config struct {
    ... // 其余相关配置
	CacheRedis cache.CacheConf 
}
```
3.在`svc`文件中初始化 
```  go
import (
	"github.com/cjstarcc/gorm-zero/gormc"
	"github.com/cjstarcc/go-zero-RWCache/store/redis"
	"github.com/cjstarcc/gorm-zero/gormc/config/mysql"
    ...
)

type ServiceContext struct {
    ...
    CachedConn        gormc.CachedConn
}

func NewServiceContext(c config.Config) *ServiceContext {

	db, err := mysql.Connect(c.Mysql)   //这里只要返回gorm的gorm.DB结构就行了，怎么返回不关心
	if err != nil {
		zapx.Logger.Error("db init", zap.Error(err))
	}
	return &ServiceContext{
		gormc.NewConn(db, c.CacheRedis,cache.WithExpiry(time.Second * 5),cache.WithNotFoundExpiry(time.Second * 5)), // 这里需要穿db，还有配置，cache.WithExpiry(time.Second * 5) 为设置缓存超时时间，cache.WithNotFoundExpiry(time.Second * 5) 为设置缓存如果没有找到占位符的超时时间
	}
}

```
4. 写业务代码

a. 单表缓存

在业务代码中添加如下代码

``` go


var (
	cacheSdpTrustAndroidManufacturerInfoTableIdPrefix = "cache:sdpTrust:androidManufacturerInfoTable:id:"  // 添加主键
)

type AndroidManufacturerInfoCache struct {
	cache gormc.CachedConn
}

func NewAndroidManufacturerInfoCache(db *gorm.DB, conf cache.CacheConf, opts ...cache.Option) AndroidManufacturerInfoCache {
	return AndroidManufacturerInfoCache{cache: gormc.NewConn(db, conf, opts...)}
}

//  查询单表数据
func (c *AndroidManufacturerInfoCache) QueryByIdByCache(ctx context.Context, id int) (*model.AndroidManufacturerInfoTable, error) {
	var resp model.AndroidManufacturerInfoTable
	err := c.cache.QueryCtx(ctx, &resp, c.formatPrimary(id), func(conn *gorm.DB, v interface{}) error {
		return conn.Model(&model.AndroidManufacturerInfoTable{}).Where("id = ?", id).First(&resp).Error
	})   // 通过调用 QueryCtx将数据通过json的格式缓存到redis
	switch err {
	case nil:
		return &resp, nil
	case gormc.ErrNotFound:
		return nil, gorm.ErrRecordNotFound
	default:
		return nil, err
	}
}

//  删除单表数据
func (c *AndroidManufacturerInfoCache) Update(ctx context.Context, tx *gorm.DB, data *model.AndroidManufacturerInfoTable) error {
	old, err := c.QueryByIdByCache(ctx, data.ID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	err = c.cache.ExecCtx(ctx, func(conn *gorm.DB) error {
		db := conn
		if tx != nil {
			db = tx
		}
		return db.Model(&model.AndroidManufacturerInfoTable{}).WithContext(ctx).Select(reflectStruct.RawFieldNamesExpectAutoSet(model.AndroidManufacturerInfoTable{})).Where("id = ?", data.ID).Updates(&data).Error
	}, c.getCacheKeys(old)...)   // 通过调用更改数据库数据，同时将缓存删除
	return err
}

```

同时在`svc`文件中初始化 

``` go
type ServiceContext struct {
    ...
    CachedConn        gormc.CachedConn
    AndroidManufacturerCache          cacheBiz.AndroidManufacturerInfoCache
}

func NewServiceContext(c config.Config) *ServiceContext {

	db, err := mysql.Connect(c.Mysql)   //这里只要返回gorm的gorm.DB结构就行了，怎么返回不关心
	if err != nil {
		zapx.Logger.Error("db init", zap.Error(err))
	}
	return &ServiceContext{
		gormc.NewConn(db, c.CacheRedis,cache.WithExpiry(time.Second * 5),cache.WithNotFoundExpiry(time.Second * 5)), 
        AndroidManufacturerCache:          cacheBiz.NewAndroidManufacturerInfoCache(db, c.CacheRedis),
}
```

这样我们就可以将单表的数据进行缓存。

 b. 缓存我们自定义数据

 ``` go
 type resp struct {
	Data string
}
func QueryByIdByCache() ( error) {
    var resp resp
    id := "cache:1"
 	err := c.cache.QueryCtx(ctx, &resp, id, func(conn *gorm.DB, v interface{}) error {
        resp.Data = "hello"
        return nil  
	}) // 这里只要给resp赋值就行了，他会自动的将resp序列化
}

func update(resp resp, id string) ( error) {
    ids := append(ids,id)
	err = c.cache.ExecCtx(ctx, func(conn *gorm.DB) error {
        return nil
	}, ids...)   // 通过传ID的形式，让缓存删除
	return err
}
 ```