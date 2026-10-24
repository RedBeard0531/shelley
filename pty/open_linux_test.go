package pty

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

// grow uses n frames of stack and returns, leaving the stack grown for the
// garbage collector to shrink.
//
//go:noinline
func grow(n int) int {
	if n == 0 {
		return 0
	}
	return grow(n-1) + 1
}

func openByNumberPair() (master, slave *os.File, err error) {
	if master, err = openMaster(); err != nil {
		return nil, nil, err
	}
	if slave, err = openByNumber(master); err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}

// Stacks that move while a PTY is being opened, as the garbage collector
// shrinks them, must not make open pair the master with some other slave.
func TestOpenPairsUnderStackMoves(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				runtime.GC()
			}
		}
	}()
	for _, tc := range []struct {
		name string
		open func() (master, slave *os.File, err error)
	}{
		{"open", open},
		{"by number", openByNumberPair},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					for i := range 200 {
						if i%10 == 0 {
							grow(1000)
						}
						if err := checkPair(tc.open); err != nil {
							t.Error(err)
							return
						}
					}
				})
			}
			wg.Wait()
		})
	}
}

func checkPair(open func() (master, slave *os.File, err error)) error {
	master, slave, err := open()
	if err != nil {
		return err
	}
	defer master.Close()
	defer slave.Close()
	var n uint32
	if err := control(master, func(fd int) (err error) {
		n, err = unix.IoctlGetUint32(fd, unix.TIOCGPTN)
		return err
	}); err != nil {
		return err
	}
	got, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", slave.Fd()))
	if err != nil {
		return err
	}
	if want := fmt.Sprintf("/dev/pts/%d", n); got != want {
		return fmt.Errorf("master %s came with slave %s", want, got)
	}
	return nil
}

func TestCheckPeerRejectsAnotherSlave(t *testing.T) {
	master, slave, err := open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	otherMaster, otherSlave, err := open()
	if err != nil {
		t.Fatal(err)
	}
	defer otherMaster.Close()
	defer otherSlave.Close()
	if err := checkPeer(master, otherSlave); err == nil {
		t.Error("checkPeer accepted another master's slave")
	}
	if err := checkPeer(master, slave); err != nil {
		t.Errorf("checkPeer rejected the master's own slave: %v", err)
	}
}
