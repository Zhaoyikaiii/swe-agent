package runtime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"

	"github.com/local/swe-agent/internal/core"
)

type Local struct {
	BaseEnv map[string]string
}

func NewLocal(env map[string]string) *Local {
	return &Local{BaseEnv: env}
}

func (l *Local) Execute(ctx context.Context, req core.ExecRequest) (core.ExecResult, error) {
	if req.Command == "" {
		return core.ExecResult{Code: 0}, nil
	}
	runCtx := ctx
	cancel := func() {}
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	defer cancel()

	cmd := exec.Command(shellName(), shellArg(), req.Command)
	cmd.Dir = req.Cwd
	cmd.Env = mergeEnv(os.Environ(), l.BaseEnv, req.Env)
	if runtime.GOOS != "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Start()
	if err != nil {
		return core.ExecResult{Code: -1, Stderr: err.Error()}, err
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	timedOut := false
	select {
	case err = <-done:
	case <-runCtx.Done():
		timedOut = errors.Is(runCtx.Err(), context.DeadlineExceeded)
		terminateProcess(cmd)
		err = <-done
	}

	code := 0
	if err != nil {
		code = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
	}
	return core.ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Code:     code,
		TimedOut: timedOut,
	}, nil
}

func terminateProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if runtime.GOOS != "windows" {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
			return
		}
	}
	_ = cmd.Process.Kill()
}

func (l *Local) TemplateVars(ctx context.Context) map[string]string {
	return map[string]string{
		"os":   runtime.GOOS,
		"arch": runtime.GOARCH,
	}
}

func (l *Local) Close(ctx context.Context) error {
	return nil
}

func shellName() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "/bin/sh"
}

func shellArg() string {
	if runtime.GOOS == "windows" {
		return "/C"
	}
	return "-c"
}

func mergeEnv(base []string, maps ...map[string]string) []string {
	env := map[string]string{}
	for _, kv := range base {
		for i, r := range kv {
			if r == '=' {
				env[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	for _, m := range maps {
		for k, v := range m {
			env[k] = v
		}
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
