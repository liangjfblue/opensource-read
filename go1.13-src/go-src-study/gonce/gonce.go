package gonce

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

type GOnce struct {
	sync.Once
}

// IsDone 扩展sync.Once, 支持查询是否执行过
func (o *GOnce) IsDone() bool {
	return atomic.LoadUint32((*uint32)(unsafe.Pointer(&o.Once))) == 1
}
