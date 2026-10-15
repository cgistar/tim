package ui

import (
	"context"
	"fmt"

	"github.com/rivo/tview"
)

func (s *screen) rootPassword(ctx context.Context) (string, error) {
	type result struct {
		value string
		err   error
	}
	answer := make(chan result, 1)
	var previousFocus tview.Primitive
	closePopup := func() {
		s.pages.RemovePage("root-authorization")
		if previousFocus != nil {
			s.app.SetFocus(previousFocus)
		}
	}
	go s.app.QueueUpdateDraw(func() {
		if ctx.Err() != nil {
			return
		}
		previousFocus = s.app.GetFocus()
		finish := func(value string, err error) {
			closePopup()
			select {
			case answer <- result{value, err}:
			default:
			}
		}
		cancel := func() { finish("", fmt.Errorf("已取消 sudo 密码输入，未执行需要权限的操作")) }
		field := tview.NewInputField().SetLabel("sudo 密码").SetMaskCharacter('*').SetFieldWidth(36)
		form := tview.NewForm().AddFormItem(field)
		form.AddButton("继续", func() {
			value := field.GetText()
			field.SetText("")
			if value == "" {
				finish("", fmt.Errorf("未输入 sudo 密码，未执行需要权限的操作"))
				return
			}
			finish(value, nil)
		})
		form.AddButton("取消", func() { field.SetText(""); cancel() })
		form.SetCancelFunc(func() { field.SetText(""); cancel() })
		form.SetBorder(true).SetTitle(" 输入当前用户的 sudo 密码 ")
		configureForm(form)
		popup := tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(tview.NewFlex().
				AddItem(nil, 0, 1, false).
				AddItem(form, 56, 0, true).
				AddItem(nil, 0, 1, false), 7, 0, true).
			AddItem(nil, 0, 1, false)
		s.pages.AddPage("root-authorization", popup, true, true)
		s.app.SetFocus(form)
	})
	select {
	case value := <-answer:
		return value.value, value.err
	case <-ctx.Done():
		go s.app.QueueUpdateDraw(func() { s.pages.RemovePage("root-authorization"); s.app.SetFocus(s.menu) })
		return "", ctx.Err()
	}
}
