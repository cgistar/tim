package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"tim/internal/core"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type screen struct {
	rootSession           bool
	onSuccess             func()
	errorLogPath          string
	logView               *tview.TextView
	logUpdated            time.Time
	logFollow             bool
	app                   *tview.Application
	pages                 *tview.Pages
	status                *tview.TextView
	notice                *resultView
	right                 *tview.TextView
	footer                *tview.TextView
	version, latest       string
	versionUnavailable    bool
	ips                   map[string]string
	network               bool
	networkCancel         context.CancelFunc
	generation            uint64
	keepDialog            bool
	menu                  *tview.List
	manager               *core.Manager
	busy                  bool
	last                  core.Status
	cancel                context.CancelFunc
	exitDelay, frontDelay nodeDelay
}

func Run(m *core.Manager) error {
	s := newScreen(m)
	session := m.EnableRootSession(s.rootPassword)
	s.rootSession = os.Geteuid() != 0
	defer session.Close()
	s.network = true
	defer func() {
		s.resetDelays()
		if s.networkCancel != nil {
			s.networkCancel()
		}
	}()
	go s.app.QueueUpdateDraw(func() {
		s.startup()
		if !s.busy {
			s.refreshNetwork(false)
		}
	})
	return s.app.Run()
}
func newScreen(m *core.Manager) *screen {
	tview.Styles.PrimitiveBackgroundColor = tcell.ColorDefault
	s := &screen{app: tview.NewApplication().EnableMouse(true), pages: tview.NewPages(), manager: m, ips: make(map[string]string), last: core.Status{Unknown: true}}
	s.status = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	s.status.SetScrollable(false)
	s.right = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignRight).SetWrap(false)
	s.footer = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	s.notice = newResultView()
	s.notice.SetBorder(true).SetTitle(" 操作日志 ")
	s.notice.SetText("输入订阅 URL，下载后 [S 启动]。[Tab 切换焦点]，[Esc 返回]。")
	s.menu = tview.NewList().ShowSecondaryText(true)
	s.notice.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyTab {
			s.app.SetFocus(s.menu)
			return nil
		}
		return event
	})
	s.menu.SetSelectedStyle(controlFocusStyle())
	menuItems := []struct {
		key                rune
		label, description string
		open               func()
	}{
		{'c', "订阅设置", "配置订阅 URL", s.settings},
		{'p', "基本配置", "端口 / 监听 / 延迟测试地址", s.ports},
		{'d', "DNS 配置", "监听 / fake-ip / IPv6 / 域名嗅探 / 上游 / 过滤列表", s.dns},
		{'a', "链式前置代理", "独立节点 YAML / 分享链接 / Base64", s.chain},
		{'e', "编辑 user.yaml", "保留自定义配置；停止输入后自动应用", s.editor},
		{'i', "安装服务", "系统保活 / 开机启动", s.serviceAction},
	}
	for _, item := range menuItems {
		s.menu.AddItem(shortcutHint(strings.ToUpper(string(item.key)), item.label), item.description, 0, item.open)
	}
	// 列表内置快捷键固定绘制为圆括号，改由菜单处理以统一提示样式。
	s.menu.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyRune {
			for i, item := range menuItems {
				if unicode.ToLower(event.Rune()) == item.key {
					s.menu.SetCurrentItem(i)
					item.open()
					return nil
				}
			}
		}
		return event
	})
	s.menu.SetBorder(true).SetTitle(" 操作 ")
	header := tview.NewFlex().AddItem(s.status, 0, 1, false).AddItem(s.right, 48, 0, false)
	header.SetBorder(true).SetTitle(" mihomo · Linux ")
	s.app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		s.refreshLogs()
		width, _ := screen.Size()
		desired := 0
		for _, line := range strings.Split(s.right.GetText(true), "\n") {
			if n := tview.TaggedStringWidth(tview.Escape(line)); n > desired {
				desired = n
			}
		}
		available := width - 42
		if available < 20 {
			available = 20
		}
		if desired > available {
			desired = available
		}
		header.ResizeItem(s.right, desired, 0)
		return false
	})
	root := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(header, 4, 0, false).AddItem(s.menu, 0, 1, true).AddItem(s.notice, 5, 0, false)
	s.pages.AddPage("main", root, true, true)
	s.app.SetMouseCapture(func(event *tcell.EventMouse, action tview.MouseAction) (*tcell.EventMouse, tview.MouseAction) {
		if page, _ := s.pages.GetFrontPage(); page == "root-authorization" {
			return event, action
		}
		if s.busy {
			return nil, action
		}
		if s.kernelDownloadMouse(event, action) || s.delayMouse(event, action) {
			return nil, action
		}
		return event, action
	})
	s.app.SetInputCapture(func(e *tcell.EventKey) *tcell.EventKey {
		if page, _ := s.pages.GetFrontPage(); page == "root-authorization" && e.Key() != tcell.KeyCtrlC {
			return e
		}
		if s.busy {
			if e.Key() == tcell.KeyCtrlC && s.cancel != nil {
				s.cancel()
				s.notice.SetText("正在取消，请等待当前步骤结束；已保存的修改保留…")
			}
			return nil
		}
		if e.Key() == tcell.KeyCtrlC {
			s.app.Stop()
			return nil
		}
		if page, _ := s.pages.GetFrontPage(); page == "main" && e.Key() == tcell.KeyRune {
			switch unicode.ToLower(e.Rune()) {
			case 'v':
				s.preview()
				return nil
			case 'f':
				s.work("状态已刷新", func(context.Context) error { return nil })
				return nil
			case 'q':
				s.app.Stop()
				return nil
			case 'o':
				s.resultDetails(s.notice.GetText(false))
				return nil
			case 'l':
				s.logs()
				return nil
			case 'u':
				if s.latest != "" {
					s.work("内核已更新", m.Update)
				}
				return nil
			case 's':
				s.runKernel(m.ToggleRunning)
				return nil
			case 'r':
				s.runKernel(m.Restart)
				return nil
			case 'k':
				if s.versionUnavailable {
					s.kernelDialog(nil)
				}
				return nil
			case 'm':
				s.mode()
				return nil
			case 't':
				s.tun()
				return nil
			case 'b':
				s.networkGuard()
				return nil
			}
		}
		return e
	})
	s.renderStatus()
	layout := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(s.pages, 0, 1, true).AddItem(s.footer, 1, 0, false)
	s.app.SetRoot(layout, true).SetFocus(s.menu)
	return s
}
func (s *screen) work(message string, fn func(context.Context) error) {
	s.workResult(func(ctx context.Context) (string, error) { return message, fn(ctx) })
}
func (s *screen) workResult(fn func(context.Context) (string, error)) {
	if s.busy {
		return
	}
	if !s.keepDialog {
		s.close()
	}
	s.busy = true
	s.resetDelays()
	s.renderStatus()
	onSuccess := s.onSuccess
	s.onSuccess = nil
	if s.networkCancel != nil {
		s.networkCancel()
	}
	s.generation++
	s.notice.SetText("正在处理，[Ctrl+C 取消]…")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	s.cancel = cancel
	go func() {
		defer cancel()
		message, err := fn(ctx)
		statusCtx, statusCancel := context.WithTimeout(ctx, 5*time.Second)
		st, se := s.manager.Status(statusCtx)
		statusCancel()
		// 操作失败后只展示错误，避免收尾状态查询立即再次要求密码。
		needsRoot := err == nil && s.rootSession && st.Guard.NeedsRoot
		if needsRoot {
			// 先展示无需权限的状态并开始联网查询，再等待防火墙的会话授权。
			initial := st
			s.app.QueueUpdateDraw(func() {
				s.last = initial
				s.updateServiceMenu()
				s.renderStatus()
				if s.network {
					s.refreshNetwork(se == nil)
				}
			})
			st.Guard, _ = s.manager.NetworkGuardStatus(ctx)
		}
		s.app.QueueUpdateDraw(func() {
			s.busy = false
			if err == nil && onSuccess != nil {
				onSuccess()
			}
			s.cancel = nil
			s.last = st
			s.updateServiceMenu()
			s.renderStatus()
			if s.network && !needsRoot {
				s.refreshNetwork(se == nil)
			}
			msg := message
			if err != nil {
				msg = err.Error()
			}
			if se != nil {
				if err == nil {
					msg = "状态检查: " + se.Error()
				} else if !strings.Contains(msg, se.Error()) {
					msg += "\n状态检查: " + se.Error()
				}
			}
			if st.Guard.Error != "" && !strings.Contains(msg, st.Guard.Error) {
				if err == nil && se == nil {
					msg = "断网保护异常: " + st.Guard.Error
				} else {
					msg += "\n断网保护异常: " + st.Guard.Error
				}
			}
			if st.Chain.Reason != "" {
				msg += "\n链式前置: " + st.Chain.Text()
			}
			if !st.Running && st.Service.Active {
				msg += "\n" + st.Message
			}
			s.notice.SetText(msg)
			if err != nil || se != nil || st.Guard.Error != "" {
				s.failure(fmt.Errorf("%s", msg))
			}
		})
	}()
}
func (s *screen) close() {
	s.logView = nil
	s.pages.RemovePage("dialog")
	s.app.SetFocus(s.menu)
	s.renderFooter()
}
func (s *screen) show(p tview.Primitive) {
	switch item := p.(type) {
	case *tview.Form:
		configureForm(item)
	case *tview.Modal:
		configureModal(item)
	case *tview.TextArea:
		item.SetSelectedStyle(controlFocusStyle())
	}
	s.pages.AddPage("dialog", p, true, true)
	s.app.SetFocus(p)
	s.footer.SetText("[yellow]" + shortcutHint("Esc", "返回") + "[-]  [yellow]" + shortcutHint("Tab", "切换焦点") + "[-]")
}
func (s *screen) failure(err error) {
	message := err.Error()
	logPath := s.errorLogPath
	if logPath == "" {
		logPath = s.manager.Store.Path("error.log")
	}
	logErr := appendErrorLog(logPath, message)
	if logErr != nil {
		message += "\n错误日志写入失败: " + logErr.Error()
	}
	if page, _ := s.pages.GetFrontPage(); page == "main" {
		s.notice.SetText(time.Now().Format("2006-01-02 15:04:05") + " error: " + message)
		return
	}
	if logErr == nil {
		message += "\n完整错误已追加至 error.log"
	}
	s.notice.SetText(message)
	s.showResultDetails(message, true)
}
func (s *screen) resultDetails(message string) {
	s.showResultDetails(message, false)
}
func (s *screen) showResultDetails(message string, popup bool) {
	previous := s.app.GetFocus()
	previousFooter := s.footer.GetText(false)
	detail := newResultView().SetText(message)
	detail.SetBorder(true).SetTitle(" 完整操作结果 · 左键选择 · 右键复制 · [Esc 返回] ")
	dismiss := func() { s.pages.RemovePage("error"); s.app.SetFocus(previous); s.footer.SetText(previousFooter) }
	detail.SetInputCapture(func(e *tcell.EventKey) *tcell.EventKey {
		if e.Key() == tcell.KeyEscape || e.Key() == tcell.KeyEnter {
			dismiss()
			return nil
		}
		return e
	})
	var view tview.Primitive = detail
	if popup {
		detail.SetTitle(" 错误提示 · [Esc / Enter 关闭] · ↑↓ 滚动 ")
		detail.SetBackgroundColor(tcell.ColorDarkSlateGray)
		detail.SetTextStyle(tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorDarkSlateGray))
		row := tview.NewFlex().AddItem(nil, 0, 1, false).AddItem(detail, 88, 0, true).AddItem(nil, 0, 1, false)
		layout := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(nil, 0, 1, false).AddItem(row, 18, 0, true).AddItem(nil, 0, 1, false)
		layout.SetDrawFunc(func(_ tcell.Screen, _, _, width, height int) (int, int, int, int) {
			row.ResizeItem(detail, max(1, min(88, width-4)), 0)
			layout.ResizeItem(row, max(1, min(18, height-4)), 0)
			return layout.GetInnerRect()
		})
		view = layout
	}
	s.pages.AddPage("error", view, true, true)
	s.app.SetFocus(detail)
	s.footer.SetText("[yellow]" + shortcutHint("↑↓/PgUp/PgDn", "滚动") + "[-]  [yellow]" + shortcutHint("Home/End", "首尾") + "[-]  [yellow]" + shortcutHint("Esc", "返回") + "[-]")
}
func (s *screen) confirm(text string, fn func()) {
	s.show(tview.NewModal().SetText(text).AddButtons([]string{"执行", "取消"}).SetDoneFunc(func(i int, _ string) {
		s.close()
		if i == 0 {
			fn()
		}
	}))
}

func (s *screen) updateServiceMenu() {
	label, description := "安装服务", "保活 / 开机启动；请先停止内核"
	if s.last.Service.Installed {
		label, description = "卸载服务", "确认后停止内核并移除服务，保留配置和数据"
	}
	s.menu.SetItemText(5, shortcutHint("I", label), description)
}

func (s *screen) serviceAction() {
	if s.last.Service.Installed {
		s.confirm("将停止内核并卸载服务，保留配置和数据。是否继续？", func() {
			s.work("服务已卸载，内核已停止；断网保护保持原状，需单独关闭", s.manager.UninstallService)
		})
		return
	}
	s.work("服务已安装，内核保持停止；按 S 启动", s.manager.InstallService)
}
func (s *screen) settings() {
	v, err := s.manager.Store.Settings()
	if err != nil {
		s.failure(err)
		return
	}
	form := tview.NewForm()
	commit := func() {
		snapshot := v.Clone()
		s.onSuccess = s.close
		s.keepDialog = true
		s.work("订阅下载成功并已保存", func(ctx context.Context) error {
			return s.manager.RefreshURL(ctx, snapshot.URL)
		})
		s.keepDialog = false
	}
	form.AddInputField("订阅 URL", v.URL, 70, nil, func(t string) { v.URL = strings.TrimSpace(t) })
	form.AddButton("下载 / 刷新订阅", commit)
	back := func() {
		saved, err := s.manager.Store.Settings()
		if err != nil {
			s.failure(err)
			return
		}
		if v.URL == saved.URL {
			s.close()
			return
		}
		s.confirmSettings(form, "subscription-confirm", "订阅地址有修改，是否保存地址并下载订阅？", commit)
	}
	form.AddButton("返回", back)
	form.SetCancelFunc(back)
	form.SetBorder(true).SetTitle(" 订阅设置 · 下载前保存地址 ")
	s.show(form)
}

func (s *screen) ports() {
	settings, err := s.manager.Store.Settings()
	if err != nil {
		s.failure(err)
		return
	}
	cfg := settings.Config
	form := tview.NewForm()
	type field struct {
		label   string
		path    []string
		integer bool
		value   string
	}
	fields := []field{{"Mixed 端口（0 关闭）", []string{"mixed-port"}, true, ""}, {"HTTP 端口（0 关闭）", []string{"port"}, true, ""}, {"SOCKS 端口（0 关闭）", []string{"socks-port"}, true, ""}, {"控制接口地址:端口", []string{"external-controller"}, false, ""}}
	for i := range fields {
		f := &fields[i]
		f.value = cfg.Text(f.path...)
		if f.integer && f.value == "" {
			f.value = "0"
		}
		form.AddInputField(f.label, f.value, 45, nil, func(t string) { f.value = strings.TrimSpace(t) })
	}
	lan := cfg.Text("allow-lan") == "true"
	form.AddCheckbox("允许局域网", lan, func(v bool) { lan = v })
	stack := cfg.Text("tun", "stack")
	stackOptions := []string{"mixed", "system", "gvisor"}
	form.AddDropDown("栈模式", stackOptions, slices.Index(stackOptions, stack), func(option string, _ int) { stack = option })
	strictRoute := cfg.Text("tun", "strict-route") == "true"
	form.AddCheckbox("严格路由", strictRoute, func(v bool) { strictRoute = v })
	delayURL := settings.DelayURL
	form.AddInputField("延迟测试 URL（留空默认）", delayURL, 60, nil, func(v string) { delayURL = strings.TrimSpace(v) })
	form.GetFormItem(form.GetFormItemCount() - 1).(*tview.InputField).SetPlaceholder(core.DefaultDelayURL)
	mihomoPath := settings.MihomoPath
	form.AddInputField("mihomo 路径", mihomoPath, 60, nil, func(v string) { mihomoPath = strings.TrimSpace(v) })
	dataDir := settings.DataDir
	form.AddInputField("data 目录", dataDir, 60, nil, func(v string) { dataDir = strings.TrimSpace(v) })
	if s.manager.Store.DataOverride != "" {
		form.AddTextView("DATA 环境变量优先", s.manager.Store.DataDir(), 60, 2, false, false)
	}
	initial := make([]string, len(fields))
	for i, f := range fields {
		initial[i] = f.value
	}
	initialLAN := lan
	commit := func() {
		if !slices.Contains(stackOptions, stack) {
			s.failure(fmt.Errorf("请选择栈模式：mixed、system 或 gvisor"))
			return
		}
		snapshot := settings.Clone()
		snapshot.DelayURL = delayURL
		snapshot.MihomoPath = mihomoPath
		snapshot.DataDir = dataDir
		for _, f := range fields {
			var value any = f.value
			if f.integer {
				n, err := strconv.Atoi(f.value)
				if err != nil || n < 0 || n > 65535 {
					s.failure(fmt.Errorf("%s 必须为 0–65535", f.label))
					return
				}
				value = n
			}
			if err := snapshot.Config.Set(value, f.path...); err != nil {
				s.failure(err)
				return
			}
		}
		if err := snapshot.Config.Set(lan, "allow-lan"); err != nil {
			s.failure(err)
			return
		}
		if err := snapshot.Config.Set(stack, "tun", "stack"); err != nil {
			s.failure(err)
			return
		}
		if err := snapshot.Config.Set(strictRoute, "tun", "strict-route"); err != nil {
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
	back := func() {
		dirty := lan != initialLAN || stack != cfg.Text("tun", "stack") || strictRoute != (cfg.Text("tun", "strict-route") == "true") || delayURL != settings.DelayURL || mihomoPath != settings.MihomoPath || dataDir != settings.DataDir
		for i, f := range fields {
			dirty = dirty || f.value != initial[i]
		}
		if !dirty {
			s.close()
			return
		}
		s.confirmSettings(form, "ports-confirm", "基本配置有修改，是否应用新配置？", commit)
	}
	form.AddButton("返回", back)
	form.SetCancelFunc(back)
	form.SetBorder(true).SetTitle(" 基本配置 · 退出时选择应用 ")
	s.showManagedForm(form, settings)
}
func (s *screen) mode() {
	s.work("配置已更新", func(ctx context.Context) error {
		d, _, err := s.manager.Preview()
		if err != nil {
			return err
		}
		mode := "global"
		if d.Text("mode") == "global" {
			mode = "rule"
		}
		return s.manager.ToggleMode(ctx, mode)
	})
}
func (s *screen) tun() {
	s.work("配置已更新", func(ctx context.Context) error {
		d, _, err := s.manager.Preview()
		if err != nil {
			return err
		}
		return s.manager.ToggleTun(ctx, d.Text("tun", "enable") != "true")
	})
}
func (s *screen) editor() {
	b, err := os.ReadFile(s.manager.Store.Path("user.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		b, err = []byte{}, nil
	}
	if err != nil {
		s.failure(err)
		return
	}
	if strings.TrimSpace(string(b)) == "{}" {
		b = []byte{}
	}
	area := tview.NewTextArea().SetText(string(b), false)
	area.SetBorder(true).SetTitle(" user.yaml · 停止输入 1 秒自动应用 · [Esc 返回] ")
	saved := string(b)
	var timer *time.Timer
	revision := 0
	apply := func(closeAfter bool) {
		raw := area.GetText()
		if raw == saved {
			if closeAfter {
				s.close()
			}
			return
		}
		doc, err := core.ParseUser([]byte(raw))
		if err != nil {
			s.notice.SetText(err.Error())
			if closeAfter {
				s.failure(err)
			}
			return
		}
		if s.busy {
			return
		}
		s.onSuccess = func() {
			saved = raw
			if closeAfter {
				s.close()
			}
		}
		s.keepDialog = true
		s.work("配置已更新", func(ctx context.Context) error {
			return s.manager.Apply(ctx, doc)
		})
		s.keepDialog = false
	}
	area.SetChangedFunc(func() {
		revision++
		current := revision
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(time.Second, func() {
			s.app.QueueUpdateDraw(func() {
				if page, _ := s.pages.GetFrontPage(); page == "dialog" && current == revision {
					apply(false)
				}
			})
		})
	})
	area.SetInputCapture(func(e *tcell.EventKey) *tcell.EventKey {
		if e.Key() == tcell.KeyEscape || e.Key() == tcell.KeyCtrlS {
			if timer != nil {
				timer.Stop()
			}
			apply(true)
			return nil
		}
		return e
	})
	s.show(area)
}
func (s *screen) view(title, text string) {
	v := tview.NewTextView().SetText(text).SetWrap(false).SetScrollable(true)
	v.SetBorder(true).SetTitle(" " + title + " · [Esc 返回] ")
	v.SetInputCapture(func(e *tcell.EventKey) *tcell.EventKey {
		if e.Key() == tcell.KeyEscape {
			s.close()
			return nil
		}
		return e
	})
	s.show(v)
}
func (s *screen) preview() {
	d, w, err := s.manager.Preview()
	if err != nil {
		s.failure(err)
		return
	}
	b, err := d.Bytes()
	if err != nil {
		s.failure(err)
		return
	}
	s.view("最终配置（含凭据，请勿分享）", strings.Join(w, "\n")+"\n"+string(b))
}
func (s *screen) logs() {
	v, err := s.manager.LogTail()
	if err != nil {
		s.failure(err)
		return
	}
	view := tview.NewTextView().SetWrap(false).SetScrollable(true).SetText(v)
	view.SetBorder(true).SetTitle(" mihomo 日志 · 每秒刷新 · [↑ 暂停跟随] · [End 最新] · [Esc 返回] ")
	s.logView = view
	s.logFollow = true
	s.logUpdated = time.Now()
	view.ScrollToEnd()
	view.SetInputCapture(func(e *tcell.EventKey) *tcell.EventKey {
		switch e.Key() {
		case tcell.KeyEscape:
			s.close()
			return nil
		case tcell.KeyEnd:
			s.logFollow = true
			view.ScrollToEnd()
			return nil
		case tcell.KeyUp, tcell.KeyPgUp, tcell.KeyHome:
			s.logFollow = false
		}
		return e
	})
	s.show(view)
}
func (s *screen) refreshLogs() {
	if s.logView == nil || time.Since(s.logUpdated) < time.Second {
		return
	}
	s.logUpdated = time.Now()
	text, err := s.manager.LogTail()
	if err != nil {
		s.notice.SetText("日志刷新失败: " + err.Error())
		return
	}
	if s.logView.GetText(false) != text {
		row, col := s.logView.GetScrollOffset()
		s.logView.SetText(text)
		if s.logFollow {
			s.logView.ScrollToEnd()
		} else {
			s.logView.ScrollTo(row, col)
		}
	}
}

func (s *screen) renderStatus() {
	state, mode := "停止", ""
	color := "red"
	if s.last.Running {
		state, color = "运行", "green"
	} else if s.last.Unknown {
		state, color = "状态未知", "yellow"
	}
	if s.last.Running {
		switch s.last.Mode {
		case "rule":
			mode = "规则"
		case "global":
			mode = "全局"
		case "direct":
			mode = "直连"
		}
	}
	var line string
	if s.last.Running && s.last.Mode == "rule" {
		line = s.delayNode(&s.exitDelay, "出口: ", "", "按规则路由")
	} else if s.last.Running && s.last.Mode == "global" && s.last.Global != "" {
		line = s.delayNode(&s.exitDelay, "GLOBAL: ", s.last.Global, s.last.Global)
	} else {
		s.exitDelay.reset()
	}
	if s.last.Running && s.last.Chain.Text() != "" {
		if line != "" {
			line += "  |  "
		}
		line = s.delayNode(&s.frontDelay, line+"链式前置: ", s.last.Chain.Proxy, s.last.Chain.Text())
	} else {
		s.frontDelay.reset()
	}
	statusItems := []string{fmt.Sprintf("[%s]%s[-]", color, state)}
	if mode != "" {
		statusItems = append(statusItems, mode)
	}
	if s.last.Running && s.last.Tun {
		statusItems = append(statusItems, "TUN")
	}
	if s.last.Service.Installed {
		statusItems = append(statusItems, "Service")
		if !s.last.Running && s.last.Service.Result != "" && s.last.Service.Result != "success" {
			line += "  服务: " + tview.Escape(s.last.Service.State+" "+s.last.Service.Result)
		}
	}
	if s.last.Guard.Enabled {
		statusItems = append(statusItems, "已断网保存")
	}
	if s.last.Guard.Active && (!s.last.Running || !s.last.Tun) {
		if line != "" {
			line += "  |  "
		}
		line += "保护已开启，TUN 未就绪"
	}
	s.status.SetText(strings.Join(statusItems, " | ") + "\n" + line)
	version := s.version
	if version == "" {
		version = "读取中"
	}

	items := []string{}
	for _, probe := range core.PublicIPProbes() {
		value := s.ips[probe.Label]
		if value == "" {
			value = "未查询"
		}
		items = append(items, probe.Label+": "+tview.Escape(value))
	}
	versionText := "mihomo " + tview.Escape(version)
	if s.versionUnavailable {
		versionText = "mihomo [yellow]" + tview.Escape("[下载]") + "[-]"
	}
	s.right.SetText(versionText + "\n" + strings.Join(items, "  |  "))
	if page, _ := s.pages.GetFrontPage(); page == "main" {
		s.renderFooter()
	}
}
func (s *screen) renderFooter() {
	items := []string{
		shortcutHint("S", "启动/停止"), shortcutHint("R", "重启"),
		shortcutHint("L", "日志"), shortcutHint("M", "模式"), shortcutHint("T", "TUN"),
		shortcutHint("B", "断网保护"),
	}
	if s.latest != "" {
		items = append(items, shortcutHint("U", "更新 "+s.latest))
	}
	if s.versionUnavailable {
		items = append(items, shortcutHint("K", "下载内核"))
	}
	items = append(items, shortcutHint("V", "预览"), shortcutHint("F", "刷新"), shortcutHint("O", "结果"), shortcutHint("Q", "退出"))
	text := "[yellow]" + strings.Join(items, "  ") + "[-]"
	s.footer.SetText(text)
}

func (s *screen) refreshNetwork(healthy bool) {
	if s.networkCancel != nil {
		s.networkCancel()
	}
	s.generation++
	// TUN 重建后不能复用旧路由下建立的空闲连接。
	s.manager.IPClient.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	s.networkCancel = cancel
	generation := s.generation
	running := s.last.Running && healthy
	for _, probe := range core.PublicIPProbes() {
		s.ips[probe.Label] = "查询中"
	}
	s.latest = ""
	s.renderStatus()
	go func() {
		version, err := s.manager.CurrentVersion(ctx)
		unavailable := err != nil
		if ctx.Err() != nil {
			return
		}
		s.app.QueueUpdateDraw(func() {
			if generation == s.generation {
				s.version = version
				s.versionUnavailable = unavailable
				if err != nil {
					s.notice.SetText(s.notice.GetText(false) + "\n内核版本读取失败: " + err.Error())
				}
				s.renderStatus()
			}
		})
		if !running {
			return
		}
		release, err := s.manager.Latest(ctx)
		if ctx.Err() != nil {
			return
		}
		s.app.QueueUpdateDraw(func() {
			if generation != s.generation {
				return
			}
			if err == nil && core.NewerRelease(version, release.Tag) {
				s.latest = release.Tag
			}
			if err != nil {
				s.notice.SetText(s.notice.GetText(false) + "\n版本检查失败: " + err.Error())
			}
			s.renderStatus()
		})
	}()
	for _, probe := range core.PublicIPProbes() {
		go func() {
			ip, err := s.manager.PublicIP(ctx, probe)
			if errors.Is(ctx.Err(), context.Canceled) {
				return
			}
			s.app.QueueUpdateDraw(func() {
				if generation != s.generation {
					return
				}
				s.ips[probe.Label] = "查询失败"
				if err == nil {
					s.ips[probe.Label] = ip
				} else {
					s.notice.SetText(s.notice.GetText(false) + "\n" + probe.Label + " 查询失败: " + err.Error())
				}
				s.renderStatus()
			})
		}()
	}
}

// 追加完整错误，避免后续操作覆盖排障信息。
func appendErrorLog(path, message string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintf(file, "[%s]\n%s\n\n", time.Now().Format(time.RFC3339), message)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
