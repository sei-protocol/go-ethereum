package timer

import (
	"fmt"
	"time"
)

type Timer struct {
	name  string
	start time.Time
}

func NewTimer(name string) *Timer {
	return &Timer{
		name:  name,
		start: time.Now(),
	}
}

func (t *Timer) Stop() {
	fmt.Printf("go-ethereum: [Timer] %s took %s\n", t.name, time.Since(t.start).String())
}
