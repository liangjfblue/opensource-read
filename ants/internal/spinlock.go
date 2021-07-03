// Copyright 2019 Andy Pan & Dietoad. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package internal

import (
	"runtime"
	"sync"
	"sync/atomic"
)

type spinLock uint32

const maxBackoff = 64

func (sl *spinLock) Lock() {
	backoff := 1
	// 一直检测sl是否为0, 为0则置1, 并退出
	for !atomic.CompareAndSwapUint32((*uint32)(sl), 0, 1) {
		// 利用指数退避算法, see https://en.wikipedia.org/wiki/Exponential_backoff.
		// 指数退避方法
		for i := 0; i < backoff; i++ {
			runtime.Gosched()
		}
		if backoff < maxBackoff {
			backoff <<= 1
		}
	}
}

func (sl *spinLock) Unlock() {
	// 原子置零,通知自旋退出
	atomic.StoreUint32((*uint32)(sl), 0)
}

// NewSpinLock instantiates a spin-lock.
func NewSpinLock() sync.Locker {
	return new(spinLock)
}
