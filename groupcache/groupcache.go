/*
Copyright 2012 Google Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package groupcache provides a data loading mechanism with caching
// and de-duplication that works across a set of peer processes.
//
// Each data Get first consults its local cache, otherwise delegates
// to the requested key's canonical owner, which then checks its cache
// or finally gets the data.  In the common case, many concurrent
// cache misses across a set of peers for the same key result in just
// one cache fill.
package groupcache

import (
	"context"
	"errors"
	"math/rand"
	"strconv"
	"sync"
	"sync/atomic"

	pb "github.com/golang/groupcache/groupcachepb"
	"github.com/golang/groupcache/lru"
	"github.com/golang/groupcache/singleflight"
)

/**
核心组件:
1.group: 提供Set/Get, 管理cache
2.PeerPicker: peer选择算法接口
3.ProtoGetter: peer远程请求接口
4.HTTPPool: http请求peer实现
5.byteview: 只读视图实现
6.sinks: 数据类型转换
7.lru: 本地cache, lru管理
8.groupcachepb: protobul传输协议
9.consistenthash: 一致性hash管理peer

特色:
1.利用group作为一组逻辑server的管理者, 好处是方便按业务来定义Group
2.支持集群缓存, 通过一致性hash管理key落到哪个节点. 好处是避免节点挂了rehash压力
3.远程访问peer支持自定义, 默认是http
4.远程访问peer时采用waitgroup实现并发请求时, 只能一个请求, 其他直接返回, 减少不必要的请求
5.使用pb作为传输协议, 性能高
6.使用只读视图, 好处是提高并发读
7.提供用户自定义本地和远程不命中, 哪个源获取数据, 回源本地
*/

// A Getter loads data for a key.
// 提供接口, 用户自定义从哪个源获取没命中key的value
type Getter interface {
	// Get returns the value identified by key, populating dest.
	//
	// The returned data must be unversioned. That is, key must
	// uniquely describe the loaded data, without an implicit
	// current time, and without relying on cache expiration
	// mechanisms.
	Get(ctx context.Context, key string, dest Sink) error
}

// A GetterFunc implements Getter with a function.
// 函数类型数显接口, 实现类型转换
type GetterFunc func(ctx context.Context, key string, dest Sink) error

func (f GetterFunc) Get(ctx context.Context, key string, dest Sink) error {
	return f(ctx, key, dest)
}

var (
	mu sync.RWMutex
	// 逻辑分组server
	groups = make(map[string]*Group)

	initPeerServerOnce sync.Once
	initPeerServer     func()
)

// GetGroup returns the named group previously created with NewGroup, or
// nil if there's no such group.
func GetGroup(name string) *Group {
	mu.RLock()
	g := groups[name]
	mu.RUnlock()
	return g
}

// NewGroup creates a coordinated group-aware Getter from a Getter.
//
// The returned Getter tries (but does not guarantee) to run only one
// Get call at once for a given key across an entire set of peer
// processes. Concurrent callers both in the local process and in
// other processes receive copies of the answer once the original Get
// completes.
//
// The group name must be unique for each getter.
// 创建逻辑group server
func NewGroup(name string, cacheBytes int64, getter Getter) *Group {
	return newGroup(name, cacheBytes, getter, nil)
}

// If peers is nil, the peerPicker is called via a sync.Once to initialize it.
func newGroup(name string, cacheBytes int64, getter Getter, peers PeerPicker) *Group {
	// getter必须有
	if getter == nil {
		panic("nil Getter")
	}
	mu.Lock()
	defer mu.Unlock()
	// 创建时拉取一次所有节点信息
	initPeerServerOnce.Do(callInitPeerServer)
	// 重复创建
	if _, dup := groups[name]; dup {
		panic("duplicate registration of group " + name)
	}
	g := &Group{
		name:       name,
		getter:     getter,
		peers:      peers,
		cacheBytes: cacheBytes,
		loadGroup:  &singleflight.Group{},
	}
	// 是否有注册Group钩子
	if fn := newGroupHook; fn != nil {
		fn(g)
	}
	groups[name] = g
	return g
}

// newGroupHook, if non-nil, is called right after a new group is created.
var newGroupHook func(*Group)

// RegisterNewGroupHook registers a hook that is run each time
// a group is created.
func RegisterNewGroupHook(fn func(*Group)) {
	if newGroupHook != nil {
		panic("RegisterNewGroupHook called more than once")
	}
	newGroupHook = fn
}

// RegisterServerStart registers a hook that is run when the first
// group is created.
// server启动时注册函数
func RegisterServerStart(fn func()) {
	if initPeerServer != nil {
		panic("RegisterServerStart called more than once")
	}
	initPeerServer = fn
}

// callInitPeerServer 初始化所有节点信息
func callInitPeerServer() {
	if initPeerServer != nil {
		initPeerServer()
	}
}

// A Group is a cache namespace and associated data loaded spread over
// a group of 1 or more machines.
type Group struct {
	name       string
	getter     Getter
	peersOnce  sync.Once
	peers      PeerPicker // 所有节点
	cacheBytes int64      // 缓存大小 limit for sum of mainCache and hotCache size

	// mainCache is a cache of the keys for which this process
	// (amongst its peers) is authoritative. That is, this cache
	// contains keys which consistent hash on to this process's
	// peer number.
	// lru实现的cache
	mainCache cache

	// hotCache contains keys/values for which this peer is not
	// authoritative (otherwise they would be in mainCache), but
	// are popular enough to warrant mirroring in this process to
	// avoid going over the network to fetch from a peer.  Having
	// a hotCache avoids network hotspotting, where a peer's
	// network card could become the bottleneck on a popular key.
	// This cache is used sparingly to maximize the total number
	// of key/value pairs that can be stored globally.
	// 热key
	hotCache cache

	// loadGroup ensures that each key is only fetched once
	// (either locally or remotely), regardless of the number of
	// concurrent callers.
	// 并发只获取一次
	loadGroup flightGroup

	_ int32 // force Stats to be 8-byte aligned on 32-bit platforms

	// Stats are statistics on the group.
	// 统计信息
	Stats Stats
}

// flightGroup is defined as an interface which flightgroup.Group
// satisfies.  We define this so that we may test with an alternate
// implementation.
// 并发请求peer, 只请求一次, 其余阻塞等待获取本地缓存
type flightGroup interface {
	// Done is called when Do is done.
	Do(key string, fn func() (interface{}, error)) (interface{}, error)
}

// Stats are per-group statistics.
// 统计信息
type Stats struct {
	Gets           AtomicInt // any Get request, including from peers
	CacheHits      AtomicInt // either cache was good
	PeerLoads      AtomicInt // either remote load or remote cache hit (not an error)
	PeerErrors     AtomicInt
	Loads          AtomicInt // (gets - cacheHits)
	LoadsDeduped   AtomicInt // after singleflight
	LocalLoads     AtomicInt // total good local loads
	LocalLoadErrs  AtomicInt // total bad local loads
	ServerRequests AtomicInt // gets that came over the network from peers
}

// Name returns the name of the group.
func (g *Group) Name() string {
	return g.name
}

func (g *Group) initPeers() {
	if g.peers == nil {
		g.peers = getPeers(g.name)
	}
}

// Get 获取key
func (g *Group) Get(ctx context.Context, key string, dest Sink) error {
	// 确保一个只初始化一次所有节点信息(todo 重分配还原, 下次Get会再次调用)
	g.peersOnce.Do(g.initPeers)
	// 统计获取+1
	g.Stats.Gets.Add(1)
	if dest == nil {
		return errors.New("groupcache: nil dest Sink")
	}
	// 从本地缓存获取key
	value, cacheHit := g.lookupCache(key)

	// 命中直接返回
	if cacheHit {
		g.Stats.CacheHits.Add(1)
		return setSinkView(dest, value)
	}
	// 不命中

	// Optimization to avoid double unmarshalling or copying: keep
	// track of whether the dest was already populated. One caller
	// (if local) will set this; the losers will not. The common
	// case will likely be one caller.
	// 存在并发请求peer时只有一个可以请求
	destPopulated := false
	value, destPopulated, err := g.load(ctx, key, dest)
	if err != nil {
		return err
	}
	// 不是当前请求去请求
	if destPopulated {
		return nil
	}
	return setSinkView(dest, value)
}

// load loads key either by invoking the getter locally or by sending it to another machine.
func (g *Group) load(ctx context.Context, key string, dest Sink) (value ByteView, destPopulated bool, err error) {
	g.Stats.Loads.Add(1)
	viewi, err := g.loadGroup.Do(key, func() (interface{}, error) {
		// Check the cache again because singleflight can only dedup calls
		// that overlap concurrently.  It's possible for 2 concurrent
		// requests to miss the cache, resulting in 2 load() calls.  An
		// unfortunate goroutine scheduling would result in this callback
		// being run twice, serially.  If we don't check the cache again,
		// cache.nbytes would be incremented below even though there will
		// be only one entry for this key.
		//
		// Consider the following serialized event ordering for two
		// goroutines in which this callback gets called twice for the
		// same key:
		// 1: Get("key")
		// 2: Get("key")
		// 1: lookupCache("key")
		// 2: lookupCache("key")
		// 1: load("key")
		// 2: load("key")
		// 1: loadGroup.Do("key", fn)
		// 1: fn()
		// 2: loadGroup.Do("key", fn)
		// 2: fn()
		// 再次确认是否本地缓存, 避免远程请求开销
		if value, cacheHit := g.lookupCache(key); cacheHit {
			g.Stats.CacheHits.Add(1)
			return value, nil
		}
		g.Stats.LoadsDeduped.Add(1)
		var value ByteView
		var err error
		// 一致性hash, 根据key计算出peer
		if peer, ok := g.peers.PickPeer(key); ok {
			// http请求peer
			value, err = g.getFromPeer(ctx, peer, key)
			if err == nil {
				// 请求成功
				g.Stats.PeerLoads.Add(1)
				return value, nil
			}
			// 请求失败, 没key
			g.Stats.PeerErrors.Add(1)
			// TODO(bradfitz): log the peer's error? keep
			// log of the past few for /groupcachez?  It's
			// probably boring (normal task movement), so not
			// worth logging I imagine.
		}
		value, err = g.getLocally(ctx, key, dest)
		if err != nil {
			g.Stats.LocalLoadErrs.Add(1)
			return nil, err
		}
		g.Stats.LocalLoads.Add(1)
		destPopulated = true // only one caller of load gets this return value
		g.populateCache(key, value, &g.mainCache)
		return value, nil
	})
	if err == nil {
		value = viewi.(ByteView)
	}
	return
}

func (g *Group) getLocally(ctx context.Context, key string, dest Sink) (ByteView, error) {
	err := g.getter.Get(ctx, key, dest)
	if err != nil {
		return ByteView{}, err
	}
	return dest.view()
}

func (g *Group) getFromPeer(ctx context.Context, peer ProtoGetter, key string) (ByteView, error) {
	// 构造请求
	req := &pb.GetRequest{
		Group: &g.name,
		Key:   &key,
	}
	res := &pb.GetResponse{}
	// 请求peer
	err := peer.Get(ctx, req, res)
	if err != nil {
		return ByteView{}, err
	}
	// 只读视图
	value := ByteView{b: res.Value}
	// TODO(bradfitz): use res.MinuteQps or something smart to
	// conditionally populate hotCache.  For now just do it some
	// percentage of the time.
	// 热key统计, 填充
	if rand.Intn(10) == 0 {
		g.populateCache(key, value, &g.hotCache)
	}
	return value, nil
}

// lookupCache 本地缓存查找key
func (g *Group) lookupCache(key string) (value ByteView, ok bool) {
	if g.cacheBytes <= 0 {
		return
	}
	// 先从main再从热key查找
	value, ok = g.mainCache.get(key)
	if ok {
		return
	}
	value, ok = g.hotCache.get(key)
	return
}

// populateCache 把请求peer的key-value缓存本地
func (g *Group) populateCache(key string, value ByteView, cache *cache) {
	if g.cacheBytes <= 0 {
		return
	}
	// 加入本地缓存
	cache.add(key, value)

	// Evict items from cache(s) if necessary.
	// 加入热key, 若超出最大限制, 先淘汰旧的
	for {
		mainBytes := g.mainCache.bytes()
		hotBytes := g.hotCache.bytes()
		if mainBytes+hotBytes <= g.cacheBytes {
			return
		}

		// TODO(bradfitz): this is good-enough-for-now logic.
		// It should be something based on measurements and/or
		// respecting the costs of different resources.
		victim := &g.mainCache
		if hotBytes > mainBytes/8 {
			victim = &g.hotCache
		}
		// lru淘汰
		victim.removeOldest()
	}
}

// CacheType represents a type of cache.
type CacheType int

const (
	// The MainCache is the cache for items that this peer is the
	// owner for.
	MainCache CacheType = iota + 1

	// The HotCache is the cache for items that seem popular
	// enough to replicate to this node, even though it's not the
	// owner.
	HotCache
)

// CacheStats returns stats about the provided cache within the group.
func (g *Group) CacheStats(which CacheType) CacheStats {
	switch which {
	case MainCache:
		return g.mainCache.stats()
	case HotCache:
		return g.hotCache.stats()
	default:
		return CacheStats{}
	}
}

// cache is a wrapper around an *lru.Cache that adds synchronization,
// makes values always be ByteView, and counts the size of all keys and
// values.
// 缓存结构
type cache struct {
	mu         sync.RWMutex
	nbytes     int64      // of all keys and values
	lru        *lru.Cache // lru结构缓存
	nhit, nget int64
	nevict     int64 // number of evictions
}

// 统计信息
func (c *cache) stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return CacheStats{
		Bytes:     c.nbytes,
		Items:     c.itemsLocked(),
		Gets:      c.nget,
		Hits:      c.nhit,
		Evictions: c.nevict,
	}
}

// 加入本地缓存
func (c *cache) add(key string, value ByteView) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lru == nil {
		// 延迟初始化
		c.lru = &lru.Cache{
			OnEvicted: func(key lru.Key, value interface{}) {
				val := value.(ByteView)
				c.nbytes -= int64(len(key.(string))) + int64(val.Len())
				c.nevict++
			},
		}
	}
	// 加入lru缓存
	c.lru.Add(key, value)
	// 更新大小
	c.nbytes += int64(len(key)) + int64(value.Len())
}

// get 获取
func (c *cache) get(key string) (value ByteView, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nget++
	if c.lru == nil {
		return
	}
	// lru get
	vi, ok := c.lru.Get(key)
	if !ok {
		return
	}
	c.nhit++
	return vi.(ByteView), true
}

func (c *cache) removeOldest() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lru != nil {
		c.lru.RemoveOldest()
	}
}

func (c *cache) bytes() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.nbytes
}

func (c *cache) items() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.itemsLocked()
}

func (c *cache) itemsLocked() int64 {
	if c.lru == nil {
		return 0
	}
	return int64(c.lru.Len())
}

// An AtomicInt is an int64 to be accessed atomically.
type AtomicInt int64

// Add atomically adds n to i.
func (i *AtomicInt) Add(n int64) {
	atomic.AddInt64((*int64)(i), n)
}

// Get atomically gets the value of i.
func (i *AtomicInt) Get() int64 {
	return atomic.LoadInt64((*int64)(i))
}

func (i *AtomicInt) String() string {
	return strconv.FormatInt(i.Get(), 10)
}

// CacheStats are returned by stats accessors on Group.
type CacheStats struct {
	Bytes     int64
	Items     int64
	Gets      int64
	Hits      int64
	Evictions int64
}
