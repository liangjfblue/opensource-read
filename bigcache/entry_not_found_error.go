package bigcache

import "errors"

var (
	//错误定义
	// ErrEntryNotFound is an error type struct which is returned when entry was not found for provided key
	ErrEntryNotFound = errors.New("Entry not found")
)
