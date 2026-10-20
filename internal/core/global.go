package core

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
)

const globalGroup = "GLOBAL"

// GLOBAL 候选中的兼容占位和 PASS 不能作为实际出口。
var hiddenGlobalChoices = map[string]bool{"COMPATIBLE": true, "PASS": true}

type ProxyChoice struct {
	Name, Type, Now string
}

func (c *Controller) Proxies(ctx context.Context) (map[string]ProxyInfo, error) {
	var result struct {
		Proxies map[string]ProxyInfo `json:"proxies"`
	}
	if err := c.Do(ctx, http.MethodGet, "/proxies", nil, &result); err != nil {
		return nil, err
	}
	return result.Proxies, nil
}

// 控制接口地址以已提交的运行配置为准，避免使用尚未应用的草稿。
func (m *Manager) runtimeController() (*Controller, error) {
	raw, err := os.ReadFile(m.Store.Path("config.yaml"))
	if err != nil {
		return nil, err
	}
	d, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	return NewController(d)
}

// 候选及顺序完全来自内核 GLOBAL，不重新生成或排序。
func (m *Manager) GlobalChoices(ctx context.Context) (string, []ProxyChoice, error) {
	c, err := m.runtimeController()
	if err != nil {
		return "", nil, err
	}
	var info RuntimeInfo
	if err = c.Do(ctx, http.MethodGet, "/configs", nil, &info); err != nil {
		return "", nil, err
	}
	if info.Mode != "global" {
		return "", nil, fmt.Errorf("仅全局模式可切换 GLOBAL 出口，当前为 %s 模式", info.Mode)
	}
	proxies, err := c.Proxies(ctx)
	if err != nil {
		return "", nil, err
	}
	global, ok := proxies[globalGroup]
	if !ok {
		return "", nil, fmt.Errorf("内核未返回 GLOBAL")
	}
	choices := make([]ProxyChoice, 0, len(global.All))
	for _, name := range global.All {
		if hiddenGlobalChoices[name] {
			continue
		}
		p := proxies[name]
		choices = append(choices, ProxyChoice{Name: name, Type: p.Type, Now: p.Now})
	}
	if len(choices) == 0 {
		return "", nil, fmt.Errorf("GLOBAL 没有可选出口")
	}
	return global.Now, choices, nil
}

// 选择由内核原生记忆；切换后回读确认实际生效。
func (m *Manager) SelectGlobal(ctx context.Context, name string) error {
	return m.withOperation(func() error {
		c, err := m.runtimeController()
		if err != nil {
			return err
		}
		g, err := c.Global(ctx)
		if err != nil {
			return err
		}
		if hiddenGlobalChoices[name] || !contains(g.All, name) {
			return fmt.Errorf("%s 不在 GLOBAL 候选中，请刷新后重试", name)
		}
		if err = c.Do(ctx, http.MethodPut, "/proxies/"+url.PathEscape(globalGroup), map[string]string{"name": name}, nil); err != nil {
			return fmt.Errorf("切换 GLOBAL 失败: %w", err)
		}
		if g, err = c.Global(ctx); err != nil {
			return err
		}
		if g.Now != name {
			return fmt.Errorf("切换后 GLOBAL 实际为 %s", g.Now)
		}
		return nil
	})
}
