package hystrix

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type runFunc func() error
type fallbackFunc func(error) error
type runFuncC func(context.Context) error
type fallbackFuncC func(context.Context, error) error

// A CircuitError is an error which models various failure states of execution,
// such as the circuit being open or a timeout.
type CircuitError struct {
	Message string
}

func (e CircuitError) Error() string {
	return "hystrix: " + e.Message
}

// command models the state used for a single execution on a circuit. "hystrix command" is commonly
// used to describe the pairing of your run/fallback functions with a circuit.
type command struct {
	sync.Mutex

	ticket      *struct{}
	start       time.Time
	errChan     chan error
	finished    chan bool
	circuit     *CircuitBreaker
	run         runFuncC
	fallback    fallbackFuncC
	runDuration time.Duration
	events      []string
}

var (
	// ErrMaxConcurrency occurs when too many of the same named command are executed at the same time.
	ErrMaxConcurrency = CircuitError{Message: "max concurrency"}
	// ErrCircuitOpen returns when an execution attempt "short circuits". This happens due to the circuit being measured as unhealthy.
	ErrCircuitOpen = CircuitError{Message: "circuit open"}
	// ErrTimeout occurs when the provided function takes too long to execute.
	ErrTimeout = CircuitError{Message: "timeout"}
)

// Go runs your function while tracking the health of previous calls to it.
// If your function begins slowing down or failing repeatedly, we will block
// new calls to it for you to give the dependent service time to repair.
//
// Define a fallback function if you want to define some code to execute during outages.
func Go(name string, run runFunc, fallback fallbackFunc) chan error {
	runC := func(ctx context.Context) error {
		return run()
	}
	var fallbackC fallbackFuncC
	if fallback != nil {
		fallbackC = func(ctx context.Context, err error) error {
			return fallback(err)
		}
	}
	return GoC(context.Background(), name, runC, fallbackC)
}

// GoC runs your function while tracking the health of previous calls to it.
// If your function begins slowing down or failing repeatedly, we will block
// new calls to it for you to give the dependent service time to repair.
//
// Define a fallback function if you want to define some code to execute during outages.
func GoC(ctx context.Context, name string, run runFuncC, fallback fallbackFuncC) chan error {
	//构造一次命令
	cmd := &command{
		run:      run,
		fallback: fallback,
		start:    time.Now(),
		errChan:  make(chan error, 1),
		finished: make(chan bool, 1),
	}

	// dont have methods with explicit params and returns
	// let data come in and out naturally, like with any closure
	// explicit error return to give place for us to kill switch the operation (fallback)

	//获取命令的断路器
	circuit, _, err := GetCircuit(name)
	if err != nil {
		cmd.errChan <- err
		return cmd.errChan
	}
	cmd.circuit = circuit
	//用于通知回收ticket
	ticketCond := sync.NewCond(cmd)
	ticketChecked := false
	// When the caller extracts error from returned errChan, it's assumed that
	// the ticket's been returned to executorPool. Therefore, returnTicket() can
	// not run after cmd.errorWithFallback().
	//回收ticket, 流量控制
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
	// Shared by the following two goroutines. It ensures only the faster
	// goroutine runs errWithFallback() and reportAllEvent().
	//上报当前请求的执行状态信息
	returnOnce := &sync.Once{}
	reportAllEvent := func() {
		err := cmd.circuit.ReportEvent(cmd.events, cmd.start, cmd.runDuration)
		if err != nil {
			log.Printf(err.Error())
		}
	}

	go func() {
		defer func() { cmd.finished <- true }()

		// Circuits get opened when recent executions have shown to have a high error rate.
		// Rejecting new executions allows backends to recover, and the circuit will allow
		// new traffic when it feels a healthly state has returned.
		//断路器是否打开?
		if !cmd.circuit.AllowRequest() {
			cmd.Lock()
			// It's safe for another goroutine to go ahead releasing a nil ticket.

			//在另外一个goroutine[returnTicket]会等待请求完成或者超时, 前提是若ticketChecked为false,会一直等待
			//检测是否有ticket到来, 因为在这个goroutine因为断路器结束了, 不会有下面执行请求和归还ticket并设置ticketChecked
			//为true和ticketCond.Signal()通知goroutine[returnTicket], 因此goroutine[returnTicket]不会回收ticket
			//所以这里要设置ticketChecked为true和通知ticketCond, 所以即使断路器打开了也能回收ticket
			ticketChecked = true
			ticketCond.Signal()
			cmd.Unlock()
			returnOnce.Do(func() {
				//回收ticket
				returnTicket()
				//回调错误处理函数
				cmd.errorWithFallback(ctx, ErrCircuitOpen)
				//上报执行状态信息
				reportAllEvent()
			})
			return
		}

		// As backends falter, requests take longer but don't always fail.
		//
		// When requests slow down but the incoming rate of requests stays the same, you have to
		// run more at a time to keep up. By controlling concurrency during these situations, you can
		// shed load which accumulates due to the increasing ratio of active commands to incoming requests.
		cmd.Lock()
		select {
		//等待取ticket, 才能执行下面真正的请求处理
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

		//执行真正的处理函数
		runStart := time.Now()
		runErr := run(ctx)
		returnOnce.Do(func() {
			//上报执行状态信息
			defer reportAllEvent()
			cmd.runDuration = time.Since(runStart)
			returnTicket()
			if runErr != nil {
				cmd.errorWithFallback(ctx, runErr)
				return
			}
			cmd.reportEvent("success")
		})
	}()

	//等待成功执行/主动结束(通过ctx)/超时
	go func() {
		timer := time.NewTimer(getSettings(name).Timeout)
		defer timer.Stop()

		select {
		case <-cmd.finished:
			// returnOnce has been executed in another goroutine
		case <-ctx.Done():
			returnOnce.Do(func() {
				returnTicket()
				cmd.errorWithFallback(ctx, ctx.Err())
				reportAllEvent()
			})
			return
		case <-timer.C:
			returnOnce.Do(func() {
				returnTicket()
				cmd.errorWithFallback(ctx, ErrTimeout)
				reportAllEvent()
			})
			return
		}
	}()

	return cmd.errChan
}

// Do runs your function in a synchronous manner, blocking until either your function succeeds
// or an error is returned, including hystrix circuit errors
func Do(name string, run runFunc, fallback fallbackFunc) error {
	runC := func(ctx context.Context) error {
		return run()
	}
	var fallbackC fallbackFuncC
	if fallback != nil {
		fallbackC = func(ctx context.Context, err error) error {
			return fallback(err)
		}
	}
	return DoC(context.Background(), name, runC, fallbackC)
}

// DoC runs your function in a synchronous manner, blocking until either your function succeeds
// or an error is returned, including hystrix circuit errors
func DoC(ctx context.Context, name string, run runFuncC, fallback fallbackFuncC) error {
	done := make(chan struct{}, 1)

	//封装处理函数
	r := func(ctx context.Context) error {
		err := run(ctx)
		if err != nil {
			return err
		}

		done <- struct{}{}
		return nil
	}

	//封装回调函数
	f := func(ctx context.Context, e error) error {
		err := fallback(ctx, e)
		if err != nil {
			return err
		}

		done <- struct{}{}
		return nil
	}

	//通过chan接收 错误
	var errChan chan error
	if fallback == nil {
		errChan = GoC(ctx, name, r, nil)
	} else {
		errChan = GoC(ctx, name, r, f)
	}

	//等待结束
	select {
	case <-done:
		return nil
	case err := <-errChan:
		return err
	}
}

//reportEvent 汇总上报事件
func (c *command) reportEvent(eventType string) {
	c.Lock()
	defer c.Unlock()

	c.events = append(c.events, eventType)
}

// errorWithFallback triggers the fallback while reporting the appropriate metric events.
func (c *command) errorWithFallback(ctx context.Context, err error) {
	eventType := "failure"
	if err == ErrCircuitOpen {
		eventType = "short-circuit"
	} else if err == ErrMaxConcurrency {
		eventType = "rejected"
	} else if err == ErrTimeout {
		eventType = "timeout"
	} else if err == context.Canceled {
		eventType = "context_canceled"
	} else if err == context.DeadlineExceeded {
		eventType = "context_deadline_exceeded"
	}

	c.reportEvent(eventType)
	fallbackErr := c.tryFallback(ctx, err)
	if fallbackErr != nil {
		c.errChan <- fallbackErr
	}
}

func (c *command) tryFallback(ctx context.Context, err error) error {
	if c.fallback == nil {
		// If we don't have a fallback return the original error.
		return err
	}

	//每个命令(资源)都可设一个错误回调函数
	fallbackErr := c.fallback(ctx, err)
	if fallbackErr != nil {
		//上报执行状态信息
		c.reportEvent("fallback-failure")
		return fmt.Errorf("fallback failed with '%v'. run error was '%v'", fallbackErr, err)
	}

	c.reportEvent("fallback-success")

	return nil
}
