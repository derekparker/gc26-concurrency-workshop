package main

import (
	"fmt"
	"sync"
)

type incrementor struct {
	counter int
}

func (inc *incrementor) increment() {
	for range 100000 {
		inc.counter++
	}
}

func main() {
	var wg sync.WaitGroup
	inc := &incrementor{}

	wg.Go(inc.increment)
	wg.Go(inc.increment)

	wg.Wait()

	// Two goroutines, 100000 increments each. Should be 200000... right?
	fmt.Printf("Final counter: %d\n", inc.counter)
}
