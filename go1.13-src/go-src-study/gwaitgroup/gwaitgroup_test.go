package gwaitgroup

import (
	"fmt"
	"testing"
	"time"
)

// TestGWaitGroup_waiterCount 测试获取counter
func TestGWaitGroup_waiterCount(t *testing.T) {
	var waiter, counter uint32

	wg := new(GWaitGroup)
	wg.wg.Add(5)
	for i := 0; i < 5; i++ {
		go func(i int, wg *GWaitGroup) {
			defer wg.wg.Done()
			// waiter:0, counter: 5
			// waiter:0, counter: 4
			// waiter:0, counter: 3
			// waiter:0, counter: 2
			// waiter:0, counter: 1
			waiter, counter = wg.waiterCount()
			fmt.Printf("waiter:%d, counter: %d\n", waiter, counter)
		}(i, wg)
		time.Sleep(time.Second)
	}
	wg.wg.Wait()
}

// TestGWaitGroup_waiterCount2 测试waiter变化
func TestGWaitGroup_waiterCount2(t *testing.T) {
	var waiter, counter uint32

	wg := new(GWaitGroup)
	wg.wg.Add(5)

	// waiter:0, counter: 5
	waiter, counter = wg.waiterCount()
	fmt.Printf("waiter:%d, counter: %d\n", waiter, counter)

	for i := 0; i < 5; i++ {
		go func(i int, wg *GWaitGroup) {
			defer wg.wg.Done()
			time.Sleep(time.Second)
			// waiter:1, counter: 5
			// waiter:1, counter: 5
			// waiter:1, counter: 5
			// waiter:1, counter: 5
			// waiter:1, counter: 5
			waiter, counter = wg.waiterCount()
			fmt.Printf("waiter:%d, counter: %d\n", waiter, counter)
		}(i, wg)

	}
	wg.wg.Wait()
}