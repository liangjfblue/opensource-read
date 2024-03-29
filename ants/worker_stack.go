package ants

import "time"

// 栈方式 实现工作池
type workerStack struct {
	items  []*goWorker
	expiry []*goWorker
	size   int
}

func newWorkerStack(size int) *workerStack {
	return &workerStack{
		items: make([]*goWorker, 0, size),
		size:  size,
	}
}

func (wq *workerStack) len() int {
	return len(wq.items)
}

func (wq *workerStack) isEmpty() bool {
	return len(wq.items) == 0
}

// 添加worker
func (wq *workerStack) insert(worker *goWorker) error {
	wq.items = append(wq.items, worker)
	return nil
}

// 取出worker，后进先出。最大程度利用可用的worker，但是会造成栈底的worker长时间没任务，可能会过期。适用于任务频繁的场景
func (wq *workerStack) detach() *goWorker {
	l := wq.len()
	if l == 0 {
		return nil
	}

	w := wq.items[l-1]
	wq.items[l-1] = nil // avoid memory leaks
	wq.items = wq.items[:l-1]

	return w
}

//
func (wq *workerStack) retrieveExpiry(duration time.Duration) []*goWorker {
	n := wq.len()
	if n == 0 {
		return nil
	}

	expiryTime := time.Now().Add(-duration)
	// 二分法，查找过期的下标（调用时会更新recycleTime，因此放回栈后时，从栈底到顶是按照时间旧到新的顺序）
	// 因此，找出的下标前面是过期可回收的worker
	index := wq.binarySearch(0, n-1, expiryTime)

	// 清空旧的过期列表
	wq.expiry = wq.expiry[:0]
	if index != -1 {
		// 新的过期列表，二分法查找出的可期worker
		wq.expiry = append(wq.expiry, wq.items[:index+1]...)
		// 深拷贝，复用items，把后面的往前挪，释放过期的
		m := copy(wq.items, wq.items[index+1:])
		for i := m; i < n; i++ {
			wq.items[i] = nil
		}
		wq.items = wq.items[:m]
	}
	return wq.expiry
}

func (wq *workerStack) binarySearch(l, r int, expiryTime time.Time) int {
	var mid int
	for l <= r {
		mid = (l + r) / 2
		// 找出过期的最大下标
		if expiryTime.Before(wq.items[mid].recycleTime) {
			r = mid - 1
		} else {
			l = mid + 1
		}
	}
	return r
}

func (wq *workerStack) reset() {
	for i := 0; i < wq.len(); i++ {
		wq.items[i].task <- nil
		wq.items[i] = nil
	}
	wq.items = wq.items[:0]
}
