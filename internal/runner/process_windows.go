package runner

import (
	"context"
	"os/exec"
	"strconv"
	"time"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}
