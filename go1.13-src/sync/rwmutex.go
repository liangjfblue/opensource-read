// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sync

import (
	"internal/race"
	"sync/atomic"
	"unsafe"
)

// There is a modified copy of this file in runtime/rwmutex.go.
// If you make any changes here, see if you should make them there.

// A RWMutex is a reader/writer mutual exclusion lock.
// The lock can be held by an arbitrary number of readers or a single writer.
// The zero value for a RWMutex is an unlocked mutex.
//
// A RWMutex must not be copied after first use.
//
// If a goroutine holds a RWMutex for reading and another goroutine might
// call Lock, no goroutine should expect to be able to acquire a read lock
// until the initial read lock is released. In particular, this prohibits
// recursive read locking. This is to ensure that the lock eventually becomes
// available; a blocked Lock call excludes new readers from acquiring the
// lock.
/**
在Mutex的基础上增加信号量, 待读总数readerCount, 写等待读总数readerWait三个信息来实现读写锁
rwmutexMaxReaders用于标记是否有wirter
RLock()读锁:
	readerCount加一, 如果当前小于0, 证明当前有写锁, 睡眠阻塞等待

RUnlock()
	readerCount减一, 如果当前小于0, 证明有写锁想占有; readerWait减一, 若为0, 证明writer前的reader执行完了, 此时唤醒writer, 否则继续睡眠

Lock()
	上排它锁, 计算出writer前面的reader总数, 若writer前有reader, readerWait加上reader数, writer睡眠

Unlock()
	若释放未加锁的锁, panic; 唤醒所有reader, 释放排它锁

由上可知, 读锁是通过原子计数和信号量来实现, 加读锁只是原子计数; 由于有写锁, 会增加一个rwmutexMaxReaders翻转+-来达到标记当前有写锁, 实现读写锁的区分,
并且防止写锁一直等待, 拿不到锁, 增加了readerWait来记录上写锁时前面的reader, writer前的reader释放锁, writer就会拿到锁, 不用等到所有reader释放锁
	--------------readerCount----------
    --------------readerWait
	|----------------|----------------|
    -----reader----writer----reader---

看图, 正常上读锁, 释放读锁, 没问; 当有读锁时, 记录下writer的位置, 等待writer前的reader释放读锁(readerCount减), 唤醒writer, 等到writer释放锁,
唤醒writer后的reader
 */


type RWMutex struct {
	w           Mutex  // 互斥锁 held if there are pending writers
	writerSem   uint32 // writer等待reader信号量 semaphore for writers to wait for completing readers
	readerSem   uint32 // reader等待writer信号量 semaphore for readers to wait for completing writers
	readerCount int32  // 待处理的读总数 number of pending readers
	readerWait  int32  // 退出读的总数 writer前等待的reader释放完锁, 就到writer获取锁, 避免写锁一直拿不到 number of departing readers
}

// 最大的reader总数
const rwmutexMaxReaders = 1 << 30

// RLock locks rw for reading.
//
// It should not be used for recursive read locking; a blocked Lock
// call excludes new readers from acquiring the lock. See the
// documentation on the RWMutex type.
func (rw *RWMutex) RLock() {
	if race.Enabled {
		_ = rw.w.state
		race.Disable()
	}
	// 若当前有写锁, 睡眠等待(翻转readerCount-rwmutexMaxReaders为负)
	if atomic.AddInt32(&rw.readerCount, 1) < 0 {
		// A writer is pending, wait for it.
		runtime_SemacquireMutex(&rw.readerSem, false, 0)
	}
	if race.Enabled {
		race.Enable()
		race.Acquire(unsafe.Pointer(&rw.readerSem))
	}
}

// RUnlock undoes a single RLock call;
// it does not affect other simultaneous readers.
// It is a run-time error if rw is not locked for reading
// on entry to RUnlock.
func (rw *RWMutex) RUnlock() {
	if race.Enabled {
		_ = rw.w.state
		race.ReleaseMerge(unsafe.Pointer(&rw.writerSem))
		race.Disable()
	}
	// 读者减一, 若readerCount小于0, 可能当前有写锁或者释放被上锁的锁
	if r := atomic.AddInt32(&rw.readerCount, -1); r < 0 {
		// Outlined slow-path to allow the fast-path to be inlined
		rw.rUnlockSlow(r)
	}
	if race.Enabled {
		race.Enable()
	}
}

func (rw *RWMutex) rUnlockSlow(r int32) {
	// 释放被上锁的锁
	if r+1 == 0 || r+1 == -rwmutexMaxReaders {
		race.Enable()
		throw("sync: RUnlock of unlocked RWMutex")
	}
	// A writer is pending.
	// 如果当前有writer在等待(公平性, 防止饥饿)
	if atomic.AddInt32(&rw.readerWait, -1) == 0 {
		// The last reader unblocks the writer.
		// 唤醒writer
		runtime_Semrelease(&rw.writerSem, false, 1)
	}
}

// Lock locks rw for writing.
// If the lock is already locked for reading or writing,
// Lock blocks until the lock is available.
func (rw *RWMutex) Lock() {
	if race.Enabled {
		_ = rw.w.state
		race.Disable()
	}
	// First, resolve competition with other writers.
	// 上排它锁
	rw.w.Lock()
	// Announce to readers there is a pending writer.
	// 计算出writer前面的reader数量, 通知reader有writer获取锁
	r := atomic.AddInt32(&rw.readerCount, -rwmutexMaxReaders) + rwmutexMaxReaders
	// Wait for active readers.
	// writer前面等待的reader总数
	if r != 0 && atomic.AddInt32(&rw.readerWait, r) != 0 {
		// 让writer睡眠等待前面的reader释放锁
		runtime_SemacquireMutex(&rw.writerSem, false, 0)
	}
	if race.Enabled {
		race.Enable()
		race.Acquire(unsafe.Pointer(&rw.readerSem))
		race.Acquire(unsafe.Pointer(&rw.writerSem))
	}
}

// Unlock unlocks rw for writing. It is a run-time error if rw is
// not locked for writing on entry to Unlock.
//
// As with Mutexes, a locked RWMutex is not associated with a particular
// goroutine. One goroutine may RLock (Lock) a RWMutex and then
// arrange for another goroutine to RUnlock (Unlock) it.
func (rw *RWMutex) Unlock() {
	if race.Enabled {
		_ = rw.w.state
		race.Release(unsafe.Pointer(&rw.readerSem))
		race.Disable()
	}

	// Announce to readers there is no active writer.
	// 通知reader没有writer
	r := atomic.AddInt32(&rw.readerCount, rwmutexMaxReaders)
	if r >= rwmutexMaxReaders {
		race.Enable()
		throw("sync: Unlock of unlocked RWMutex")
	}
	// Unblock blocked readers, if any.
	// 唤醒writer后面的reader
	for i := 0; i < int(r); i++ {
		runtime_Semrelease(&rw.readerSem, false, 0)
	}
	// Allow other writers to proceed.
	rw.w.Unlock()
	if race.Enabled {
		race.Enable()
	}
}

// RLocker returns a Locker interface that implements
// the Lock and Unlock methods by calling rw.RLock and rw.RUnlock.
func (rw *RWMutex) RLocker() Locker {
	return (*rlocker)(rw)
}

type rlocker RWMutex

func (r *rlocker) Lock()   { (*RWMutex)(r).RLock() }
func (r *rlocker) Unlock() { (*RWMutex)(r).RUnlock() }
