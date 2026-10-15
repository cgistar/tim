//go:build linux

package core

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func verifyRuntime(p Process, d *Document, info RuntimeInfo) error {
	owned := map[string]bool{}
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", p.PID))
	if err != nil {
		return err
	}
	for _, f := range fds {
		v, e := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", p.PID, f.Name()))
		if e == nil && strings.HasPrefix(v, "socket:[") {
			owned[strings.TrimSuffix(strings.TrimPrefix(v, "socket:["), "]")] = true
		}
	}
	ports := map[string]bool{}
	for _, proto := range []string{"tcp", "tcp6", "udp", "udp6"} {
		f, e := os.Open(fmt.Sprintf("/proc/%d/net/%s", p.PID, proto))
		if e != nil {
			return e
		}
		scan := bufio.NewScanner(f)
		for scan.Scan() {
			fields := strings.Fields(scan.Text())
			if len(fields) < 10 || !owned[fields[9]] {
				continue
			}
			if strings.HasPrefix(proto, "tcp") && fields[3] != "0A" {
				continue
			}
			addr := strings.Split(fields[1], ":")
			if len(addr) != 2 {
				continue
			}
			port, e := strconv.ParseInt(addr[1], 16, 32)
			if e == nil {
				kind := "tcp"
				if strings.HasPrefix(proto, "udp") {
					kind = "udp"
				}
				ports[fmt.Sprintf("%s:%d", kind, port)] = true
			}
		}
		err = scan.Err()
		f.Close()
		if err != nil {
			return err
		}
	}
	for _, key := range []string{"mixed-port", "port", "socks-port"} {
		v := d.Text(key)
		if v != "" && v != "0" && !ports["tcp:"+v] {
			return fmt.Errorf("mihomo 未实际监听 %s=%s", key, v)
		}
	}
	if d.Text("dns", "enable") == "true" && d.Text("dns", "listen") != "" {
		addr := d.Text("dns", "listen")
		idx := strings.LastIndexByte(addr, ':')
		if idx >= 0 && !ports["udp:"+addr[idx+1:]] {
			return fmt.Errorf("mihomo 未实际监听 DNS %s", addr)
		}
	}
	if info.Tun.Enable {
		if expected := d.Text("tun", "device"); expected != "" && expected != info.Tun.Device {
			return fmt.Errorf("TUN 设备名不匹配：期望 %s，实际 %s", expected, info.Tun.Device)
		}
		if info.Tun.Device == "" {
			return fmt.Errorf("TUN 未返回实际设备名")
		}
		if _, err = os.Stat(filepath.Join("/sys/class/net", filepath.Base(info.Tun.Device))); err != nil {
			return fmt.Errorf("TUN 接口尚未建立: %w", err)
		}
	}
	return nil
}
