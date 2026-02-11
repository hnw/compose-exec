package compose

import (
	"errors"
	"io"
	"sync"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/pkg/stdcopy"
)

// StdoutPipe returns a pipe that will be connected to the command's standard output.
//
// It is an error to call StdoutPipe after the command has started or when Stdout is already set.
func (c *Cmd) StdoutPipe() (io.ReadCloser, error) {
	if c.isStarted() {
		return nil, errors.New("compose: already started")
	}
	if c.Stdout != nil {
		return nil, errors.New("compose: Stdout already set")
	}
	pr, pw := io.Pipe()
	c.mu.Lock()
	c.Stdout = pw
	c.stdoutPipe = pw
	c.mu.Unlock()
	return pr, nil
}

// StderrPipe returns a pipe that will be connected to the command's standard error.
//
// It is an error to call StderrPipe after the command has started or when Stderr is already set.
func (c *Cmd) StderrPipe() (io.ReadCloser, error) {
	if c.isStarted() {
		return nil, errors.New("compose: already started")
	}
	if c.Stderr != nil {
		return nil, errors.New("compose: Stderr already set")
	}
	pr, pw := io.Pipe()
	c.mu.Lock()
	c.Stderr = pw
	c.stderrPipe = pw
	c.mu.Unlock()
	return pr, nil
}

// StdinPipe returns a pipe that will be connected to the command's standard input.
//
// It is an error to call StdinPipe after the command has started or when Stdin is already set.
func (c *Cmd) StdinPipe() (io.WriteCloser, error) {
	if c.isStarted() {
		return nil, errors.New("compose: already started")
	}
	if c.Stdin != nil {
		return nil, errors.New("compose: Stdin already set")
	}
	pr, pw := io.Pipe()
	c.mu.Lock()
	c.Stdin = pr
	c.stdinPipe = pr
	c.mu.Unlock()
	return pw, nil
}

func (c *Cmd) normalizedWriters() (io.Writer, io.Writer) {
	stdout := c.Stdout
	stderr := c.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if c.captureStderr {
		// Reset per run; only capture when explicitly enabled (Output/CombinedOutput).
		c.stderrBuf.Reset()
		stderr = io.MultiWriter(stderr, &c.stderrBuf)
	} else {
		// Avoid returning stale stderr from previous runs.
		c.stderrBuf.Reset()
	}
	return stdout, stderr
}

func (c *Cmd) closeStdoutPipe(err error) {
	c.mu.Lock()
	stdoutPipe := c.stdoutPipe
	c.stdoutPipe = nil
	c.mu.Unlock()

	if stdoutPipe != nil {
		if err != nil {
			_ = stdoutPipe.CloseWithError(err)
		} else {
			_ = stdoutPipe.Close()
		}
	}
}

func (c *Cmd) closeStderrPipe(err error) {
	c.mu.Lock()
	stderrPipe := c.stderrPipe
	c.stderrPipe = nil
	c.mu.Unlock()

	if stderrPipe != nil {
		if err != nil {
			_ = stderrPipe.CloseWithError(err)
		} else {
			_ = stderrPipe.Close()
		}
	}
}

func (c *Cmd) closeStdPipes(err error) {
	c.closeStdoutPipe(err)
	c.closeStderrPipe(err)
}

func (c *Cmd) closeStdinPipe(err error) {
	c.mu.Lock()
	stdinPipe := c.stdinPipe
	c.stdinPipe = nil
	c.mu.Unlock()
	if stdinPipe == nil {
		return
	}
	if err != nil {
		_ = stdinPipe.CloseWithError(err)
	} else {
		_ = stdinPipe.Close()
	}
}

func (c *Cmd) closePipes(err error) {
	c.closeStdPipes(err)
	c.closeStdinPipe(err)
}

func (c *Cmd) startForwarding(
	attachResp dockertypes.HijackedResponse,
	stdout,
	stderr io.Writer,
) <-chan struct{} {
	ioDone := c.ioDone
	stdinDone := c.stdinDone
	ready := make(chan struct{})
	rawReader := attachResp.Reader
	var reader io.Reader
	if rawReader != nil {
		reader = &readSignalReader{
			r:     rawReader,
			ready: ready,
		}
	} else {
		close(ready)
	}

	go func() {
		if reader != nil {
			_, _ = stdcopy.StdCopy(stdout, stderr, reader)
		}
		c.closeStdPipes(nil)
		close(ioDone)
	}()

	go func() {
		defer close(stdinDone)
		if c.Stdin == nil {
			return
		}
		_, err := io.Copy(attachResp.Conn, c.Stdin)
		c.closeStdinPipe(err)
		_ = attachResp.CloseWrite()
	}()

	return ready
}

type readSignalReader struct {
	r     io.Reader
	ready chan struct{}
	once  sync.Once
}

func (r *readSignalReader) Read(p []byte) (int, error) {
	r.once.Do(func() {
		close(r.ready)
	})
	return r.r.Read(p)
}
