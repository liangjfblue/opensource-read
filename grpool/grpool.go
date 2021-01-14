package grpool

import "sync"

/**
工作池分为3个组件:
1.工作池
2.工作者
3.任务调度器

工作池是入口, 首先创建工作池, 通过传入的参数numWorkers, jobQueueLen来确定工作者的数量, 任务队列长度.

在工作池初始化时, 会创建一个任务调度器, 传入jobQueue, workerPool给调度器. 创建调度器时会根据工作者数量来创建固定的工作者并start

经过以上初始化, 工作池, 调度器, 工作者三者就运行起来了

添加新任务, 任务投入工作池的JobQueue, 因调度器会监听JobQueue, 因此调度器收到新的添加的任务, 调度器因持有工作池, 会经工作池拿出一个
空闲的worker, 并把任务投入其任务队列jobChannel, 工作者会一直监听等待任务到来, 此时会收到任务Job, 最后执行Job().

以上就是整个工作池的工作原理.
*/

// Gorouting instance which can accept client jobs
type worker struct {
	workerPool chan *worker  //worker池
	jobChannel chan Job      //任务队列
	stop       chan struct{} //关闭标记
}

// worker池运行
func (w *worker) start() {
	go func() {
		var job Job
		for {
			// worker free, add it to pool
			// 把空闲worker放回工作池中
			w.workerPool <- w

			select {
			// 阻塞等待新任务
			case job = <-w.jobChannel:
				// 执行任务
				job()
			case <-w.stop:
				w.stop <- struct{}{}
				return
			}
		}
	}()
}

// 创建worker
func newWorker(pool chan *worker) *worker {
	return &worker{
		workerPool: pool,
		jobChannel: make(chan Job),
		stop:       make(chan struct{}),
	}
}

// Accepts jobs from clients, and waits for first free worker to deliver job
type dispatcher struct {
	workerPool chan *worker  // 引用worker池
	jobQueue   chan Job      // 任务队列
	stop       chan struct{} // 关闭标记
}

// 任务Job分发
func (d *dispatcher) dispatch() {
	for {
		select {
		// 阻塞等待新任务
		case job := <-d.jobQueue:
			// worker池拿出一个空闲的worker
			worker := <-d.workerPool
			// 把任务分发给空闲worker
			worker.jobChannel <- job
		case <-d.stop:
			// 通知所有worker关闭
			for i := 0; i < cap(d.workerPool); i++ {
				worker := <-d.workerPool

				worker.stop <- struct{}{}
				<-worker.stop
			}

			// 关闭任务分发器
			d.stop <- struct{}{}
			return
		}
	}
}

// 创建任务分发器, 通过chan接收任务Job
func newDispatcher(workerPool chan *worker, jobQueue chan Job) *dispatcher {
	d := &dispatcher{
		workerPool: workerPool,
		jobQueue:   jobQueue,
		stop:       make(chan struct{}),
	}

	// 初始化固定worker(goroutine), 运行等待任务(chan Job)
	for i := 0; i < cap(d.workerPool); i++ {
		worker := newWorker(d.workerPool)
		worker.start()
	}

	// 调度器运行
	go d.dispatch()
	return d
}

// Represents user request, function which should be executed in some worker.
// 任务处理函数类型
type Job func()

type Pool struct {
	JobQueue   chan Job       // 任务队列
	dispatcher *dispatcher    // 任务分发器
	wg         sync.WaitGroup // 控制任务并发和完成
}

// Will make pool of gorouting workers.
// numWorkers - how many workers will be created for this pool
// queueLen - how many jobs can we accept until we block
//
// Returned object contains JobQueue reference, which you can use to send job to pool.
// 实例化工作池
func NewPool(numWorkers int, jobQueueLen int) *Pool {
	jobQueue := make(chan Job, jobQueueLen)
	workerPool := make(chan *worker, numWorkers)

	pool := &Pool{
		JobQueue:   jobQueue,
		dispatcher: newDispatcher(workerPool, jobQueue),
	}

	return pool
}

// In case you are using WaitAll fn, you should call this method
// every time your job is done.
//
// If you are not using WaitAll then we assume you have your own way of synchronizing.
// 任务完成, 实质就是wg.done
func (p *Pool) JobDone() {
	p.wg.Done()
}

// How many jobs we should wait when calling WaitAll.
// It is using WaitGroup Add/Done/Wait
// 等待n个任务完成, 实质是借助wg.Add添加
func (p *Pool) WaitCount(count int) {
	p.wg.Add(count)
}

// Will wait for all jobs to finish.
// 等待任务完成, 实质是借助wg.Wait实现
func (p *Pool) WaitAll() {
	p.wg.Wait()
}

// Will release resources used by pool
// 释放工作池
func (p *Pool) Release() {
	p.dispatcher.stop <- struct{}{}
	<-p.dispatcher.stop
}
