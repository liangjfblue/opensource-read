# nsqd的网络通信和对网络逻辑处理框架的思考

## nsqd网络通信
nsqd中关于通信的一些小点:

- tcp服务是用于监听客户端连接, 使用了多路复用模型(go标准库的tcp服务是基于epoll/kqueue/select等的)

- 暴露http服务, 提供接口直接发布消息到nsqd, 获取topic channel信息

- 元数据持久化元数据到文件是json字符串对象

- nsqd请求nsqlookup获取topic channel等数据是通过http请求获取的

- 客户端连接nsqd后,先发送4个字节的head, 主要是验证版本号

- 消费者连接snqd后, nsqd不会马上推送消息, 等到消费者发送 RDY>0 时才开始推送消息

- nsq通过RDY-FIN的机制, 来更新能够接收的消息数, 并在max-in-fight的25%时更新, 来达到客户端流控的作用

- 按行读取客户端发送的数据, \n或者\r\n切分, 命令之间用 " " 隔开

- nsqd推送消息格式

        // message format:
        // [x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x]...
        // |       (int64)        ||    ||      (hex string encoded in ASCII)           || (binary)
        // |       8-byte         ||    ||                 16-byte                      || N-byte
        // ------------------------------------------------------------------------------------------...
        //   nanosecond timestamp    ^^                   message ID                       message body
        //                        (uint16)
        //                         2-byte
        //                        attempts
        head+body
        head: 4字节整个包大小
        body: 数据包



## 对于大多数网络逻辑处理框架的思考
关键词: "读写分离", 生产者消费者模型, 异步

**读(面向每个client): **

- 每个client有单独的goroutine来负责client的请求
- 构造消息(消息id+clientID), 投入内存队列chan
- 接收客户端请求结束, 剩下的交给核心的消息处理goroutine
 
**写(面向nsqd): **

nsqd有一个核心的消息处理goroutine(messagePump)
- 负责监听内存队列chan的所有消息
- 根据消息(msg.clientID)找到client对象
- 推送消息给client

**二者的能够联动起来是由以下组成:**

- 消息绑定clientID(有一个消息map key-msgId, value-msg(id+clientID+data...))
- 内存队列(积压队列)和延时队列, 客户端goroutine负责读数据并构造消息, 投入队列, nsqd的核心的消息处理goroutine监听并取出根据clientID找到client, 最后发送给client就行

**同时有以下goroutine辅助:**

- 扫描消息队列goroutine负责把队列中的过期消息取出更新时间+重试+放回队列
- 统计信息goroutine负责统计topic, channel, 消息等信息

这样的架构很常见, 可以把读和写分离, 其实就是生产者-消费者模型. 通过队列来解耦, 并且起到缓冲的作用, 并且异步推送消息和重试确认消息, 达到提高性能的目的.


