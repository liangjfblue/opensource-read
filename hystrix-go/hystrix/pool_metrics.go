package hystrix

import (
	"sync"

	"github.com/afex/hystrix-go/hystrix/rolling"
)

//poolMetrics 线程资源的底层的窗口统计, 每个窗口统计的是已用线程数
type poolMetrics struct {
	Mutex   *sync.RWMutex
	//接收并发统计信息
	Updates chan poolMetricsUpdate

	Name              string
	//最大活跃请求数
	MaxActiveRequests *rolling.Number
	//执行统计
	Executed          *rolling.Number
}

type poolMetricsUpdate struct {
	activeCount int
}

//newPoolMetrics 创建线程池统计
func newPoolMetrics(name string) *poolMetrics {
	m := &poolMetrics{}
	m.Name = name
	m.Updates = make(chan poolMetricsUpdate)
	m.Mutex = &sync.RWMutex{}

	m.Reset()

	//开启goroutine监控更新
	go m.Monitor()

	return m
}

func (m *poolMetrics) Reset() {
	m.Mutex.Lock()
	defer m.Mutex.Unlock()

	m.MaxActiveRequests = rolling.NewNumber()
	m.Executed = rolling.NewNumber()
}

//Monitor 监听和更新窗口已用线程数
func (m *poolMetrics) Monitor() {
	for u := range m.Updates {
		//收到线程请求
		m.Mutex.RLock()

		//增加使用计数
		m.Executed.Increment(1)
		//更新活跃总数
		m.MaxActiveRequests.UpdateMax(float64(u.activeCount))

		m.Mutex.RUnlock()
	}
}
