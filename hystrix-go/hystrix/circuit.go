package hystrix

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// CircuitBreaker is created for each ExecutorPool to track whether requests
// should be attempted, or rejected if the Health of the circuit is too low.
//断路器
type CircuitBreaker struct {
	Name                   string
	open                   bool
	forceOpen              bool
	mutex                  *sync.RWMutex
	openedOrLastTestedTime int64

	executorPool *executorPool//流量控制器抽象
	metrics      *metricExchange//统计控制器抽象
}

var (
	circuitBreakersMutex *sync.RWMutex
	circuitBreakers      map[string]*CircuitBreaker
)

func init() {
	circuitBreakersMutex = &sync.RWMutex{}
	circuitBreakers = make(map[string]*CircuitBreaker)
}

// GetCircuit returns the circuit for the given command and whether this call created it.
func GetCircuit(name string) (*CircuitBreaker, bool, error) {
	circuitBreakersMutex.RLock()
	_, ok := circuitBreakers[name]
	if !ok {
		circuitBreakersMutex.RUnlock()
		circuitBreakersMutex.Lock()
		defer circuitBreakersMutex.Unlock()
		// because we released the rlock before we obtained the exclusive lock,
		// we need to double check that some other thread didn't beat us to
		// creation.
		if cb, ok := circuitBreakers[name]; ok {
			return cb, false, nil
		}
		//延迟创建, 节省资源
		circuitBreakers[name] = newCircuitBreaker(name)
	} else {
		defer circuitBreakersMutex.RUnlock()
	}

	return circuitBreakers[name], !ok, nil
}

// Flush purges all circuit and metric information from memory.
func Flush() {
	circuitBreakersMutex.Lock()
	defer circuitBreakersMutex.Unlock()

	for name, cb := range circuitBreakers {
		cb.metrics.Reset()
		cb.executorPool.Metrics.Reset()
		delete(circuitBreakers, name)
	}
}

// newCircuitBreaker creates a CircuitBreaker with associated Health
//创建断路器, 其实就是创建统计控制器和流量控制器
func newCircuitBreaker(name string) *CircuitBreaker {
	c := &CircuitBreaker{}
	c.Name = name
	c.metrics = newMetricExchange(name)
	c.executorPool = newExecutorPool(name)
	c.mutex = &sync.RWMutex{}

	return c
}

// toggleForceOpen allows manually causing the fallback logic for all instances
// of a given command.
func (circuit *CircuitBreaker) toggleForceOpen(toggle bool) error {
	circuit, _, err := GetCircuit(circuit.Name)
	if err != nil {
		return err
	}

	circuit.forceOpen = toggle
	return nil
}

// IsOpen is called before any Command execution to check whether or
// not it should be attempted. An "open" circuit means it is disabled.
//断路器是否打开
func (circuit *CircuitBreaker) IsOpen() bool {
	circuit.mutex.RLock()
	o := circuit.forceOpen || circuit.open
	circuit.mutex.RUnlock()

	if o {
		return true
	}

	//请求数小于设定的初始请求起始数直接默认是关闭
	if uint64(circuit.metrics.Requests().Sum(time.Now())) < getSettings(circuit.Name).RequestVolumeThreshold {
		return false
	}

	//错误率大于阈值, 打开断路器
	if !circuit.metrics.IsHealthy(time.Now()) {
		// too many failures, open the circuit
		circuit.setOpen()
		return true
	}

	return false
}

// AllowRequest is checked before a command executes, ensuring that circuit state and metric health allow it.
// When the circuit is open, this call will occasionally return true to measure whether the external service
// has recovered.
func (circuit *CircuitBreaker) AllowRequest() bool {
	return !circuit.IsOpen() || circuit.allowSingleTest()
}

func (circuit *CircuitBreaker) allowSingleTest() bool {
	circuit.mutex.RLock()
	defer circuit.mutex.RUnlock()

	now := time.Now().UnixNano()
	openedOrLastTestedTime := atomic.LoadInt64(&circuit.openedOrLastTestedTime)
	if circuit.open && now > openedOrLastTestedTime+getSettings(circuit.Name).SleepWindow.Nanoseconds() {
		swapped := atomic.CompareAndSwapInt64(&circuit.openedOrLastTestedTime, openedOrLastTestedTime, now)
		if swapped {
			log.Printf("hystrix-go: allowing single test to possibly close circuit %v", circuit.Name)
		}
		return swapped
	}

	return false
}
//setOpen 打开断路器
func (circuit *CircuitBreaker) setOpen() {
	circuit.mutex.Lock()
	defer circuit.mutex.Unlock()

	if circuit.open {
		return
	}

	log.Printf("hystrix-go: opening circuit %v", circuit.Name)

	circuit.openedOrLastTestedTime = time.Now().UnixNano()
	circuit.open = true
}

//setOpen 关闭断路器
func (circuit *CircuitBreaker) setClose() {
	circuit.mutex.Lock()
	defer circuit.mutex.Unlock()

	if !circuit.open {
		return
	}

	log.Printf("hystrix-go: closing circuit %v", circuit.Name)

	circuit.open = false
	circuit.metrics.Reset()
}

// ReportEvent records command metrics for tracking recent error rates and exposing data to the dashboard.
//ReportEvent 上报执行状态信息给统计控制器
func (circuit *CircuitBreaker) ReportEvent(eventTypes []string, start time.Time, runDuration time.Duration) error {
	if len(eventTypes) == 0 {
		return fmt.Errorf("no event types sent for metrics")
	}

	circuit.mutex.RLock()
	o := circuit.open
	circuit.mutex.RUnlock()
	//只要有成功就关闭断路器
	if eventTypes[0] == "success" && o {
		circuit.setClose()
	}

	//计算可用Tickets
	var concurrencyInUse float64
	if circuit.executorPool.Max > 0 {
		concurrencyInUse = float64(circuit.executorPool.ActiveCount()) / float64(circuit.executorPool.Max)
	}

	select {
	//上报统计命令信息到底层的MetricCollector
	case circuit.metrics.Updates <- &commandExecution{
		Types:            eventTypes,
		Start:            start,
		RunDuration:      runDuration,
		ConcurrencyInUse: concurrencyInUse,
	}:
	default:
		return CircuitError{Message: fmt.Sprintf("metrics channel (%v) is at capacity", circuit.Name)}
	}

	return nil
}
