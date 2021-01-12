// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sync

import (
	"sync/atomic"
	"unsafe"
)

// Cond implements a condition variable, a rendezvous point
// for goroutines waiting for or announcing the occurrence
// of an event.
//
// Each Cond has an associated Locker L (often a *Mutex or *RWMutex),
// which must be held when changing the condition and
// when calling the Wait method.
//
// A Cond must not be copied after first use.
type Cond struct {
	// copy检查
	noCopy noCopy

	// L is held while observing or changing the condition
	// 改变条件时用到
	L Locker

	notify  notifyList
	checker copyChecker
}

// NewCond returns a new Cond with Locker l.
func NewCond(l Locker) *Cond {
	return &Cond{L: l}
}

// Wait atomically unlocks c.L and suspends execution
// of the calling goroutine. After later resuming execution,
// Wait locks c.L before returning. Unlike in other systems,
// Wait cannot return unless awoken by Broadcast or Signal.
//
// Because c.L is not locked when Wait first resumes, the caller
// typically cannot assume that the condition is true when
// Wait returns. Instead, the caller should Wait in a loop:
//
//    c.L.Lock()
//    for !condition() {
//        c.Wait()
//    }
//    ... make use of condition ...
//    c.L.Unlock()
//
// Wait 阻塞等待直到满足等待条件满足被唤醒被唤醒
func (c *Cond) Wait() {
	// 检查cond是否被复制
	c.checker.check()
	// 加入到等待队列中, 阻塞等待
	t := runtime_notifyListAdd(&c.notify)
	// 释放锁, 让其他goroutine有机会修改等待条件
	c.L.Unlock()
	// 阻塞休眠直到被唤醒
	runtime_notifyListWait(&c.notify, t)
	c.L.Lock()
}

// Signal wakes one goroutine waiting on c, if there is any.
//
// It is allowed but not required for the caller to hold c.L
// during the call.
// 唤醒一个
func (c *Cond) Signal() {
	// 检查cond是否被复制
	c.checker.check()
	// 唤醒
	runtime_notifyListNotifyOne(&c.notify)
}

// Broadcast wakes all goroutines waiting on c.
//
// It is allowed but not required for the caller to hold c.L
// during the call.
// 唤醒所有
func (c *Cond) Broadcast() {
	// 检查cond是否被复制
	c.checker.check()
	// 唤醒
	runtime_notifyListNotifyAll(&c.notify)
}

// copyChecker holds back pointer to itself to detect object copying.
type copyChecker uintptr

// check 检车cond是否有复制
func (c *copyChecker) check() {
	// nocopy指针是否相等, 是否不为0, 若是则被复制
	if uintptr(*c) != uintptr(unsafe.Pointer(c)) &&
		!atomic.CompareAndSwapUintptr((*uintptr)(c), 0, uintptr(unsafe.Pointer(c))) &&
		uintptr(*c) != uintptr(unsafe.Pointer(c)) {
		panic("sync.Cond is copied")
	}
}

// noCopy may be embedded into structs which must not be copied
// after the first use.
//
// See https://golang.org/issues/8005#issuecomment-190753527
// for details.
type noCopy struct{}

// Lock is a no-op used by -copylocks checker from `go vet`.
func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}
