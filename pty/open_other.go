//go:build !linux

package pty

import (
	"fmt"
	"os"

	creackpty "github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// open allocates a PTY with github.com/creack/pty, whose masters block. On
// macOS its Open hands TIOCPTYGNAME a stack buffer much as on Linux it
// hands TIOCGPTN one (see open_linux.go); replacing it waits for a Mac to
// test on.
func open() (master, slave *os.File, err error) { return creackpty.Open() }

// Pollable returns master, a master from Start, registered with the runtime
// poller so that Close interrupts a pending Read or Write. Here that means a
// non-blocking duplicate; Pollable closes master.
func Pollable(master *os.File) (*os.File, error) {
	defer master.Close()
	var fd int
	if err := control(master, func(old int) (err error) {
		fd, err = unix.FcntlInt(uintptr(old), unix.F_DUPFD_CLOEXEC, 0)
		return err
	}); err != nil {
		return nil, fmt.Errorf("dup pty: %w", err)
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("pty nonblock: %w", err)
	}
	return os.NewFile(uintptr(fd), master.Name()), nil
}
