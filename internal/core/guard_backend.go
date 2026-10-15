package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var errGuardCollision = errors.New("同名断网保护资源已存在，拒绝接管")

func (g *networkGuard) nftExists(ctx context.Context, s *guardSpec) (bool, error) {
	raw, err := g.command(ctx, s, "nft", nil, "-j", "list", "tables")
	if err != nil {
		return false, err
	}
	var listing struct {
		NFTables []struct {
			Table struct{ Family, Name string } `json:"table"`
		} `json:"nftables"`
	}
	if err = json.Unmarshal(raw, &listing); err != nil {
		return false, err
	}
	if listing.NFTables == nil {
		return false, fmt.Errorf("nftables 表列表响应缺少 nftables 数组")
	}
	for _, item := range listing.NFTables {
		if item.Table.Family == "inet" && item.Table.Name == guardTable {
			return true, nil
		}
	}
	return false, nil
}

func (g *networkGuard) iptListing(ctx context.Context, s *guardSpec, ipv6 bool) (string, error) {
	name := "iptables"
	if ipv6 {
		name = "ip6tables"
	}
	raw, err := g.command(ctx, s, name, nil, "-w", "5", "-S")
	return string(raw), err
}

func guardIPTExists(listing string) bool {
	for _, line := range strings.Split(listing, "\n") {
		if line == "-N "+guardChain {
			return true
		}
	}
	return false
}

// 完全不存在视为关闭；任何残留链或入口都必须继续核验，不能掩盖半套规则。
func (g *networkGuard) exists(ctx context.Context, s *guardSpec) (bool, error) {
	if s.Backend == "nftables" {
		return g.nftExists(ctx, s)
	}
	exists := false
	for _, ipv6 := range []bool{false, true} {
		listing, err := g.iptListing(ctx, s, ipv6)
		if err != nil {
			return false, err
		}
		for _, line := range strings.Split(listing, "\n") {
			if line == "-N "+guardChain || strings.HasPrefix(line, "-A "+guardChain+" ") || strings.Contains(line, "-j "+guardChain) || strings.Contains(line, "-g "+guardChain) {
				exists = true
			}
		}
	}
	return exists, nil
}

func (g *networkGuard) preflight(ctx context.Context, s *guardSpec, fresh bool) error {
	if s.Backend == "nftables" {
		exists, err := g.nftExists(ctx, s)
		if err != nil {
			return err
		}
		if fresh && exists {
			return fmt.Errorf("%w: nftables %s", errGuardCollision, guardTable)
		}
		program, err := guardNFTProgram(exists)
		if err != nil {
			return err
		}
		_, err = g.command(ctx, s, "nft", program, "-c", "-j", "-f", "-")
		return err
	}
	for _, ipv6 := range []bool{false, true} {
		listing, err := g.iptListing(ctx, s, ipv6)
		if err != nil {
			return err
		}
		if fresh && (guardIPTExists(listing) || strings.Contains(listing, "-j "+guardChain)) {
			return fmt.Errorf("%w: iptables %s", errGuardCollision, guardChain)
		}
		name := "iptables-restore"
		if ipv6 {
			name = "ip6tables-restore"
		}
		if _, err = g.command(ctx, s, name, guardIPTProgram(ipv6), "--wait", "5", "--noflush", "--test"); err != nil {
			return err
		}
	}
	return nil
}

func (g *networkGuard) selectBackend(ctx context.Context) (*guardSpec, error) {
	if g.remote != nil {
		reply, err := g.remote.rootCall(ctx, rootRequest{Action: "guard-select"})
		return reply.Spec, err
	}
	var failures []error
	for _, backend := range []string{"nftables", "iptables"} {
		s := &guardSpec{Backend: backend, Programs: map[string]string{}}
		for _, name := range guardProgramNames(backend) {
			path, err := g.lookup(name)
			if err != nil {
				failures = append(failures, fmt.Errorf("缺少 %s: %w", name, err))
				break
			}
			path, err = filepath.Abs(path)
			if err != nil {
				return nil, err
			}
			s.Programs[name] = path
		}
		if len(s.Programs) != len(guardProgramNames(backend)) {
			continue
		}
		if err := g.preflight(ctx, s, true); err != nil {
			if errors.Is(err, errGuardCollision) {
				return nil, err
			}
			failures = append(failures, err)
			continue
		}
		return s, nil
	}
	return nil, fmt.Errorf("没有可用的双栈防火墙后端，请安装 nftables（Ubuntu/Debian: apt install nftables）: %w", errors.Join(failures...))
}

func (g *networkGuard) apply(ctx context.Context, s *guardSpec) error {
	if g.remote != nil {
		_, err := g.remote.rootCall(ctx, rootRequest{Action: "guard-apply", Backend: s.Backend})
		return err
	}
	if err := g.preflight(ctx, s, false); err != nil {
		return err
	}
	if s.Backend == "nftables" {
		exists, err := g.nftExists(ctx, s)
		if err != nil {
			return err
		}
		program, err := guardNFTProgram(exists)
		if err != nil {
			return err
		}
		if _, err = g.command(ctx, s, "nft", program, "-j", "-f", "-"); err != nil {
			return err
		}
	} else {
		for _, ipv6 := range []bool{false, true} {
			name, restore := "iptables", "iptables-restore"
			if ipv6 {
				name, restore = "ip6tables", "ip6tables-restore"
			}
			if _, err := g.command(ctx, s, restore, guardIPTProgram(ipv6), "--wait", "5", "--noflush"); err != nil {
				return err
			}
			listing, err := g.iptListing(ctx, s, ipv6)
			if err != nil {
				return err
			}
			// 先插入新入口，再删除旧入口，更新过程中不解除保护。
			if _, err = g.command(ctx, s, name, nil, "-w", "5", "-I", "OUTPUT", "1", "-j", guardChain); err != nil {
				return err
			}
			lines := strings.Split(strings.TrimSpace(listing), "\n")
			index := 0
			var duplicate []int
			for _, line := range lines {
				if strings.HasPrefix(line, "-A OUTPUT ") {
					index++
					if line == "-A OUTPUT -j "+guardChain {
						duplicate = append(duplicate, index+1)
					}
				}
			}
			for i := len(duplicate) - 1; i >= 0; i-- {
				if _, err = g.command(ctx, s, name, nil, "-w", "5", "-D", "OUTPUT", fmt.Sprint(duplicate[i])); err != nil {
					return err
				}
			}
		}
	}
	return g.verify(ctx, s)
}

func (g *networkGuard) verify(ctx context.Context, s *guardSpec) error {
	if s.Backend == "nftables" {
		raw, err := g.command(ctx, s, "nft", nil, "-j", "list", "table", "inet", guardTable)
		if err != nil {
			return err
		}
		return verifyGuardNFT(raw)
	}
	for _, ipv6 := range []bool{false, true} {
		listing, err := g.iptListing(ctx, s, ipv6)
		if err != nil {
			return err
		}
		var chain []string
		firstOutput := ""
		hooks := 0
		for _, line := range strings.Split(strings.TrimSpace(listing), "\n") {
			if strings.HasPrefix(line, "-A "+guardChain+" ") {
				chain = append(chain, line)
			}
			if strings.HasPrefix(line, "-A OUTPUT ") && firstOutput == "" {
				firstOutput = line
			}
			if line == "-A OUTPUT -j "+guardChain {
				hooks++
			}
		}
		if firstOutput != "-A OUTPUT -j "+guardChain || hooks != 1 {
			return fmt.Errorf("iptables OUTPUT 保护入口缺失、重复或不在首位")
		}
		if err = verifyGuardIPT([]byte(strings.Join(chain, "\n")), ipv6); err != nil {
			return err
		}
	}
	return nil
}

func (g *networkGuard) remove(ctx context.Context, s *guardSpec) error {
	if s.Backend == "nftables" {
		exists, err := g.nftExists(ctx, s)
		if err != nil || !exists {
			return err
		}
		_, err = g.command(ctx, s, "nft", nil, "delete", "table", "inet", guardTable)
		return err
	}
	for _, ipv6 := range []bool{false, true} {
		listing, err := g.iptListing(ctx, s, ipv6)
		if err != nil {
			return err
		}
		name := "iptables"
		if ipv6 {
			name = "ip6tables"
		}
		for _, line := range strings.Split(listing, "\n") {
			if line == "-A OUTPUT -j "+guardChain {
				if _, err = g.command(ctx, s, name, nil, "-w", "5", "-D", "OUTPUT", "-j", guardChain); err != nil {
					return err
				}
			}
		}
		if !guardIPTExists(listing) {
			continue
		}
		for _, op := range []string{"-F", "-X"} {
			if _, err = g.command(ctx, s, name, nil, "-w", "5", op, guardChain); err != nil {
				return err
			}
		}
	}
	return nil
}
