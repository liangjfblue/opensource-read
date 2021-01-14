package pool

import (
	"net"
	"sync"
)

// PoolConn is a wrapper around net.Conn to modify the the behavior of
// net.Conn's Close() method.
type PoolConn struct {
	net.Conn
	mu       sync.RWMutex // 读写锁
	c        *channelPool // chan实现的连接池, 连接引用, 用于close时放回连接池
	unusable bool         // 标记conn不可用, 彻底关闭
}

// Close() puts the given connects back to the pool instead of closing it.
// 重写close, 主要是判断unusable标记拦截, 彻底关闭连接和返回连接池
func (p *PoolConn) Close() error {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// 彻底关闭连接?
	if p.unusable {
		if p.Conn != nil {
			return p.Conn.Close()
		}
		return nil
	}
	// 放回连接池
	return p.c.put(p.Conn)
}

// MarkUnusable() marks the connection not uable any more, to let the pool close it instead of returning it to pool.
// 标记当前连接废了, close时直接关闭
func (p *PoolConn) MarkUnusable() {
	p.mu.Lock()
	p.unusable = true
	p.mu.Unlock()
}

// newConn wraps a standard net.Conn to a poolConn net.Conn.
// 包装net.Conn
func (c *channelPool) wrapConn(conn net.Conn) net.Conn {
	p := &PoolConn{c: c}
	p.Conn = conn
	return p
}
