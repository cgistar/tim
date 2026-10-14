package ui

import (
	"context"
	"fmt"
	"strings"

	"tim/internal/core"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func (s *screen) showManagedForm(form *tview.Form, settings core.Settings) {
	user, err := s.manager.Store.User()
	if err != nil {
		s.failure(err)
		return
	}
	auto, err := settings.Automatic()
	if err != nil {
		s.failure(err)
		return
	}
	warnings := core.OverrideWarnings(auto, user)
	if len(warnings) == 0 {
		s.show(form)
		return
	}
	form.AddButton("查看配置来源", func() { s.resultDetails(strings.Join(warnings, "\n")) })
	view := tview.NewTextView().SetText(strings.Join(warnings, "\n")).SetScrollable(true).SetWrap(true)
	view.SetBorder(true).SetTitle(" 配置来源 · user.yaml 优先 ")
	layout := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(form, 0, 1, true).AddItem(view, 5, 0, false)
	configureForm(form)
	s.show(layout)
	s.app.SetFocus(form)
}

func (s *screen) dns() {
	settings, err := s.manager.Store.Settings()
	if err != nil {
		s.failure(err)
		return
	}
	baseline := settings.Clone()
	s.dnsForm(settings, baseline)
}

func (s *screen) dnsForm(settings, baseline core.Settings) {
	form := tview.NewForm()
	fields := make([]string, len(core.DNSListKeys))
	form.AddCheckbox("DNS 模式：fake-ip", settings.FakeIP, func(v bool) { settings.FakeIP = v })
	form.AddCheckbox("启用 IPv6 解析", settings.Config.Text("dns", "ipv6") == "true", func(v bool) {
		if err := settings.Config.Set(v, "dns", "ipv6"); err != nil {
			s.failure(err)
		}
	})
	form.AddCheckbox("启用域名嗅探", settings.Config.Text("sniffer", "enable") == "true", func(v bool) {
		if err := settings.Config.Set(v, "sniffer", "enable"); err != nil {
			s.failure(err)
		}
	})
	form.AddInputField("DNS 监听地址", settings.Config.Text("dns", "listen"), 45, nil, func(text string) {
		if err := settings.Config.Set(strings.TrimSpace(text), "dns", "listen"); err != nil {
			s.failure(err)
		}
	})
	for i, key := range core.DNSListKeys {
		values, err := core.StringList(settings.Config, "dns", key)
		if err != nil {
			s.failure(err)
			return
		}
		fields[i] = strings.Join(values, ", ")
		form.AddInputField(key, fields[i], 0, nil, func(text string) { fields[i] = text })
	}
	collect := func() bool {
		candidate := settings.Config.Clone()
		for i, key := range core.DNSListKeys {
			values, err := core.ParseCommaList(fields[i], key != "direct-nameserver")
			if err != nil {
				s.failure(fmt.Errorf("%s: %w", key, err))
				return false
			}
			if err = candidate.Set(values, "dns", key); err != nil {
				s.failure(err)
				return false
			}
		}
		settings.Config = candidate
		return true
	}
	commit := func() {
		if !collect() {
			return
		}
		snapshot := settings.Clone()
		if err := snapshot.Config.Set(true, "dns", "enable"); err != nil {
			s.failure(err)
			return
		}
		if err := snapshot.Validate(); err != nil {
			s.failure(err)
			return
		}
		s.onSuccess = s.close
		s.keepDialog = true
		s.work("配置已更新", func(ctx context.Context) error {
			return s.manager.ApplySettings(ctx, snapshot)
		})
		s.keepDialog = false
	}
	dirty := func() bool {
		candidate := settings.Clone()
		for i, key := range core.DNSListKeys {
			values, err := core.ParseCommaList(fields[i], key != "direct-nameserver")
			// 无效输入仍需在退出前处理。
			if err != nil {
				return true
			}
			if err = candidate.Config.Set(values, "dns", key); err != nil {
				return true
			}
		}
		return !candidate.Equal(baseline)
	}
	back := func() {
		if !dirty() {
			s.close()
			return
		}
		s.confirmSettings(form, "dns-confirm", "DNS 配置有修改，是否应用？", commit)
	}
	form.AddButton("管理 Fake-IP 过滤列表", func() {
		if !collect() {
			return
		}
		s.fakeIPFilter(&settings, func() { s.dnsForm(settings, baseline) })
	})
	form.AddButton("一键重置 DNS 配置", func() {
		if err := settings.ResetDNS(); err != nil {
			s.failure(err)
			return
		}
		s.dnsForm(settings, baseline)
	})
	form.AddButton("返回", back)
	form.SetCancelFunc(back)
	title := " DNS 配置 · 上游逗号分隔 · 退出时选择应用 "
	if settings.Config.Text("dns", "enable") != "true" {
		title = " DNS 当前关闭 · 应用本页后启用 DNS "
	}
	form.SetBorder(true).SetTitle(title)
	s.showManagedForm(form, settings)
}

func (s *screen) fakeIPFilter(settings *core.Settings, back func()) {
	values, err := core.StringList(settings.Config, "dns", "fake-ip-filter")
	if err != nil {
		s.failure(err)
		return
	}
	area := tview.NewTextArea().SetText(strings.Join(values, "\n"), false)
	area.SetBorder(true).SetTitle(" Fake-IP 过滤列表 · 每行一项 · [Ctrl+S 确定] · [Esc 放弃] ")
	area.SetInputCapture(func(e *tcell.EventKey) *tcell.EventKey {
		switch e.Key() {
		case tcell.KeyCtrlS:
			entries := []string{}
			for _, line := range strings.Split(area.GetText(), "\n") {
				if value := strings.TrimSpace(line); value != "" {
					entries = append(entries, value)
				}
			}
			if err := settings.Config.Set(entries, "dns", "fake-ip-filter"); err != nil {
				s.failure(err)
				return nil
			}
			back()
			return nil
		case tcell.KeyEscape:
			back()
			return nil
		}
		return e
	})
	s.show(area)
	s.footer.SetText("[yellow]" + shortcutHint("Ctrl+S", "确定列表") + "[-]  [yellow]" + shortcutHint("Esc", "放弃列表修改") + "[-]；返回 DNS 页后，退出时确认应用")
}
