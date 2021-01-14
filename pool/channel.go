package pool

import (
	"errors"
	"fmt"
	"net"
	"sync"
)

// 本项目的实现:
// 1. 连接池是用chan实现, 好处是直接利用chan的并发安全特性, 实现并发安全
// 2. 支持自定义Factory构造器
// 3. 连接池没有连接数量最大限制
// 4. 通过unusable标记和重写Close拦截是放回连接池还是直接关闭
// 5. 开箱即用, 非常稳定
// channelPool implements the Pool interface based on buffered channels.
// 连接池
type channelPool struct {
	// storage for our net.Conn connections
	mu sync.RWMutex
	// chan实现的连接池
	conns chan net.Conn

	// net.Conn generator
	// net.Conn的创建构造器
	factory Factory
}

// Factory is a function to create new connections.
// 创建net.Conn的构造器
type Factory func() (net.Conn, error)

// NewChannelPool returns a new pool based on buffered channels with an initial
// capacity and maximum capacity. Factory is used when initial capacity is
// greater than zero to fill the pool. A zero initialCap doesn't fill the Pool
// until a new Get() is called. During a Get(), If there is no new connection
// available in the pool, a new connection will be created via the Factory()
// method.
// 创建一个连接池
func NewChannelPool(initialCap, maxCap int, factory Factory) (Pool, error) {
	if initialCap < 0 || maxCap <= 0 || initialCap > maxCap {
		return nil, errors.New("invalid capacity settings")
	}

	c := &channelPool{
		conns:   make(chan net.Conn, maxCap),
		factory: factory,
	}

	// create initial connections, if something goes wrong,
	// just close the pool error out.
	// 初始化固定的连接
	for i := 0; i < initialCap; i++ {
		// 根据传入的net.Conn构造器创建连接
		conn, err := factory()
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("factory is not able to fill the pool: %s", err)
		}
		// 连接放入池中
		c.conns <- conn
	}

	return c, nil
}

// 获取连接池, net.Conn构造器
func (c *channelPool) getConnsAndFactory() (chan net.Conn, Factory) {
	c.mu.RLock()
	conns := c.conns
	factory := c.factory
	c.mu.RUnlock()
	return conns, factory
}

// Get implements the Pool interfaces Get() method. If there is no new
// connection available in the pool, a new connection will be created via the
// Factory() method.
// 从连接池中获取连接
func (c *channelPool) Get() (net.Conn, error) {
	// 获取连接池, net.Conn构造器
	conns, factory := c.getConnsAndFactory()
	// 未初始化, 退出
	if conns == nil {
		return nil, ErrClosed
	}

	// wrap our connections with out custom net.Conn implementation (wrapConn
	// method) that puts the connection back to the pool if it's closed.
	select {
	// 从池(chan)中获取连接
	case conn := <-conns:
		if conn == nil {
			return nil, ErrClosed
		}
		// 返回的是包装类, 扩展了2个: MarkUnusable和重写Close(close时拦截判断决定放回连接池还是彻底关闭连接)
		return c.wrapConn(conn), nil
	default:
		// 连接池没连接, 通过构造器创建新的连接
		// 注意: 此库对连接最大数量没限制
		conn, err := factory()
		if err != nil {
			return nil, err
		}

		return c.wrapConn(conn), nil
	}
}

// put puts the connection back to the pool. If the pool is full or closed,
// conn is simply closed. A nil conn will be rejected.
// 连接放回连接池
func (c *channelPool) put(conn net.Conn) error {
	if conn == nil {
		return errors.New("connection is nil. rejecting")
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	// 连接池关闭, 直接把连接关闭
	if c.conns == nil {
		// pool is closed, close passed connection
		return conn.Close()
	}

	// put the resource back into the pool. If the pool is full, this will
	// block and the default case will be executed.
	select {
	// 放回连接池, 当连接池满了, chan写不进, 走default
	case c.conns <- conn:
		return nil
	default:
		// pool is full, close passed connection
		return conn.Close()
	}
}

// 关闭连接池
func (c *channelPool) Close() {
	c.mu.Lock()
	conns := c.conns
	c.conns = nil
	c.factory = nil
	c.mu.Unlock()

	if conns == nil {
		return
	}

	// 关闭chan
	close(conns)
	// 关闭连接池中所有连接
	for conn := range conns {
		conn.Close()
	}
}

// 连接池当前的连接数
func (c *channelPool) Len() int {
	conns, _ := c.getConnsAndFactory()
	return len(conns)
}
