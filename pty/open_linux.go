package pty

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// open allocates a PTY. It replaces github.com/creack/pty's Open, which
// passes ioctl the addresses of stack variables as uintptrs through
// ordinary calls. When the goroutine's stack moves in between, as it may
// whenever it grows or the garbage collector shrinks it, the kernel writes
// the PTY number into the old stack, and Open goes on to open /dev/pts/0,
// whoever's terminal that is. Here pointers reach ioctl only through x/sys.
func open() (master, slave *os.File, err error) {
	if master, err = openMaster(); err != nil {
		return nil, nil, err
	}
	slave, peerErr := openPeer(master)
	if peerErr != nil {
		var numErr error
		if slave, numErr = openByNumber(master); numErr != nil {
			master.Close()
			return nil, nil, fmt.Errorf("open pty slave: %w; by number: %w", peerErr, numErr)
		}
	}
	return master, slave, nil
}

// openMaster allocates a PTY and unlocks its slave. os.OpenFile registers
// the master with the runtime poller.
func openMaster() (*os.File, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	if err := control(master, func(fd int) error { return unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0) }); err != nil {
		master.Close()
		return nil, fmt.Errorf("unlock pty: %w", err)
	}
	return master, nil
}

// openPeer opens master's slave with TIOCGPTPEER, through no /dev/pts path.
func openPeer(master *os.File) (*os.File, error) {
	var slave *os.File
	err := control(master, func(fd int) error {
		r, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCGPTPEER, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC)
		if errno != 0 {
			return fmt.Errorf("TIOCGPTPEER: %w", errno)
		}
		slave = os.NewFile(r, "/dev/pts")
		return nil
	})
	return slave, err
}

// openByNumber opens master's slave by its /dev/pts path, for systems that
// refuse TIOCGPTPEER: kernels before 4.13, and sandboxes that emulate or
// filter ioctls. That /dev/pts need not be the master's devpts, as in some
// containers, so it checks the slave it got is the master's.
func openByNumber(master *os.File) (*os.File, error) {
	var n uint32
	if err := control(master, func(fd int) (err error) {
		n, err = unix.IoctlGetUint32(fd, unix.TIOCGPTN)
		return err
	}); err != nil {
		return nil, fmt.Errorf("TIOCGPTN: %w", err)
	}
	path := "/dev/pts/" + strconv.FormatUint(uint64(n), 10)
	// Not os.OpenFile, which would make the command's terminal non-blocking.
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	slave := os.NewFile(uintptr(fd), path)
	if err := checkPeer(master, slave); err != nil {
		slave.Close()
		return nil, err
	}
	return slave, nil
}

// checkPeer sets a random window size on master and fails unless slave
// reports it. The size is random so that another PTY being checked at the
// same time can't pass for master's. Start sets the real size afterwards.
func checkPeer(master, slave *os.File) error {
	r := rand.Uint64() | 1 // a new PTY's size is all zeros
	want := unix.Winsize{Row: uint16(r), Col: uint16(r >> 16), Xpixel: uint16(r >> 32), Ypixel: uint16(r >> 48)}
	if err := control(master, func(fd int) error { return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &want) }); err != nil {
		return err
	}
	var got *unix.Winsize
	if err := control(slave, func(fd int) (err error) {
		got, err = unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
		return err
	}); err != nil {
		return err
	}
	if *got != want {
		return fmt.Errorf("%s belongs to another pty master", slave.Name())
	}
	return nil
}

// Pollable returns master, a master from Start, registered with the runtime
// poller so that Close interrupts a pending Read or Write. On Linux, Start's
// masters already are.
func Pollable(master *os.File) (*os.File, error) { return master, nil }
