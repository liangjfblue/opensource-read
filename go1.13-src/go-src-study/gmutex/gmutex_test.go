package main

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

func TestGMutex_TryLock(t *testing.T) {
	rand.Seed(time.Now().Unix())

	m := new(GMutex)

	// 随机sleep 0~5
	go func() {
		m.Lock()
		defer m.Unlock()

		randTime := rand.Int31n(10)
		fmt.Println("rand sleep time: ", randTime)
		time.Sleep(time.Second * time.Duration(randTime))
	}()

	time.Sleep(time.Second * 2)

	if ok := m.TryLock(); ok {
		defer m.Unlock()
		fmt.Println("get lock")
	} else {
		fmt.Println("can not get lock")
	}
}

func TestGMutex_IsLock(t *testing.T) {
	m := new(GMutex)
	m.Lock()
	fmt.Println(m.IsLock())
	m.Unlock()
	fmt.Println(m.IsLock())
}