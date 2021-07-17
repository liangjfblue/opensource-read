// MIT License

// Copyright (c) 2018 Andy Pan

// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package ants

import (
	"runtime"
	"time"
)

// goWorker is the actual executor who runs the tasks,
// it starts a goroutine that accepts tasks and
// performs function calls.
// 工作者
type goWorker struct {
	// pool who owns this worker.
	// 多pool时，worker的归属
	pool *Pool

	// task is a job should be done.
	// 任务队列
	task chan func()

	// recycleTime will be update when putting a worker back into queue.
	recycleTime time.Time
}

// run starts a goroutine to repeat the process
// that performs the function calls.
// 1.自增运行worker
// 2.封装执行后的操作
//	2.1.recovery+回收pool+钩子+打印堆栈
// 3.执行任务
// 4.唤醒阻塞任务
func (w *goWorker) run() {
	// 增加运行worker数
	w.pool.incRunning()
	go func() {
		defer func() {
			// 减去运行worker数
			w.pool.decRunning()
			// worker放回pool
			w.pool.workerCache.Put(w)
			if p := recover(); p != nil {
				if ph := w.pool.options.PanicHandler; ph != nil {
					// panic钩子
					ph(p)
				} else {
					// 没panic钩子就打印出堆栈信息
					w.pool.options.Logger.Printf("worker exits from a panic: %v\n", p)
					var buf [4096]byte
					n := runtime.Stack(buf[:], false)
					w.pool.options.Logger.Printf("worker exits from panic: %s\n", string(buf[:n]))
				}
			}
			// Call Signal() here in case there are goroutines waiting for available workers.
			// 发送信号，通知有可用worker
			w.pool.cond.Signal()
		}()

		for f := range w.task {
			if f == nil {
				return
			}
			// 执行任务
			f()
			// worker回收到pool
			if ok := w.pool.revertWorker(w); !ok {
				return
			}
		}
	}()
}
