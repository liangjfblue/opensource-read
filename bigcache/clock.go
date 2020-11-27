package bigcache

import "time"

type clock interface {
	Epoch() int64
}

type systemClock struct {
}

//Epoch 获取时间戳
func (c systemClock) Epoch() int64 {
	return time.Now().Unix()
}
