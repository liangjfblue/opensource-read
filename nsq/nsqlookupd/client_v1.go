package nsqlookupd

import (
	"net"
)

//封装nsqd连接conn
type ClientV1 struct {
	net.Conn
	peerInfo *PeerInfo
}

func NewClientV1(conn net.Conn) *ClientV1 {
	return &ClientV1{
		Conn: conn,
	}
}

func (c *ClientV1) String() string {
	return c.RemoteAddr().String()
}
