package ui

import (
	"context"
	"fmt"

	"github.com/rivo/tview"
)

func (s *screen) startup() {
	status, err := s.manager.Status(context.Background())
	if err != nil {
		s.failure(err)
		return
	}
	if status.Running {
		s.workResult(s.manager.AutoStart)
		return
	}
	missing, err := s.manager.Store.KernelMissing()
	if err != nil {
		s.failure(err)
		return
	}
	if !missing {
		s.workResult(s.manager.AutoStart)
		return
	}
	settings, err := s.manager.Store.Settings()
	if err != nil {
		s.failure(err)
		return
	}
	s.notice.SetText("未安装 mihomo 内核，下载后可启动代理。")
	s.show(tview.NewModal().SetText(fmt.Sprintf("找不到 mihomo 内核：%s\n是否通过当前网络下载 GitHub 官方稳定版？", settings.MihomoPath)).
		AddButtons([]string{"下载", "取消"}).SetDoneFunc(func(index int, _ string) {
		s.close()
		if index != 0 {
			s.notice.SetText("已取消下载，内核保持停止。可在基本配置中设置已有内核路径。")
			return
		}
		s.workResult(func(ctx context.Context) (string, error) {
			if err := s.manager.InstallKernel(ctx); err != nil {
				return "", err
			}
			message, err := s.manager.AutoStart(ctx)
			return "内核已下载；" + message, err
		})
	}))
}
