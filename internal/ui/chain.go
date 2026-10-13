package ui

import (
	"context"
	"strings"

	"clisimplehub/tui/internal/core"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func (s *screen) chain() {
	settings, err := s.manager.Store.Settings()
	if err != nil {
		s.failure(err)
		return
	}
	baseline := settings.Clone()
	form := tview.NewForm()
	form.AddCheckbox("启用链式前置", settings.Chain.Enabled, func(v bool) { settings.Chain.Enabled = v })
	form.AddTextArea("节点配置 / 分享链接", settings.Chain.Input, 0, 12, core.MaxChainInputSize, func(v string) { settings.Chain.Input = v })
	form.AddTextView("支持格式", "单个 Clash YAML 节点、vless://、vmess://、socks5://、ss://、trojan:// 等；也可粘贴 Base64。\n仅导入一个独立前置。应用时无效则提示未生效，不阻止有效订阅更新。", 0, 3, false, false)
	commit := func() {
		snapshot := settings.Clone()
		snapshot.Chain.Input = strings.TrimSpace(snapshot.Chain.Input)
		s.onSuccess = s.close
		s.keepDialog = true
		s.work("链式前置配置已保存", func(ctx context.Context) error {
			return s.manager.ApplySettings(ctx, snapshot)
		})
		s.keepDialog = false
	}
	back := func() {
		if settings.Equal(baseline) {
			s.close()
			return
		}
		s.confirmSettings(form, "chain-confirm", "链式前置配置有修改，是否应用？", commit)
	}
	form.AddButton("返回", back)
	form.SetCancelFunc(back)
	form.SetBorder(true).SetTitle(" 链式前置代理 · [Tab 切换] · [Esc 返回] ")
	form.GetFormItem(1).(*tview.TextArea).SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			back()
			return nil
		}
		return event
	})
	s.show(form)
}
