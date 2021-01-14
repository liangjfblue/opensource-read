package gonce

import (
	"fmt"
	"testing"
)

func TestGOnce_IsDone(t *testing.T) {
	o := new(GOnce)

	fmt.Println("done? ", o.IsDone())

	o.Do(func() {
		fmt.Println("do something")
	})

	fmt.Println("done? ", o.IsDone())
}
