package nsqd

import (
	"bytes"
	"sync"
)

/**
对象池
为了避免频繁的创建和销毁对象, 节省cpu资源
 */
var bp sync.Pool

func init() {
	bp.New = func() interface{} {
		return &bytes.Buffer{}
	}
}

func bufferPoolGet() *bytes.Buffer {
	return bp.Get().(*bytes.Buffer)
}

func bufferPoolPut(b *bytes.Buffer) {
	bp.Put(b)
}
