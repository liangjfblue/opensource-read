package bigcache

import (
	"encoding/binary"
)

/**
对没条数据的包装成一个条目(时间戳+hash值+key+数据)
通过缓冲区的复用, 减少切片的分配, 降低了gc压力
 */
const (
	timestampSizeInBytes = 8                                                       // Number of bytes used for timestamp
	hashSizeInBytes      = 8                                                       // Number of bytes used for hash
	keySizeInBytes       = 2                                                       // Number of bytes used for size of entry key
	headersSizeInBytes   = timestampSizeInBytes + hashSizeInBytes + keySizeInBytes // Number of bytes used for all headers
)

//wrapEntry 包装条目 (时间戳+hash值+key+数据)
func wrapEntry(timestamp uint64, hash uint64, key string, entry []byte, buffer *[]byte) []byte {
	keyLength := len(key)
	blobLength := len(entry) + headersSizeInBytes + keyLength

	if blobLength > len(*buffer) {
		*buffer = make([]byte, blobLength)
	}
	blob := *buffer

	binary.LittleEndian.PutUint64(blob, timestamp)
	binary.LittleEndian.PutUint64(blob[timestampSizeInBytes:], hash)
	binary.LittleEndian.PutUint16(blob[timestampSizeInBytes+hashSizeInBytes:], uint16(keyLength))
	copy(blob[headersSizeInBytes:], key)
	copy(blob[headersSizeInBytes+keyLength:], entry)

	//blob[:blobLength] 复用切片, 减少内存分配
	return blob[:blobLength]
}

//appendToWrappedEntry 追加条目
func appendToWrappedEntry(timestamp uint64, wrappedEntry []byte, entry []byte, buffer *[]byte) []byte {
	//已包装+新数据的大小
	blobLength := len(wrappedEntry) + len(entry)
	if blobLength > len(*buffer) {
		//超过缓存大小, 冲洗分配
		*buffer = make([]byte, blobLength)
	}

	blob := *buffer

	//时间戳
	binary.LittleEndian.PutUint64(blob, timestamp)
	//追加除时间戳其他的条目数据到新的缓冲区
	copy(blob[timestampSizeInBytes:], wrappedEntry[timestampSizeInBytes:])
	//追加新条目数据
	copy(blob[len(wrappedEntry):], entry)

	return blob[:blobLength]
}

//readEntry 从包装条目获取条目数据
func readEntry(data []byte) []byte {
	length := binary.LittleEndian.Uint16(data[timestampSizeInBytes+hashSizeInBytes:])

	// copy on read 拷贝
	dst := make([]byte, len(data)-int(headersSizeInBytes+length))
	copy(dst, data[headersSizeInBytes+length:])

	return dst
}

//readTimestampFromEntry 从包装条目读取时间戳
func readTimestampFromEntry(data []byte) uint64 {
	return binary.LittleEndian.Uint64(data)
}

//readKeyFromEntry 从包装条目读取key
func readKeyFromEntry(data []byte) string {
	length := binary.LittleEndian.Uint16(data[timestampSizeInBytes+hashSizeInBytes:])

	// copy on read
	dst := make([]byte, length)
	copy(dst, data[headersSizeInBytes:headersSizeInBytes+length])

	return bytesToString(dst)
}

//readKeyFromEntry 比较包装条目的key与给定key
func compareKeyFromEntry(data []byte, key string) bool {
	length := binary.LittleEndian.Uint16(data[timestampSizeInBytes+hashSizeInBytes:])

	return bytesToString(data[headersSizeInBytes:headersSizeInBytes+length]) == key
}

//readKeyFromEntry 从包装条目读取hash值
func readHashFromEntry(data []byte) uint64 {
	return binary.LittleEndian.Uint64(data[timestampSizeInBytes:])
}

//readKeyFromEntry 清空包装条目
func resetKeyFromEntry(data []byte) {
	binary.LittleEndian.PutUint64(data[timestampSizeInBytes:], 0)
}
