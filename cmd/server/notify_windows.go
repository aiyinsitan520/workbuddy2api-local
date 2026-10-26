//go:build windows

package main

import (
	"encoding/xml"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// notifyWindows 用 Windows 通知中心弹一条 toast（不依赖第三方库）。
//
// 为什么走 PowerShell 而不是 Go 原生 WinRT：Go 调用 WinRT 需要 cgo 或
// go-ole 之类的依赖，而本部署刻意保持 CGO_ENABLED=0 的单二进制分发；
// 托盘脚本已经在跑 PowerShell，复用它零额外依赖。
//
// 失败只打日志、绝不 panic：提醒是附加能力，不能反过来影响凭据刷新。
func notifyWindows(title, body string) {
	ps, err := powershellPath()
	if err != nil {
		log.Printf("notify: toast skipped: %v", err)
		return
	}
	script := buildToastScript(title, body)
	cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", script)
	// CREATE_NO_WINDOW：避免每次提醒闪一个黑框。
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("notify: toast failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
}

// powershellPath 解析 powershell.exe 的绝对路径。
//
// 为什么不能直接 exec.Command("powershell.exe")：网关由计划任务/VBS 拉起时
// PATH 可能被裁剪（实测出现过 "executable file not found in %PATH%"），
// 而提醒恰好是最不能静默失败的功能。先查 PATH，再回落 System32 的固定安装位置。
func powershellPath() (string, error) {
	if p, err := exec.LookPath("powershell.exe"); err == nil {
		return p, nil
	}
	if root := os.Getenv("SystemRoot"); root != "" {
		p := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errPowerShellNotFound
}

var errPowerShellNotFound = errors.New("powershell.exe 不在 PATH，也不在 %SystemRoot%\\System32\\WindowsPowerShell\\v1.0")

// buildToastScript 生成弹 toast 的 PowerShell 脚本。
//
// 用 XML 转义而不是字符串拼接：账号昵称是用户/卖家填的任意文本（本次池里就有
// "雪海13004655955" 这类），含 & < > 时直接拼进 XML 会让整条通知静默失败。
func buildToastScript(title, body string) string {
	esc := func(s string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	// 通知正文里换行用 &#10;：XML 元素内容里的裸换行会被折叠成空格。
	escapedBody := strings.ReplaceAll(esc(body), "\n", "&#10;")
	return `
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] > $null
$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml('<toast><visual><binding template="ToastGeneric"><text>` + esc(title) + `</text><text>` + escapedBody + `</text></binding></visual></toast>')
$toast = New-Object Windows.UI.Notifications.ToastNotification $xml
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('WorkBuddy2API').Show($toast)
`
}
