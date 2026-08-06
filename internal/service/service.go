package service

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

type Controller struct {
	Unit    string
	Timeout time.Duration
}

func (c Controller) Action(ctx context.Context, action string) error {
	switch action {
	case "start", "stop", "restart":
	default:
		return errors.New("不支持的服务操作")
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", action, c.Unit).CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("服务操作超时")
	}
	if err != nil {
		message := strings.TrimSpace(string(out))
		if len(message) > 300 {
			message = message[:300]
		}
		if message == "" {
			message = "systemctl 执行失败"
		}
		return errors.New(message)
	}
	return nil
}
