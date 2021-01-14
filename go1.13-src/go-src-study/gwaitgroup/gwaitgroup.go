package gwaitgroup

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

//		 	state [0]	state [1]	state [2]
// 64 位	waiter		counter		sema
// 32 位	sema		waiter		counter

type GWaitGroup struct {
	wg sync.WaitGroup
}

func (g *GWaitGroup)waiterCount() (waiter uint32, counter uint32) {
	// 空结构体空间为0
	wg :=(*[3]uint32)(unsafe.Pointer(&g.wg))
	if uintptr(unsafe.Pointer(&g.wg))%8 == 0 {
		atomic.LoadUint32(&(*wg)[0])
		waiter,counter = atomic.LoadUint32(&(*wg)[0]),  atomic.LoadUint32(&(*wg)[1])
	} else {
		waiter,counter = atomic.LoadUint32(&(*wg)[1]),  atomic.LoadUint32(&(*wg)[2])
	}
	return
}
