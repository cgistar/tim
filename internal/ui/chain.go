package ui

import (
	"context"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"tim/internal/core"
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
	// Form.AddTextArea 默认定位末尾；已有多行配置应从第一行展示。
	area := tview.NewTextArea().SetLabel("节点配置 / 分享链接").
		SetSize(12, 0).SetMaxLength(core.MaxChainInputSize).SetText(settings.Chain.Input, false)
	area.SetChangedFunc(func() { settings.Chain.Input = area.GetText() })
	form.AddFormItem(area)
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
	form.AddButton("清除", func() {
		form.GetFormItem(0).(*tview.Checkbox).SetChecked(false)
		area.SetText("", false)
		form.SetFocus(1)
		s.app.SetFocus(form)
	})
	form.SetCancelFunc(back)
	form.SetBorder(true).SetTitle(" 链式前置代理 · [Tab 切换] · [Esc 返回] ")
	area.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			back()
			return nil
		}
		return event
	})
	s.show(form)
}
