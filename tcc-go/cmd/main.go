package main

import (
	"fmt"
	"net/http"
	"tcc_transaction/global/various"
	"tcc_transaction/task"
)

var serverName = "/tcc"

// 总结:
// 1.saga分布式事务, 编排式TCC
// 2.try/confirm/cancel操作
// 3.etcd存储服务的try/confirm/cancel API配置, 并支持实时监控更新配置
// 4.定时检测异常状态事务, 重试或邮件告警人工干预
// 5.客户端触发事务, http请求TCC server, 由TCC server代理请求真正的服务try/confirm/cancel接口

func main() {
	various.InitAll()
	http.Handle("/", http.FileServer(http.Dir("file")))
	// 用于决定使用哪种tcc逻辑，自定义或默认
	// tcc入口, 内部选择对应tcc操作
	var rtnHandle = func(t tcc) func(http.ResponseWriter, *http.Request) {
		p := &proxy{}
		p.t = t
		return p.process
	}
	http.HandleFunc("/tcc/examples/", rtnHandle(NewExampleTcc()))
	http.HandleFunc(fmt.Sprintf("%s/", serverName), rtnHandle(NewDefaultTcc()))

	// 定时检查异常记录, 重试或邮件告警
	go task.Start()

	// tcc服务
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		panic(err)
	}
}
