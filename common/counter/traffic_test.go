package counter

import (
	"io"
	"net"
	"runtime"
	"sync"
	"testing"
)

func TestConnectionCountsEveryReadAndWrite(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	s := &TrafficStorage{}
	c := NewConnCounter(a, s)
	done := make(chan error, 1)
	go func() {
		_, err := b.Write([]byte("abc"))
		if err == nil {
			_, err = b.Write([]byte("defgh"))
		}
		if err == nil {
			_, err = io.ReadFull(b, make([]byte, 8))
		}
		done <- err
	}()
	for _, n := range []int{3, 5} {
		if _, err := io.ReadFull(c, make([]byte, n)); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"12", "345678"} {
		if _, err := c.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	up, down := s.Read(true)
	if up != 8 || down != 8 {
		t.Fatalf("got %d/%d, want 8/8", up, down)
	}
}

func TestConcurrentCreationAndDrainConservesBytes(t *testing.T) {
	var counters sync.Map
	const writers, iterations = 32, 10000
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c := LoadOrCreate(&counters, "node")
			for j := 0; j < iterations; j++ {
				c.Tx("user", 1)
				c.Rx("user", 2)
			}
		}()
	}
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	var up, down int64
	s := LoadOrCreate(&counters, "node").GetCounter("user")
	for {
		u, d := s.Read(true)
		up += u
		down += d
		select {
		case <-done:
			u, d = s.Read(true)
			up += u
			down += d
			if up != writers*iterations || down != 2*writers*iterations {
				t.Fatalf("lost bytes: got %d/%d", up, down)
			}
			return
		default:
			runtime.Gosched()
		}
	}
}
