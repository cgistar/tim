package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// 转为表单已有的焦点移动事件，保留输入框的光标操作和原完成回调。
func formArrowNavigation(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyUp:
		return tcell.NewEventKey(tcell.KeyBacktab, 0, event.Modifiers())
	case tcell.KeyDown:
		return tcell.NewEventKey(tcell.KeyTab, 0, event.Modifiers())
	}
	return event
}

func configureForm(form *tview.Form) {
	// 前景与背景都使用终端默认值，避免复选框每次绘制时交换颜色产生反色块。
	form.SetFieldStyle(tcell.StyleDefault).
		SetButtonStyle(tcell.StyleDefault).
		SetButtonActivatedStyle(controlFocusStyle())
	for i := 0; i < form.GetFormItemCount(); i++ {
		switch item := form.GetFormItem(i).(type) {
		case *tview.Checkbox:
			item.SetCheckedString("(*)").SetUncheckedString("( )")
			item.SetActivatedStyle(tcell.StyleDefault.Bold(true).Underline(true))
			item.SetInputCapture(formArrowNavigation)
		case *tview.InputField:
			item.SetPlaceholderStyle(tcell.StyleDefault.Foreground(tcell.ColorGray))
			item.SetInputCapture(formArrowNavigation)
		case *tview.DropDown:
			item.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
				if !item.IsOpen() {
					return formArrowNavigation(event)
				}
				return event
			})
			item.SetTextOptions(" ", " ", "[ ", " ▾ ]", "[ 请选择 ▾ ]")
			item.SetFocusedStyle(controlFocusStyle())
			// 弹出列表覆盖后续字段，必须用实色背景清除底层文字。
			item.SetListStyles(
				tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorDarkSlateGray),
				tcell.StyleDefault.Foreground(tcell.ColorBlack).Background(tcell.ColorYellow).Bold(true),
			)
		}
	}
	for i := 0; i < form.GetButtonCount(); i++ {
		form.GetButton(i).SetInputCapture(formArrowNavigation)
	}
}

func controlFocusStyle() tcell.Style {
	return tcell.StyleDefault.Foreground(tcell.ColorYellow).Bold(true).Underline(true)
}

func configureModal(modal *tview.Modal) {
	modal.Box.SetBackgroundColor(tcell.ColorDefault)
	modal.SetBackgroundColor(tcell.ColorDefault).
		SetButtonStyle(tcell.StyleDefault).
		SetButtonActivatedStyle(controlFocusStyle())
}

func (s *screen) confirmSettings(form *tview.Form, page, message string, apply func()) {
	modal := tview.NewModal().SetText(message + "\n确认：应用配置；取消：放弃未保存修改并返回。").AddButtons([]string{"确认", "取消"})
	configureModal(modal)
	modal.SetDoneFunc(func(index int, _ string) {
		s.pages.RemovePage(page)
		s.app.SetFocus(form)
		if index == 0 {
			apply()
			return
		}
		s.close()
	})
	s.pages.AddPage(page, modal, true, true)
	s.app.SetFocus(modal)
}

func shortcutHint(key, action string) string {
	return tview.Escape("[" + key + " " + action + "]")
}
