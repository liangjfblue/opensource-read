package nsqlookupd

import (
	"fmt"
	"log"
	"net"
	"os"
	"sync"

	"github.com/nsqio/nsq/internal/http_api"
	"github.com/nsqio/nsq/internal/protocol"
	"github.com/nsqio/nsq/internal/util"
	"github.com/nsqio/nsq/internal/version"
)

/**
nsqlookup的逻辑比较简单
1.启动tcp监听nsqd的连接, 负责nsqd的鉴权,注册,注销,信息缓存在nsqlookup等
2.典型的tcp服务端, 每个nsqd单独一个goroutine负责读写
3.处理PING/IDENTIFY/REGISTER/UNREGISTER等事件
4.启动http服务, 供nsqadmin请求获取
5.内存中缓存nsqd的节点信息, 供客户端请求查询nsqd连接
 */

type NSQLookupd struct {
	sync.RWMutex
	opts         *Options
	//tcp 监听nsqd	4160
	tcpListener  net.Listener
	//http供nsqadmin访问	4161
	httpListener net.Listener
	tcpServer    *tcpServer
	waitGroup    util.WaitGroupWrapper
	DB           *RegistrationDB
}

func New(opts *Options) (*NSQLookupd, error) {
	var err error

	//初始化日志
	if opts.Logger == nil {
		opts.Logger = log.New(os.Stderr, opts.LogPrefix, log.Ldate|log.Ltime|log.Lmicroseconds)
	}

	//实例化nsqlookup
	l := &NSQLookupd{
		opts: opts,
		DB:   NewRegistrationDB(),
	}

	l.logf(LOG_INFO, version.String("nsqlookupd"))

	//tcp 监听nsqd	http供nsqadmin访问
	l.tcpListener, err = net.Listen("tcp", opts.TCPAddress)
	if err != nil {
		return nil, fmt.Errorf("listen (%s) failed - %s", opts.TCPAddress, err)
	}
	l.httpListener, err = net.Listen("tcp", opts.HTTPAddress)
	if err != nil {
		return nil, fmt.Errorf("listen (%s) failed - %s", opts.HTTPAddress, err)
	}

	return l, nil
}

// Main starts an instance of nsqlookupd and returns an
// error if there was a problem starting up.
//启动nsqlookup
func (l *NSQLookupd) Main() error {
	ctx := &Context{l}

	exitCh := make(chan error)
	var once sync.Once
	exitFunc := func(err error) {
		once.Do(func() {
			if err != nil {
				l.logf(LOG_FATAL, "%s", err)
			}
			exitCh <- err
		})
	}

	//tcp server
	l.tcpServer = &tcpServer{ctx: ctx}
	l.waitGroup.Wrap(func() {
		exitFunc(protocol.TCPServer(l.tcpListener, l.tcpServer, l.logf))
	})

	//http servere
	httpServer := newHTTPServer(ctx)
	l.waitGroup.Wrap(func() {
		exitFunc(http_api.Serve(l.httpListener, httpServer, "HTTP", l.logf))
	})

	err := <-exitCh
	return err
}

func (l *NSQLookupd) RealTCPAddr() *net.TCPAddr {
	return l.tcpListener.Addr().(*net.TCPAddr)
}

func (l *NSQLookupd) RealHTTPAddr() *net.TCPAddr {
	return l.httpListener.Addr().(*net.TCPAddr)
}

//Exit 退出时释放资源
func (l *NSQLookupd) Exit() {
	if l.tcpListener != nil {
		l.tcpListener.Close()
	}

	if l.tcpServer != nil {
		l.tcpServer.CloseAll()
	}

	if l.httpListener != nil {
		l.httpListener.Close()
	}
	l.waitGroup.Wait()
}
