// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package sync provides basic synchronization primitives such as mutual
// exclusion locks. Other than the Once and WaitGroup types, most are intended
// for use by low-level library routines. Higher-level synchronization is
// better done via channels and communication.
//
// Values containing the types defined in this package should not be copied.

// mutex在性能, 公平性等方面做权衡, 通过state字段的不同标记来达到是否上锁标记/等待者标记/唤醒标记/饥饿标记.
// mutex有两种模式: 1.正常模式, 2.饥饿模式.
// 正常模式, 按照队列的先进先出的顺序来获取锁; 饥饿模式下, 若唤醒者等待超过1ms, 那么可以直接获取锁, 不用和新goroutine竞争.

// 上锁时, 不能快速获得锁, goroutine会通过自旋直至被唤醒/有唤醒/有等待者, 减少切换goroutine栈, ,让出cpu控制权的消耗, 等待被唤醒或者获得锁.

// 正常获取锁是按照队列先进先出的顺序来获取锁的, 而且考虑到性能的问题, 会假设新goroutine先拿到锁, 减少了goroutine的等待, 因此会把新goroutine插入到队列头,
// 但这会造成其他旧的goroutine发生饥饿, 一直或很难拿到锁.

// 增加饥饿优化, 如果等待锁超过1ms, 那么会先拿到锁, 不遵循队列的拿锁顺序
package sync

import (
	"internal/race"
	"sync/atomic"
	"unsafe"
)

func throw(string) // provided by runtime

// A Mutex is a mutual exclusion lock.
// The zero value for a Mutex is an unlocked mutex.
//
// A Mutex must not be copied after first use.
type Mutex struct {
	state int32
	sema  uint32
}

// A Locker represents an object that can be locked and unlocked.
type Locker interface {
	Lock()
	Unlock()
}

const (
	mutexLocked = 1 << iota // mutex is locked
	mutexWoken
	mutexStarving
	mutexWaiterShift = iota

	// Mutex fairness.
	//
	// Mutex can be in 2 modes of operations: normal and starvation.
	// In normal mode waiters are queued in FIFO order, but a woken up waiter
	// does not own the mutex and competes with new arriving goroutines over
	// the ownership. New arriving goroutines have an advantage -- they are
	// already running on CPU and there can be lots of them, so a woken up
	// waiter has good chances of losing. In such case it is queued at front
	// of the wait queue. If a waiter fails to acquire the mutex for more than 1ms,
	// it switches mutex to the starvation mode.
	//
	// In starvation mode ownership of the mutex is directly handed off from
	// the unlocking goroutine to the waiter at the front of the queue.
	// New arriving goroutines don't try to acquire the mutex even if it appears
	// to be unlocked, and don't try to spin. Instead they queue themselves at
	// the tail of the wait queue.
	//
	// If a waiter receives ownership of the mutex and sees that either
	// (1) it is the last waiter in the queue, or (2) it waited for less than 1 ms,
	// it switches mutex back to normal operation mode.
	//
	// Normal mode has considerably better performance as a goroutine can acquire
	// a mutex several times in a row even if there are blocked waiters.
	// Starvation mode is important to prevent pathological cases of tail latency.
	starvationThresholdNs = 1e6
)

// Lock locks m.
// If the lock is already in use, the calling goroutine
// blocks until the mutex is available.
func (m *Mutex) Lock() {
	// Fast path: grab unlocked mutex.
	// 快速之路: 一下子获得锁
	if atomic.CompareAndSwapInt32(&m.state, 0, mutexLocked) {
		if race.Enabled {
			race.Acquire(unsafe.Pointer(m))
		}
		return
	}
	// Slow path (outlined so that the fast path can be inlined)
	// 慢速之路:尝试自旋或者饥饿状态下饥饿goroutine竞争
	m.lockSlow()
}

func (m *Mutex) lockSlow() {
	var waitStartTime int64
	starving := false // 饥饿标记
	awoke := false    // 唤醒标记
	iter := 0         //自旋次数
	old := m.state
	for {
		// Don't spin in starvation mode, ownership is handed off to waiters
		// so we won't be able to acquire the mutex anyway.
		// 锁是非饥饿状态, 加锁状态, goroutine尝试自旋
		if old&(mutexLocked|mutexStarving) == mutexLocked && runtime_canSpin(iter) {
			// Active spinning makes sense.
			// Try to set mutexWoken flag to inform Unlock
			// to not wake other blocked goroutines.
			// 自旋过程中, 前面没有等待唤醒, 且有等待锁的goroutine, 那么CAS设置唤醒状态 ==> 唤醒标记
			if !awoke && old&mutexWoken == 0 && old>>mutexWaiterShift != 0 &&
				atomic.CompareAndSwapInt32(&m.state, old, old|mutexWoken) {
				// 锁状态更新唤醒状态
				awoke = true
			}
			// 自旋
			runtime_doSpin()
			iter++
			old = m.state
			continue
		}
		new := old
		// 唤醒后处理

		// Don't try to acquire starving mutex, new arriving goroutines must queue.
		//唤醒后, 发现是非饥饿状态, 设置锁状态为加锁
		if old&mutexStarving == 0 {
			// 非饥饿状态, 加锁
			new |= mutexLocked
		}
		// 唤醒后, 发现锁状态为加锁, 或是饥饿状态 ==> waiter+1
		if old&(mutexLocked|mutexStarving) != 0 {
			// waiter加1
			new += 1 << mutexWaiterShift
		}
		// The current goroutine switches mutex to starvation mode.
		// But if the mutex is currently unlocked, don't do the switch.
		// Unlock expects that starving mutex has waiters, which will not
		// be true in this case.
		// 唤醒后, 若是饥饿状态, 且锁状态为加锁 ==> 仍然是饥饿状态
		if starving && old&mutexLocked != 0 {
			// 饥饿标记, 当前是加锁, 设置饥饿状态
			new |= mutexStarving
		}
		// 若是被唤醒
		if awoke {
			// The goroutine has been woken from sleep,
			// so we need to reset the flag in either case.
			// 唤醒后, 锁状态的唤醒标记为0, 矛盾
			if new&mutexWoken == 0 {
				throw("sync: inconsistent mutex state")
			}
			// 锁新状态清除唤醒标记
			new &^= mutexWoken
		}
		// 设置新状态
		if atomic.CompareAndSwapInt32(&m.state, old, new) {
			// 原来的锁状态已释放, 且是非饥饿状态,正常请求到了锁
			if old&(mutexLocked|mutexStarving) == 0 {
				break // locked the mutex with CAS
			}
			// 处理饥饿状态
			// If we were already waiting before, queue at the front of the queue.
			// 如果以前已经在队列, 插到队头
			queueLifo := waitStartTime != 0
			if waitStartTime == 0 {
				waitStartTime = runtime_nanotime()
			}
			// 阻塞等待
			runtime_SemacquireMutex(&m.sema, queueLifo, 1)
			// 唤醒之后检测锁是否饥饿状态, 或是否等待超过1ms, 超过1ms直接
			starving = starving || runtime_nanotime()-waitStartTime > starvationThresholdNs
			old = m.state
			// 如果锁已经处于饥饿状态, 直接得到锁
			if old&mutexStarving != 0 {
				// If this goroutine was woken and mutex is in starvation mode,
				// ownership was handed off to us but mutex is in somewhat
				// inconsistent state: mutexLocked is not set and we are still
				// accounted as waiter. Fix that.
				// 如果锁上锁，或处于饥饿模式, 或者没有waiter
				if old&(mutexLocked|mutexWoken) != 0 || old>>mutexWaiterShift == 0 {
					throw("sync: inconsistent mutex state")
				}
				// 加锁并且将waiter减一
				delta := int32(mutexLocked - 1<<mutexWaiterShift)
				if !starving || old>>mutexWaiterShift == 1 {
					// Exit starvation mode.
					// Critical to do it here and consider wait time.
					// Starvation mode is so inefficient, that two goroutines
					// can go lock-step infinitely once they switch mutex
					// to starvation mode.
					// 退出饥饿模式: 最后一个waiter或者已经不是饥饿状态, 清除
					delta -= mutexStarving
				}
				atomic.AddInt32(&m.state, delta)
				break
			}
			awoke = true
			iter = 0
		} else {
			old = m.state
		}
	}

	if race.Enabled {
		race.Acquire(unsafe.Pointer(m))
	}
}

// Unlock unlocks m.
// It is a run-time error if m is not locked on entry to Unlock.
//
// A locked Mutex is not associated with a particular goroutine.
// It is allowed for one goroutine to lock a Mutex and then
// arrange for another goroutine to unlock it.
func (m *Mutex) Unlock() {
	if race.Enabled {
		_ = m.state
		race.Release(unsafe.Pointer(m))
	}

	// Fast path: drop lock bit.
	// 快速之路: 释放锁(更新锁状态)
	new := atomic.AddInt32(&m.state, -mutexLocked)
	if new != 0 {
		// Outlined slow path to allow inlining the fast path.
		// To hide unlockSlow during tracing we skip one extra frame when tracing GoUnblock.
		m.unlockSlow(new)
	}
}

func (m *Mutex) unlockSlow(new int32) {
	// 释放非加锁的锁, panic
	/**
	  1001 0011
	  -1
	  ==>
	  1001 0010
	  &1
	  ==>
	  1001 0010
	  0000 0001
	  &
	  ==>
	  0
	*/
	if (new+mutexLocked)&mutexLocked == 0 {
		throw("sync: unlock of unlocked mutex")
	}
	// 非饥饿状态
	if new&mutexStarving == 0 {
		old := new
		for {
			// If there are no waiters or a goroutine has already
			// been woken or grabbed the lock, no need to wake anyone.
			// In starvation mode ownership is directly handed off from unlocking
			// goroutine to the next waiter. We are not part of this chain,
			// since we did not observe mutexStarving when we unlocked the mutex above.
			// So get off the way.
			// 若无waiter, 或者已设置加锁/唤醒/饥饿状态, 不需要做处理, 直接退出
			if old>>mutexWaiterShift == 0 || old&(mutexLocked|mutexWoken|mutexStarving) != 0 {
				return
			}
			// Grab the right to wake someone.
			// waiter减1, 唤醒goroutine
			new = (old - 1<<mutexWaiterShift) | mutexWoken
			if atomic.CompareAndSwapInt32(&m.state, old, new) {
				runtime_Semrelease(&m.sema, false, 1)
				return
			}
			old = m.state
		}
	} else {
		// Starving mode: handoff mutex ownership to the next waiter.
		// Note: mutexLocked is not set, the waiter will set it after wakeup.
		// But mutex is still considered locked if mutexStarving is set,
		// so new coming goroutines won't acquire it.
		// 若是饥饿状态, 直接把锁交给等待队列的队头goroutine
		runtime_Semrelease(&m.sema, true, 1)
	}
}
