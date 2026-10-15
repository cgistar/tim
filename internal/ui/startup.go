package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"tim/internal/core"
)

func (s *screen) startup() {
	// 普通用户启动时异步查询控制接口，让界面先完成绘制。
	if s.rootSession {
		s.work("状态已刷新", func(context.Context) error { return nil })
		return
	}
	status, err := s.manager.Status(context.Background())
	s.last = status
	s.updateServiceMenu()
	s.renderStatus()
	if err != nil {
		s.failure(err)
		return
	}
	if status.Running || status.Service.Active {
		s.work(status.Message, func(context.Context) error { return nil })
		return
	}
	s.notice.SetText("内核已停止，按 S 启动；进入界面不会自动启动。")
}

func (s *screen) runKernel(action func(context.Context) (string, error)) {
	if s.last.Running || s.last.Service.Active {
		s.workResult(action)
		return
	}
	missing, err := s.manager.Store.KernelMissing()
	if err != nil {
		s.failure(err)
		return
	}
	if !missing {
		s.workResult(action)
		return
	}
	s.kernelDialog(action)
}

// action 非空表示补齐内核后继续用户刚才请求的启动操作。
func (s *screen) kernelDialog(action func(context.Context) (string, error)) {
	settings, err := s.manager.Store.Settings()
	if err != nil {
		s.failure(err)
		return
	}
	form := tview.NewForm()
	path := settings.MihomoPath
	form.AddTextView("提示", "请输入已有 mihomo 路径，或通过当前网络下载 GitHub 官方稳定版。相对路径以 setting.yaml 所在目录为基准。", 64, 3, false, false)
	form.AddInputField("mihomo 路径", path, 60, nil, func(value string) { path = strings.TrimSpace(value) })
	apply := func(download bool) {
		selected := path
		version := ""
		s.onSuccess = func() {
			s.version, s.versionUnavailable = version, false
			s.close()
		}
		s.keepDialog = true
		s.workResult(func(ctx context.Context) (string, error) {
			if download {
				if err := s.manager.InstallKernel(ctx); err != nil {
					return "", err
				}
				var err error
				version, err = s.manager.CurrentVersion(ctx)
				if err != nil {
					return "", err
				}
			} else {
				if selected == "" {
					return "", fmt.Errorf("请输入 mihomo 路径")
				}
				output, err := core.Version(ctx, s.manager.Store.ResolvePath(selected))
				if err != nil {
					return "", err
				}
				settings.MihomoPath = selected
				if err = s.manager.ApplySettings(ctx, settings); err != nil {
					return "", err
				}
				version = core.VersionTag(output)
			}
			if action != nil {
				return action(ctx)
			}
			return "内核已就绪，保持停止；按 S 启动", nil
		})
		s.keepDialog = false
	}
	form.AddButton("验证并使用", func() { apply(false) })
	form.AddButton("下载", func() { apply(true) })
	form.AddButton("取消", s.close)
	form.SetCancelFunc(s.close)
	form.SetFocus(1)
	form.SetBorder(true).SetTitle(" 设置 mihomo 内核 ")
	s.show(form)
}

func (s *screen) kernelDownloadMouse(event *tcell.EventMouse, action tview.MouseAction) bool {
	page, _ := s.pages.GetFrontPage()
	if page != "main" || !s.versionUnavailable || (action != tview.MouseLeftClick && action != tview.MouseLeftDoubleClick && action != tview.MouseLeftDown) {
		return false
	}
	x, y := event.Position()
	left, top, width, height := s.right.GetInnerRect()
	buttonWidth := tview.TaggedStringWidth(tview.Escape("[下载]"))
	if height < 1 || y != top || x < left || x < left+width-buttonWidth || x >= left+width {
		return false
	}
	if action != tview.MouseLeftDown {
		s.kernelDialog(nil)
	}
	return true
}
