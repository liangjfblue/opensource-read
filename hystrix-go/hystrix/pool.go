package hystrix

type executorPool struct {
	Name    string
	Metrics *poolMetrics
	Max     int
	Tickets chan *struct{}
}

//newExecutorPool 创建执行线程池
func newExecutorPool(name string) *executorPool {
	p := &executorPool{}
	p.Name = name
	p.Metrics = newPoolMetrics(name)
	p.Max = getSettings(name).MaxConcurrentRequests

	//初始化ticket总数
	p.Tickets = make(chan *struct{}, p.Max)
	for i := 0; i < p.Max; i++ {
		p.Tickets <- &struct{}{}
	}

	return p
}

//Return 归还ticket
func (p *executorPool) Return(ticket *struct{}) {
	if ticket == nil {
		return
	}

	//更新线程池统计
	p.Metrics.Updates <- poolMetricsUpdate{
		activeCount: p.ActiveCount(),
	}
	//回收ticket
	p.Tickets <- ticket
}

//ActiveCount 已用ticket总数
func (p *executorPool) ActiveCount() int {
	return p.Max - len(p.Tickets)
}
