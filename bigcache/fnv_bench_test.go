package bigcache

import "testing"

var text = "abcdefg"

//自定义的比标准库的 快了6倍.....
func BenchmarkFnvHashSum64(b *testing.B) {
	h := newDefaultHasher()
	for i := 0; i < b.N; i++ {
		h.Sum64(text)
	}
}

func BenchmarkFnvHashStdLibSum64(b *testing.B) {
	for i := 0; i < b.N; i++ {
		stdLibFnvSum64(text)
	}
}
