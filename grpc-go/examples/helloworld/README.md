greeterClient(helloworld_grpc.pb.go)
	ClientConnInterface(grpc-go/clientconn.go)
        ClientConn实现ClientConnInterface接口

## client
grpc帮我们做啥?
1.一键根据IDL文件(proto文件)生成服务的接口
2.封装通讯底层细节(编解码/解压缩/消息包构造-解析/查找rpc注册接口/响应handler/read-write)

客户端:
1.创建grpc链接(ClientConn)
2.构造rpc服务的客户端(代码生成)
3.调用rpc接口(代码生成)
	3.1.调用ClientConn.Invoke, grpc底层的通信(ClientConn实现ClientConnInterface接口)
	3.2.创建传输流(grpc是基于http2, 流传输)
	3.3.调用SendMsg发送rpc请求(构造消息header, 发送消息)
	3.4.调用http2Client的Write
	3.5.最终数据到缓冲区
	3.6.等待接收响应

## server
grpc帮我们做啥?
1.创建listen socket
2.构建grpc服务的服务端
3.注册服务
	1.利用grpc通过IDL代码生成的服务, 得到服务的元数据(服务名, 服务的方法)
	2.注册服务到本地服务列表
4.响应客户端请求服务
	1.升级协议为http2
	2.创建传输流
	3.读数据(解压->解码->反序列化)
	4.从服务注册列表中查看服务的响应方法, 响应
	5.返回结果给客户端(序列化->编码->压缩)












