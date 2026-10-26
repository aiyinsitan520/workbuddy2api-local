//go:build !windows

package main

import "log"

// notifyWindows 非 Windows 平台只写日志：本项目的部署目标是 Windows 桌面，
// 其他平台保留可编译性（不引平台 API）。
func notifyWindows(title, body string) {
	log.Printf("notify: %s — %s", title, body)
}
