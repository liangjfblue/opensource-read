package main

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

// 1.实现TryLock
// 参考mutex源码, 通过state字段的含义+atomic包来实现

const (
	mutexLocked      = 1 << iota // 是否加锁标记
	mutexWoken                   // 唤醒标记
	mutexStarving                // 处于饥饿标记
	mutexWaiterShift = iota      // 等待者数量
)

type GMutex struct {
	sync.Mutex
}

// TryLock 尝试获取锁, 拿不到直接返回false
func (m *GMutex) TryLock() bool {
	if atomic.CompareAndSwapInt32((*int32)(unsafe.Pointer(&m.Mutex)), 0, mutexLocked) {
		return true
	}

	oldMutex := atomic.LoadInt32((*int32)(unsafe.Pointer(&m.Mutex)))
	if oldMutex&(mutexLocked|mutexStarving|mutexWoken) != 0 {
		return false
	}

	newMutex := oldMutex | mutexLocked
	return atomic.CompareAndSwapInt32((*int32)(unsafe.Pointer(&m.Mutex)), oldMutex, newMutex)
}

// IsLock 是否加锁
func (m *GMutex) IsLock() bool {
	return atomic.LoadInt32((*int32)(unsafe.Pointer(&m.Mutex)))&mutexLocked == mutexLocked
}

// IsWoken 是否唤醒
func (m *GMutex) IsWoken() bool {
	return atomic.LoadInt32((*int32)(unsafe.Pointer(&m.Mutex)))&mutexWoken == mutexWoken
}

// IsStarving 是否饥饿
func (m *GMutex) IsStarving() bool {
	return atomic.LoadInt32((*int32)(unsafe.Pointer(&m.Mutex)))&mutexStarving == mutexStarving
}

func (m *GMutex) Count() int32 {
	state := atomic.LoadInt32((*int32)(unsafe.Pointer(&m.Mutex)))
	count := state >> mutexWaiterShift
	count += state & mutexLocked
	return count
}
