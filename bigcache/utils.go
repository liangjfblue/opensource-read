package bigcache

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

//convertMBToBytes mb转字节
func convertMBToBytes(value int) int {
	return value * 1024 * 1024
}

//isPowerOfTwo 至少为2
func isPowerOfTwo(number int) bool {
	return (number & (number - 1)) == 0
}
