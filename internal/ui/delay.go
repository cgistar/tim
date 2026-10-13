package ui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type nodeDelay struct {
	proxy      string
	label      string
	value      string
	cancel     context.CancelFunc
	serial     uint64
	start, end int
}

func (d *nodeDelay) reset() {
	if d.cancel != nil {
		d.cancel()
	}
	d.serial++
	d.proxy, d.label, d.value = "", "", ""
	d.cancel = nil
	d.start, d.end = 0, 0
}

func (s *screen) resetDelays() {
	s.exitDelay.reset()
	s.frontDelay.reset()
}

func (s *screen) delayNode(d *nodeDelay, prefix, proxy, label string) string {
	if !s.last.Running {
		proxy = ""
	}
	if d.proxy != proxy {
		d.reset()
		d.proxy = proxy
	}
	d.label = label
	text := prefix + tview.Escape(label)
	d.start, d.end = 0, 0
	if proxy == "" {
		return text
	}
	value := d.value
	if value == "" {
		value = "⚡"
	}
	d.start = tview.TaggedStringWidth(text) + 1
	d.end = d.start + tview.TaggedStringWidth(value)
	return text + " [yellow]" + value + "[-]"
}

func (s *screen) delayMouse(event *tcell.EventMouse, action tview.MouseAction) bool {
	page, _ := s.pages.GetFrontPage()
	if page != "main" || !s.last.Running || (action != tview.MouseLeftClick && action != tview.MouseLeftDoubleClick && action != tview.MouseLeftDown) {
		return false
	}
	x, y := event.Position()
	left, top, width, height := s.status.GetInnerRect()
	if y != top+1 || height < 2 || x < left || x >= left+width {
		return false
	}
	for _, d := range []*nodeDelay{&s.exitDelay, &s.frontDelay} {
		if d.proxy != "" && x-left >= d.start && x-left < d.end {
			// 消费按下避免 TextView 抢焦点；抬起必须继续分发，tview 才会生成 click。
			if action == tview.MouseLeftClick || action == tview.MouseLeftDoubleClick {
				s.testDelay(d)
			}
			return true
		}
	}
	return false
}

func (s *screen) testDelay(d *nodeDelay) {
	if d.cancel != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	d.cancel = cancel
	d.serial++
	serial, proxy, label := d.serial, d.proxy, d.label
	d.value = "…"
	s.renderStatus()
	go func() {
		defer cancel()
		delay, err := s.manager.ProxyDelay(ctx, proxy)
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		s.app.QueueUpdateDraw(func() {
			if d.serial != serial || d.proxy != proxy || !s.last.Running {
				return
			}
			d.cancel = nil
			if err != nil {
				d.value = ""
				reason := err.Error()
				if errors.Is(err, context.DeadlineExceeded) {
					reason = "测速超时（5 秒）"
				}
				message := fmt.Sprintf("节点 %s 测速失败：%s", label, reason)
				s.notice.SetText(message)
				if logErr := appendErrorLog(s.errorLogPath, message); logErr != nil {
					s.notice.SetText(message + "\n日志写入失败: " + logErr.Error())
				}
			} else {
				d.value = strconv.Itoa(delay)
			}
			s.renderStatus()
		})
	}()
}
