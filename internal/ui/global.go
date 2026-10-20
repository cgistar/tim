package ui

import (
	"context"
	"fmt"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"tim/internal/core"
)

func (s *screen) globalSelector() {
	if s.busy || s.globalLoading {
		return
	}
	if !s.last.Running || s.last.Mode != "global" {
		s.notice.SetText("仅在全局模式运行时可切换 GLOBAL 出口；按 [M] 切换模式。")
		return
	}
	s.globalLoading = true
	s.notice.SetText("正在读取 GLOBAL 候选…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		current, choices, err := s.manager.GlobalChoices(ctx)
		s.app.QueueUpdateDraw(func() {
			s.globalLoading = false
			if err != nil {
				s.notice.SetText("读取 GLOBAL 候选失败: " + err.Error())
				s.failure(fmt.Errorf("读取 GLOBAL 候选失败: %w", err))
				return
			}
			// 读取期间已打开其他页面或开始操作时不再弹出列表。
			if page, _ := s.pages.GetFrontPage(); page != "main" || s.busy {
				return
			}
			s.notice.SetText(fmt.Sprintf("GLOBAL 共 %d 个可选出口，当前: %s", len(choices), current))
			s.showGlobalChoices(current, choices)
		})
	}()
}

func (s *screen) showGlobalChoices(current string, choices []core.ProxyChoice) {
	list := tview.NewList().ShowSecondaryText(false).SetHighlightFullLine(true)
	list.SetSelectedStyle(controlFocusStyle())
	selected := 0
	for i, choice := range choices {
		marker := "  "
		if choice.Name == current {
			marker, selected = "● ", i
		}
		label := tview.Escape(choice.Name)
		if choice.Name == s.last.Chain.Proxy {
			label += "（链式前置：" + tview.Escape(s.last.Chain.Name) + "）"
		}
		detail := choice.Type
		if choice.Now != "" {
			detail += " → " + choice.Now
		}
		if detail != "" {
			label += "  [gray]" + tview.Escape(detail) + "[-]"
		}
		list.AddItem(marker+label, "", 0, nil)
	}
	list.SetCurrentItem(selected)
	list.SetSelectedFunc(func(i int, _, _ string, _ rune) {
		name := choices[i].Name
		if name == current {
			s.close()
			return
		}
		s.work("GLOBAL 已切换到 "+name, func(ctx context.Context) error {
			return s.manager.SelectGlobal(ctx, name)
		})
	})
	list.SetDoneFunc(s.close)
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		// Tab 不在列表中移动焦点，避免与其他对话框的 Tab 语义混淆。
		if event.Key() == tcell.KeyTab || event.Key() == tcell.KeyBacktab {
			return nil
		}
		return event
	})
	list.SetBorder(true).SetTitle(fmt.Sprintf(" 切换 GLOBAL 出口 · %d 项 · [Enter 切换] · [Esc 返回] ", len(choices)))
	s.show(list)
	s.footer.SetText("[yellow]" + shortcutHint("↑↓/PgUp/PgDn", "选择") + "[-]  [yellow]" + shortcutHint("Enter", "切换") + "[-]  [yellow]" + shortcutHint("Esc", "返回") + "[-]")
}
