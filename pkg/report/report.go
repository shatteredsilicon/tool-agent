package report

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

type ReportSource struct {
	Name       string   `ini:"-" json:"name"`
	Executable string   `ini:"executable" json:"executable"`
	Stdin      []string `ini:"stdin" json:"stdin"`
}

type report struct {
	nodeName  string
	interval  time.Duration
	timeout   time.Duration
	sources   []ReportSource
	logger    *slog.Logger
	serverURL *url.URL
	api       *api
}

func NewReport(
	nodeName string,
	logger *slog.Logger,
	interval time.Duration,
	timeout time.Duration,
	sources []ReportSource,
	serverURL *url.URL,
) *report {
	return &report{
		nodeName: nodeName,
		interval: interval,
		timeout:  timeout,
		sources:  sources,
		logger:   logger,
		api:      newAPI(serverURL),
	}
}

func (r *report) Start(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	for {
		select {
		case <-ticker.C:
			for _, rs := range r.sources {
				now := time.Now()
				ctx, _ := context.WithTimeout(context.Background(), r.timeout)

				var output []byte
				var err error
				cmd := exec.CommandContext(ctx, "/bin/sh", "-c", rs.Executable)
				if len(rs.Stdin) > 0 {
					// run the command in a pseudo-terminal environment as some tools
					// might require terminal environment for input
					if output, err = r.terminalRun(ctx, cmd, rs); err != nil {
						r.logger.Error(fmt.Sprintf("failed to generate '%s' report: %s", rs.Name, err.Error()))
						continue
					}
				} else {
					if output, err = cmd.Output(); err != nil {
						r.logger.Error(fmt.Sprintf("failed to generate '%s' report: %s", rs.Name, err.Error()))
						continue
					}
				}

				if err := r.api.postReport(r.nodeName, Report{Ts: now, Type: rs.Name, Payload: output}); err != nil {
					r.logger.Error(fmt.Sprintf("failed to post '%s' report to the server: %s", rs.Name, err.Error()))
					continue
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// terminalRun set up a pseudo-terminal environment to run a command
func (r *report) terminalRun(ctx context.Context, cmd *exec.Cmd, rs ReportSource) ([]byte, error) {
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s for '%s': %s", os.DevNull, rs.Name, err.Error())
	}
	defer devNull.Close()
	cmd.Stderr = devNull

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to set up a pseudo-terminal for '%s': %s", rs.Name, err.Error())
	}
	defer ptmx.Close()

	if err := turnOffEcho(ptmx); err != nil {
		return nil, fmt.Errorf("failed to turn off terminal echo for '%s': %s", rs.Name, err.Error())
	}

	stdinR, stdinW := io.Pipe()
	defer stdinR.Close()

	go io.Copy(ptmx, stdinR)

	time.Sleep(time.Second) // wait a little bit so that process can be ready for the input

	go func() {
		defer stdinW.Close()

		for _, str := range rs.Stdin {
			select {
			case <-ctx.Done():
				return
			default:
				io.WriteString(stdinW, str+"\n")
				time.Sleep(time.Second)
			}
		}
	}()

	var outputBuf bytes.Buffer
	readDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(&outputBuf, ptmx)
		readDone <- err
	}()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("timed out waiting for the output of '%s'", rs.Name)
	case err := <-readDone:
		if err != nil && !errors.Is(err, syscall.EIO) {
			return nil, fmt.Errorf("failed to capture output for '%s': %s", rs.Name, err.Error())
		}
		return outputBuf.Bytes(), nil
	}
}

func turnOffEcho(f *os.File) error {
	fd := int(f.Fd())
	const ioctlReadTermios = unix.TCGETS
	const ioctlWriteTermios = unix.TCSETS

	termios, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		return err
	}

	newState := *termios
	newState.Lflag &^= unix.ECHO
	newState.Lflag |= unix.ICANON | unix.ISIG
	newState.Iflag |= unix.ICRNL

	return unix.IoctlSetTermios(fd, ioctlWriteTermios, &newState)
}
