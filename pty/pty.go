// Package pty runs commands on pseudo-terminals.
package pty

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// Start runs cmd on a new PTY of the given size, as the leader of a new
// session with the PTY as its controlling terminal, and returns the PTY's
// master side. It sets cmd's Stdin, Stdout and Stderr to the PTY, replacing
// any already set. On Linux the master is registered with the runtime
// poller, so Close interrupts a pending Read or Write; see Pollable.
func Start(cmd *exec.Cmd, cols, rows uint16) (*os.File, error) {
	master, slave, err := open()
	if err != nil {
		return nil, err
	}
	defer slave.Close()
	if err := SetSize(master, cols, rows); err != nil {
		master.Close()
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true // Ctty 0: the child's stdin
	if err := cmd.Start(); err != nil {
		master.Close()
		return nil, err
	}
	return master, nil
}

// SetSize sets the window size of the PTY whose master is f.
func SetSize(f *os.File, cols, rows uint16) error {
	return control(f, func(fd int) error {
		return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols})
	})
}

// control runs fn on f's descriptor. Unlike File.Fd, it leaves a pollable f
// pollable.
func control(f *os.File, fn func(fd int) error) error {
	raw, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var fnErr error
	if err := raw.Control(func(fd uintptr) { fnErr = fn(int(fd)) }); err != nil {
		return err
	}
	return fnErr
}
