// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// launchPTY attaches a local Unix PTY as the child's stdin/stdout in raw mode.
// stderr stays a pipe so the two streams remain distinct on the bridge.
// Closing stdin on Stop does not close the master: that would also tear down
// the stdout pump before SIGTERM has a chance to flush.
func (pr *procRunner) launchPTY(cmd *exec.Cmd, waitDelay time.Duration) (Process, error) {
	master, slave, err := openLocalPTY()
	if err != nil {
		return nil, fmt.Errorf("sessions: local PTY: %w", err)
	}
	cmd.Stdin = slave
	cmd.Stdout = slave
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, fmt.Errorf("sessions: stderr pipe: %w", err)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0, // stdin is the slave, so the child's fd 0 is the tty
	}
	cmd.Cancel = func() error { return procGroupKill(cmd) }
	if err := cmd.Start(); err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, fmt.Errorf("sessions: start %q: %w", specProgram(cmd), err)
	}
	cmd.Env = nil
	_ = slave.Close()
	stdin := ptyWriter{f: master}
	return pr.watch(cmd, stdin, master, stderr, waitDelay), nil
}

func openLocalPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	slave, err = os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	if err := setRaw(int(slave.Fd())); err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, nil, err
	}
	_ = unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80})
	return master, slave, nil
}

func setRaw(fd int) error {
	tio, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	tio.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	tio.Oflag &^= unix.OPOST
	tio.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	tio.Cflag &^= unix.CSIZE | unix.PARENB
	tio.Cflag |= unix.CS8
	tio.Cc[unix.VMIN] = 1
	tio.Cc[unix.VTIME] = 0
	return unix.IoctlSetTermios(fd, unix.TCSETS, tio)
}

// ptyWriter writes to the PTY master. Close is a no-op so Stop can SIGTERM
// without cutting the stdout pump. The master is closed as stdout on the last
// teardown rung.
type ptyWriter struct{ f *os.File }

func (w ptyWriter) Write(p []byte) (int, error) { return w.f.Write(p) }
func (ptyWriter) Close() error                  { return nil }
func (w ptyWriter) SetWriteDeadline(t time.Time) error {
	return w.f.SetWriteDeadline(t)
}
