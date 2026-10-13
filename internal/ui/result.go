package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// 复用 TextArea 的字素定位和拖选，只开放浏览与复制，屏蔽编辑和粘贴。
type resultView struct {
	*tview.TextArea
	screen tcell.Screen
}

func newResultView() *resultView {
	v := &resultView{TextArea: tview.NewTextArea()}
	v.SetSelectedStyle(controlFocusStyle())
	return v
}

func (v *resultView) SetText(text string) *resultView {
	v.TextArea.SetText(text, false)
	return v
}

func (v *resultView) GetText(_ bool) string { return v.TextArea.GetText() }

func (v *resultView) Draw(screen tcell.Screen) {
	v.screen = screen
	v.TextArea.Draw(screen)
}

func (v *resultView) copySelection() {
	if v.screen != nil && v.HasSelection() {
		text, _, _ := v.GetSelection()
		v.screen.SetClipboard([]byte(text))
	}
}

func (v *resultView) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return v.WrapInputHandler(func(event *tcell.EventKey, focus func(tview.Primitive)) {
		switch event.Key() {
		case tcell.KeyUp, tcell.KeyDown, tcell.KeyLeft, tcell.KeyRight,
			tcell.KeyHome, tcell.KeyEnd, tcell.KeyPgUp, tcell.KeyPgDn:
			v.TextArea.InputHandler()(event, focus)
		}
	})
}

func (v *resultView) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return func(action tview.MouseAction, event *tcell.EventMouse, focus func(tview.Primitive)) (bool, tview.Primitive) {
		if action == tview.MouseRightClick && v.InRect(event.Position()) {
			v.copySelection()
			return true, nil
		}
		consumed, capture := v.TextArea.MouseHandler()(action, event, func(tview.Primitive) { focus(v) })
		if capture == v.TextArea {
			capture = v
		}
		return consumed, capture
	}
}

func (v *resultView) PasteHandler() func(string, func(tview.Primitive)) { return nil }
