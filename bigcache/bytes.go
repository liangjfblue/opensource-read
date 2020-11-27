// +build !appengine

package bigcache

import (
	"reflect"
	"unsafe"
)

//bytesToString 字节转string 指针强制类型转换+反射, 避免copy开销
func bytesToString(b []byte) string {
	bytesHeader := (*reflect.SliceHeader)(unsafe.Pointer(&b))
	strHeader := reflect.StringHeader{Data: bytesHeader.Data, Len: bytesHeader.Len}
	return *(*string)(unsafe.Pointer(&strHeader))
}
