package queue

import (
	"encoding/binary"
	"log"
	"time"
)

/**
用切片实现的双向队列
*/
const (
	// Number of bytes to encode 0 in uvarint format
	minimumHeaderSize = 17 // 1 byte blobsize + timestampSizeInBytes + hashSizeInBytes
	// Bytes before left margin are not used. Zero index means element does not exist in queue, useful while reading slice from index
	leftMarginIndex = 1 //索引0不用 用来方便判断队列是否空
)

var (
	errEmptyQueue       = &queueError{"Empty queue"}
	errInvalidIndex     = &queueError{"Index must be greater than zero. Invalid index."}
	errIndexOutOfBounds = &queueError{"Index out of range"}
)

// BytesQueue is a non-thread safe queue type of fifo based on bytes array.
// For every push operation index of entry is returned. It can be used to read the entry later
//基于字节数组的fifo的非线程安全队列类型
type BytesQueue struct {
	full         bool
	array        []byte
	capacity     int
	maxCapacity  int
	head         int
	tail         int
	count        int
	rightMargin  int
	headerBuffer []byte
	verbose      bool
}

type queueError struct {
	message string
}

// getUvarintSize returns the number of bytes to encode x in uvarint format
func getUvarintSize(x uint32) int {
	if x < 128 {
		return 1
	} else if x < 16384 {
		return 2
	} else if x < 2097152 {
		return 3
	} else if x < 268435456 {
		return 4
	} else {
		return 5
	}
}

// NewBytesQueue initialize new bytes queue.
// capacity is used in bytes array allocation
// When verbose flag is set then information about memory allocation are printed
func NewBytesQueue(capacity int, maxCapacity int, verbose bool) *BytesQueue {
	return &BytesQueue{
		array:        make([]byte, capacity),
		capacity:     capacity,
		maxCapacity:  maxCapacity,
		headerBuffer: make([]byte, binary.MaxVarintLen32),
		tail:         leftMarginIndex,
		head:         leftMarginIndex,
		rightMargin:  leftMarginIndex,
		verbose:      verbose,
	}
}

// Reset removes all entries from queue
func (q *BytesQueue) Reset() {
	// Just reset indexes
	q.tail = leftMarginIndex
	q.head = leftMarginIndex
	q.rightMargin = leftMarginIndex
	q.count = 0
	q.full = false
}

// Push copies entry at the end of queue and moves tail pointer. Allocates more space if needed.
// Returns index for pushed data or error if maximum size queue limit is reached.
//复制队列末尾的条目并移动尾指针。如果需要，分配更多的空间。 如果达到最大队列大小限制，返回推入数据的索引或错误
func (q *BytesQueue) Push(data []byte) (int, error) {
	dataLen := len(data)
	headerEntrySize := getUvarintSize(uint32(dataLen))

	//是否能插入队列尾部
	if !q.canInsertAfterTail(dataLen + headerEntrySize) {
		if q.canInsertBeforeHead(dataLen + headerEntrySize) {
			//插入尾部
			q.tail = leftMarginIndex
		} else if q.capacity+headerEntrySize+dataLen >= q.maxCapacity && q.maxCapacity > 0 {
			//队列超过限制
			return -1, &queueError{"Full queue. Maximum size limit reached."}
		} else {
			//队列分配更多内存
			q.allocateAdditionalMemory(dataLen + headerEntrySize)
		}
	}

	index := q.tail

	//插入队列
	q.push(data, dataLen)

	return index, nil
}

//allocateAdditionalMemory 分配内存
func (q *BytesQueue) allocateAdditionalMemory(minimum int) {
	start := time.Now()
	//有最小限制
	if q.capacity < minimum {
		q.capacity += minimum
	}
	//2倍增长 大于某个阈值有最大限制
	q.capacity = q.capacity * 2
	if q.capacity > q.maxCapacity && q.maxCapacity > 0 {
		q.capacity = q.maxCapacity
	}

	oldArray := q.array
	q.array = make([]byte, q.capacity)

	//复制旧数据到新的队列(切片间复制)
	if leftMarginIndex != q.rightMargin {
		copy(q.array, oldArray[:q.rightMargin])

		if q.tail <= q.head {
			if q.tail != q.head {
				headerEntrySize := getUvarintSize(uint32(q.head - q.tail))
				emptyBlobLen := q.head - q.tail - headerEntrySize
				q.push(make([]byte, emptyBlobLen), emptyBlobLen)
			}

			q.head = leftMarginIndex
			q.tail = q.rightMargin
		}
	}

	q.full = false

	if q.verbose {
		log.Printf("Allocated new queue in %s; Capacity: %d \n", time.Since(start), q.capacity)
	}
}

//push 插入队列
func (q *BytesQueue) push(data []byte, len int) {
	headerEntrySize := binary.PutUvarint(q.headerBuffer, uint64(len))
	q.copy(q.headerBuffer, headerEntrySize)

	q.copy(data, len)

	if q.tail > q.head {
		q.rightMargin = q.tail
	}
	if q.tail == q.head {
		q.full = true
	}

	q.count++
}

//copy 从队列尾部插入
func (q *BytesQueue) copy(data []byte, len int) {
	q.tail += copy(q.array[q.tail:], data[:len])
}

// Pop reads the oldest entry from queue and moves head pointer to the next one
//Pop 弹出队头条目
func (q *BytesQueue) Pop() ([]byte, error) {
	data, headerEntrySize, err := q.peek(q.head)
	if err != nil {
		return nil, err
	}
	size := len(data)

	q.head += headerEntrySize + size
	q.count--

	if q.head == q.rightMargin {
		q.head = leftMarginIndex
		if q.tail == q.rightMargin {
			q.tail = leftMarginIndex
		}
		q.rightMargin = q.tail
	}

	q.full = false

	return data, nil
}

// Peek reads the oldest entry from list without moving head pointer
func (q *BytesQueue) Peek() ([]byte, error) {
	data, _, err := q.peek(q.head)
	return data, err
}

// Get reads entry from index
func (q *BytesQueue) Get(index int) ([]byte, error) {
	//根据索引 从队列中获取数据
	data, _, err := q.peek(index)
	return data, err
}

// CheckGet checks if an entry can be read from index
func (q *BytesQueue) CheckGet(index int) error {
	return q.peekCheckErr(index)
}

// Capacity returns number of allocated bytes for queue
func (q *BytesQueue) Capacity() int {
	return q.capacity
}

// Len returns number of entries kept in queue
func (q *BytesQueue) Len() int {
	return q.count
}

// Error returns error message
func (e *queueError) Error() string {
	return e.message
}

// peekCheckErr is identical to peek, but does not actually return any data
func (q *BytesQueue) peekCheckErr(index int) error {

	if q.count == 0 {
		return errEmptyQueue
	}

	if index <= 0 {
		return errInvalidIndex
	}

	if index >= len(q.array) {
		return errIndexOutOfBounds
	}
	return nil
}

// peek returns the data from index and the number of bytes to encode the length of the data in uvarint format
func (q *BytesQueue) peek(index int) ([]byte, int, error) {
	//检查索引范围
	err := q.peekCheckErr(index)
	if err != nil {
		return nil, 0, err
	}

	//Uvarint解码
	blockSize, n := binary.Uvarint(q.array[index:])
	//根据范围 获取数据
	return q.array[index+n : index+n+int(blockSize)], n, nil
}

// canInsertAfterTail returns true if it's possible to insert an entry of size of need after the tail of the queue
//如果可以在队列尾部插入所需大小的条目，则返回true
func (q *BytesQueue) canInsertAfterTail(need int) bool {
	if q.full {
		return false
	}
	if q.tail >= q.head {
		return q.capacity-q.tail >= need
	}
	// 1. there is exactly need bytes between head and tail, so we do not need
	// to reserve extra space for a potential empty entry when realloc this queue
	// 2. still have unused space between tail and head, then we must reserve
	// at least headerEntrySize bytes so we can put an empty entry
	//1.在头和尾之间确实需要字节，所以我们不需要在realloc, 此队列时为潜在的空条目预留额外空间
	//2.尾部和头部之间还有未使用的空间，那么我们必须保留, 至少headerEntrySize字节，这样我们可以放一个空条目
	return q.head-q.tail == need || q.head-q.tail >= need+minimumHeaderSize
}

// canInsertBeforeHead returns true if it's possible to insert an entry of size of need before the head of the queue
//如果有可能在队列头之前插入所需大小的条目，则返回true
func (q *BytesQueue) canInsertBeforeHead(need int) bool {
	//满了?
	if q.full {
		return false
	}
	//0---A--head----B---tail---C----
	if q.tail >= q.head {
		//A是否满足所需
		return q.head-leftMarginIndex == need || q.head-leftMarginIndex >= need+minimumHeaderSize
	}
	//0---A--tail----B---head---C----
	//B区域是否满足所需/ 是否足够放得下一个空条目17字节大小
	return q.head-q.tail == need || q.head-q.tail >= need+minimumHeaderSize
}
