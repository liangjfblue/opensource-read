# hystrix
## hystrix介绍
[hystrix](https://github.com/afex/hystrix-go)采用的是命令模式, 把一个请求抽象封装成一个命令, 视是同步调用还是异步调用来创建goroutine, 下面用命令来代表一次真实的请求

hystrix是状态机轮转, 断路器开-断路器关-探测断路器, 默认是关, 错误率达到阈值就转变为开, 从而触发熔断, 快速错误返回和降级, 保护资源

每个资源的断路器创建是在对应资源有请求过来时创建的, 预先资源的规则(最大并发,超时,窗口大小,打开阀门,错误率), 
延迟创建的设计,可以最大程度的利用机器资源, 不会预先创建那么多资源的monitor,断路器等.
        
每个资源都有独立的Metrics统计对象, 线程池(通过Tickets来控制并发), Metric对象和ExecutorPool工作池是通过updates chan
来监听每个请求的命令信息和并发信息(GoC中ReportEvent上报)

hystrix会在设置的时间窗口检测断路器, 尝试处理命令, 若正常处理, 断路器转变为关, 从而再次允许流量的进来

## hystrix核心结构
hystrix包括了三核心模块: 执行状态上报模块, 统计控制器, 流量控制器

hystrix的结构:

```markdown
hystrix
	断路器circuit
		统计控制器MetricCollector
			统计字段管理对象rolling.Number
		流量控制器poolMetrics
		    统计字段管理对象rolling.Number
        上报执行状态
```

## 执行状态上报模块
命令执行的流程:

```go
Do->
    DoC->
        GoC->
            GetCircuit->
                newCircuitBreaker->
                    newMetricExchange->
                        go m.Monitor()->
                            for update := range m.Updates {go m.IncrementMetrics}
                    newExecutorPool->
                        go m.Monitor()
        run(ctx)
        returnTicket()
        reportAllEvent()
```

在DoC封装处理函数, 错误处理函数, 然后传入GoC执行. 

GoC的处理流程:
- 1.构建本次执行的命令
- 2.获取断路器(若没有, 就创建[统计控制器和流量控制器,并开启monitor]), 
- 3.封装一些关键步骤为匿名函数(回收ticket, 上报执行状态)
- 4.判断断路器状态
    - 1.打开, 就直接返回并上报执行状态给统计控制器.
    - 2.关闭, 可以往下执行
- 5.获取ticket(通过ticket来控制并发), 若超过最大并发上报执行状态并返回
- 6.执行命令, 完了上报执行状态给统计控制器, 回收ticket

处理函数的执行 和 ticket的回收和超时回收ticket是在不同的goroutine执行的, 二者是通过条件变量来达到同步的. **必须是获取过ticket或者超过最大并发通知回收ticket** 

上报状态给统计控制器流程:
- 1.Goc调用封装的reportAllEvent(), 每个命令的断路器调用ReportEvent上报(事件,开始时间,执行时间)
- 2.若执行成功且当前断路器是打开的, 那么就关闭断路器
- 3.计算剩余并发concurrencyInUse
- 4.构建命令状态commandExecution, 投入到统计控制器metrics的Updates chan
- 5.每个命令的断路器的统计控制器会有一个monitor goroutine在后台监听Updates chan, 接收命令状态commandExecution
- 6.更新统计控制器的统计数据(go m.IncrementMetrics(wg, collector, update, totalDuration)), 
- 7.构建一个MetricResult对象, 然后调用统计控制器的collector.Update(r)来更新当前窗口的统计数据
- 8.最终就是更新底层Number的Value计数


## 统计控制器
统计控制器是hystrix最底层对于每个命令的执行状态统计, hystrix提供了一个默认的统计控制器DefaultMetricCollector,

```go
type DefaultMetricCollector struct {
	mutex *sync.RWMutex

	numRequests *rolling.Number
	errors      *rolling.Number

	successes               *rolling.Number
	failures                *rolling.Number
	rejects                 *rolling.Number
	shortCircuits           *rolling.Number
	timeouts                *rolling.Number
	contextCanceled         *rolling.Number
	contextDeadlineExceeded *rolling.Number

	fallbackSuccesses *rolling.Number
	fallbackFailures  *rolling.Number
	totalDuration     *rolling.Timing
	runDuration       *rolling.Timing
}
```

通过字段信息, 看出来MetricCollector对一个命令的所有统计都在里面了. 比较特殊的是 rolling.Number.

rolling.Number是统计控制器的统计字段的底层对象, 是一个N个小格的窗口, 用于平滑统计, 包括了获取当前窗口, 删除10秒以外的窗口,
增加当前窗口的计数,更新窗口最大值,对最近10秒内存储桶上的值进行求和,返回过去10秒内看到的最大值等功能接口. 主要是用于执行状态上报给
统计控制器时的窗口统计更新

通过按时间段来划分小窗口, 进而统计大窗口的方法, 可以平滑掉突刺现象. 使统计更加平滑

Monitor()会监听上报事件, 有事件时会调用IncrementMetrics(), 在IncrementMetrics()里面调用collector.Update(r)来更新
一次执行状态统计

```go
func (d *DefaultMetricCollector) Update(r MetricResult) {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	d.numRequests.Increment(r.Attempts)
	d.errors.Increment(r.Errors)
	d.successes.Increment(r.Successes)
	d.failures.Increment(r.Failures)
	d.rejects.Increment(r.Rejects)
	d.shortCircuits.Increment(r.ShortCircuits)
	d.timeouts.Increment(r.Timeouts)
	d.fallbackSuccesses.Increment(r.FallbackSuccesses)
	d.fallbackFailures.Increment(r.FallbackFailures)
	d.contextCanceled.Increment(r.ContextCanceled)
	d.contextDeadlineExceeded.Increment(r.ContextDeadlineExceeded)

	d.totalDuration.Add(r.TotalDuration)
	d.runDuration.Add(r.RunDuration)
}

func (r *Number) Increment(i float64) {
	if i == 0 {
		return
	}

	r.Mutex.Lock()
	defer r.Mutex.Unlock()

	b := r.getCurrentBucket()
	b.Value += i
	r.removeOldBuckets()
}
```

可以看到统计控制器MetricCollector的更新就是每个字段调用Number的Increment来对当前窗口累计计数

## 流量控制器
hystrix 的流量控制是通过参数MaxConcurrentRequests来设定的, 假如是100, 表示1秒最大并发是100, 分为10个窗口来统计, 所以可以
平滑的统计, 不会有突刺现象.

### hystrix的流量控制流程
hystrix的流量控制是通过ticket来控制的, 流程如下:
- 1.每次请求进来, 先判断断路器是否打开, 
    - 1.若关闭状态,继续步骤2
    - 2.若打开状态报错返回和上报执行状态给统计控制器
- 2.判断是否有ticket
    - 1.若没有表示超过并发, 直接错误返回和上报统计控制器
    - 2.若有ticket, 继续步骤3的命令处理
 - 3.命令处理完最终回收ticket, 以便其他请求申请获取ticket

看源代码, Goc中此处是判断并发和获取ticket

```go
select {
//等待获取ticket, 然后继续执行下面真正的命令处理
case cmd.ticket = <-circuit.executorPool.Tickets:
    ticketChecked = true
    ticketCond.Signal()
    cmd.Unlock()
default:
    //获取不到ticket, 表示超过并发, 通知回收ticket
    ticketChecked = true
    ticketCond.Signal()
    cmd.Unlock()
    returnOnce.Do(func() {
        //可以回收ticket
        returnTicket()
        cmd.errorWithFallback(ctx, ErrMaxConcurrency)
        //上报执行状态信息
        reportAllEvent()
    })
    return
}
```
 
命令处理结束, 回收ticket	

```go
returnTicket := func() {
    cmd.Lock()
    // Avoid releasing before a ticket is acquired.
    //等待归还ticket
    for !ticketChecked {
        ticketCond.Wait()
    }
    //归还ticket,流量控制上报状态, 通过ticket来控制最大并发
    cmd.circuit.executorPool.Return(cmd.ticket)
    cmd.Unlock()
}
 
```

流量控制器poolMetrics底层也是划分窗口统计并发的, 最底层的统计对象也是rolling.Number, 所以逻辑和统计控制器是一致的. 不过统计的对象换为
窗口的线程数(其实就是ticket的使用总数)

## 总结
hystrix的设计还是比较好的, 通过命令模式, 把用户处理和执行解耦; 通过断路器来变更hystrix的状态, 达到控制熔断降级的目的; 
分为前台和后台, 前台就是一个简单的Do/Go接口, 然后通过后台的控制控制器和流量控制器来实现对资源的统计和系统的保护; 底层通过
滑动窗口的方法来平滑掉突刺现象, 更好的统计执行状态数据和并发统计.

可以学习的地方有设计模式的应用, 匿名函数封装处理过程, 锁对共享资源的保护, 并发的抽象(ticket控制并发), 滑动窗口计数限流法




