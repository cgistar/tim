package ui

import (
	"context"
	"fmt"

	"github.com/rivo/tview"
)

func (s *screen) networkGuard() {
	st := s.last.Guard
	text := "断网保护未开启。\n开启后阻止普通应用绕过 TUN 访问公网；允许 tim、mihomo 出站和常见私网。普通程序直连 DNS 53/853 被阻止。\n停止内核、关闭 TUN、退出均不解除保护。规则是否跨重启保留由系统防火墙保存/加载机制决定；tim 不自动恢复，规则不存在时显示关闭。"
	buttons := []string{"开启断网保护", "取消"}
	if st.Enabled {
		text = fmt.Sprintf("断网保护已开启（%s）。", st.Backend)
		if !s.last.Running || !s.last.Tun {
			text += "\n保护已开启，TUN 未就绪。"
		}
		text += "\n关闭后普通应用可以直接联网；网页认证后需手动重新开启。"
		buttons = []string{"关闭保护并恢复直连", "取消"}
	}
	if st.Error != "" {
		text += "\n保护状态异常：" + st.Error
		if st.Enabled {
			buttons = []string{"重试开启断网保护", "关闭保护并恢复直连", "取消"}
		}
	}
	s.show(tview.NewModal().SetText(text).AddButtons(buttons).SetDoneFunc(func(_ int, label string) {
		s.close()
		switch label {
		case "开启断网保护", "重试开启断网保护":
			s.workResult(func(ctx context.Context) (string, error) {
				if err := s.manager.EnableNetworkGuard(ctx); err != nil {
					return "", err
				}
				st, err := s.manager.Status(ctx)
				if err != nil {
					return "断网保护已开启", err
				}
				if !st.Running || !st.Tun {
					return "保护已开启，TUN 未就绪；普通应用公网直连已阻止", nil
				}
				return "断网保护已开启", nil
			})
		case "关闭保护并恢复直连":
			s.work("断网保护已关闭，已恢复直连", s.manager.DisableNetworkGuard)
		}
	}))
}
