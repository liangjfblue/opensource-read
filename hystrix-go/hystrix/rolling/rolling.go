package rolling

import (
	"sync"
	"time"
)

// Number tracks a numberBucket over a bounded number of
// time buckets. Currently the buckets are one second long and only the last 10 seconds are kept.
//统计控制器的统计字段的底层对象, 是一个N个小格的窗口, 用于平滑统计
type Number struct {
	//key-秒级时间戳, value-统计
	Buckets map[int64]*numberBucket
	Mutex   *sync.RWMutex
}

type numberBucket struct {
	Value float64
}

// NewNumber initializes a RollingNumber struct.
func NewNumber() *Number {
	r := &Number{
		Buckets: make(map[int64]*numberBucket),
		Mutex:   &sync.RWMutex{},
	}
	return r
}

//getCurrentBucket 获取当前窗口
func (r *Number) getCurrentBucket() *numberBucket {
	now := time.Now().Unix()
	var bucket *numberBucket
	var ok bool

	if bucket, ok = r.Buckets[now]; !ok {
		bucket = &numberBucket{}
		r.Buckets[now] = bucket
	}

	return bucket
}

//removeOldBuckets 删除10秒以外的窗口
func (r *Number) removeOldBuckets() {
	now := time.Now().Unix() - 10

	for timestamp := range r.Buckets {
		// TODO: configurable rolling window
		if timestamp <= now {
			delete(r.Buckets, timestamp)
		}
	}
}

// Increment increments the number in current timeBucket.
//Increment 增加当前窗口的计数
func (r *Number) Increment(i float64) {
	if i == 0 {
		return
	}

	r.Mutex.Lock()
	defer r.Mutex.Unlock()

	//获取当前窗口
	b := r.getCurrentBucket()
	b.Value += i
	//移动, 删掉10秒以外的窗口
	r.removeOldBuckets()
}

// UpdateMax updates the maximum value in the current bucket.
//UpdateMax 更新窗口最大值
func (r *Number) UpdateMax(n float64) {
	r.Mutex.Lock()
	defer r.Mutex.Unlock()

	b := r.getCurrentBucket()
	if n > b.Value {
		b.Value = n
	}
	r.removeOldBuckets()
}

// Sum sums the values over the buckets in the last 10 seconds.
//Sum 对最近10秒内存储桶上的值进行求和
func (r *Number) Sum(now time.Time) float64 {
	sum := float64(0)

	r.Mutex.RLock()
	defer r.Mutex.RUnlock()

	for timestamp, bucket := range r.Buckets {
		// TODO: configurable rolling window
		if timestamp >= now.Unix()-10 {
			sum += bucket.Value
		}
	}

	return sum
}

// Max returns the maximum value seen in the last 10 seconds.
//Max 返回过去10秒内看到的最大值
func (r *Number) Max(now time.Time) float64 {
	var max float64

	r.Mutex.RLock()
	defer r.Mutex.RUnlock()

	for timestamp, bucket := range r.Buckets {
		// TODO: configurable rolling window
		if timestamp >= now.Unix()-10 {
			if bucket.Value > max {
				max = bucket.Value
			}
		}
	}

	return max
}

//Avg 过去10秒的平均计数
func (r *Number) Avg(now time.Time) float64 {
	return r.Sum(now) / 10
}
