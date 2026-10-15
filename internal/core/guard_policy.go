package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
)

const (
	guardTable      = "tim_network_guard"
	guardChain      = "TIM_NETWORK_GUARD"
	guardDevice     = "tim-tun"
	guardMihomoMark = 0x74696d01
	guardTimMark    = 0x74696d02
)

var guardLAN4 = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16"}
var guardLAN6 = []string{"fc00::/7", "fe80::/10"}

type nftObject = map[string]any

func nftMatch(left, right any) nftObject {
	return nftObject{"match": nftObject{"op": "==", "left": left, "right": right}}
}
func nftMeta(key string) nftObject { return nftObject{"meta": nftObject{"key": key}} }
func nftPayload(protocol, field string) nftObject {
	return nftObject{"payload": nftObject{"protocol": protocol, "field": field}}
}

func guardNFTRules() []nftObject {
	var rules []nftObject
	add := func(label, verdict string, matches ...nftObject) {
		expressions := append(matches, nftObject{verdict: nil})
		rules = append(rules, nftObject{"family": "inet", "table": guardTable, "chain": "output", "expr": expressions, "comment": "tim:" + label})
	}
	add("loopback", "accept", nftMatch(nftMeta("oifname"), "lo"))
	add("mihomo", "accept", nftMatch(nftMeta("mark"), guardMihomoMark))
	add("tim", "accept", nftMatch(nftMeta("mark"), guardTimMark))
	add("tun", "accept", nftMatch(nftMeta("oifname"), guardDevice))
	// 必须在私网放行之前拦截，避免公共网络的网关 DNS 成为直连例外。
	for _, protocol := range []string{"tcp", "udp"} {
		for _, port := range []int{53, 853} {
			add(fmt.Sprintf("dns-%s-%d", protocol, port), "drop", nftMatch(nftPayload(protocol, "dport"), port))
		}
	}
	for _, family := range []struct {
		protocol string
		networks []string
	}{{"ip", guardLAN4}, {"ip6", guardLAN6}} {
		for _, network := range family.networks {
			prefix := netip.MustParsePrefix(network)
			add("lan-"+network, "accept", nftMatch(nftPayload(family.protocol, "daddr"), nftObject{"prefix": nftObject{"addr": prefix.Addr().String(), "len": prefix.Bits()}}))
		}
	}
	add("dhcp4", "accept", nftMatch(nftPayload("ip", "daddr"), "255.255.255.255"), nftMatch(nftPayload("udp", "sport"), 68), nftMatch(nftPayload("udp", "dport"), 67))
	add("dhcp6", "accept", nftMatch(nftPayload("ip6", "daddr"), "ff02::1:2"), nftMatch(nftPayload("udp", "sport"), 546), nftMatch(nftPayload("udp", "dport"), 547))
	for _, message := range []struct {
		kind int
		name string
	}{{130, "mld-listener-query"}, {131, "mld-listener-report"}, {132, "mld-listener-done"}, {133, "nd-router-solicit"}, {135, "nd-neighbor-solicit"}, {136, "nd-neighbor-advert"}, {143, "mld2-listener-report"}} {
		kind := message.kind
		hops := 255
		if kind == 130 || kind == 131 || kind == 132 || kind == 143 {
			hops = 1
		}
		add(fmt.Sprintf("icmp6-%d", kind), "accept", nftMatch(nftPayload("icmpv6", "type"), message.name), nftMatch(nftPayload("ip6", "hoplimit"), hops))
	}
	return rules
}

func guardNFTProgram(replace bool) ([]byte, error) {
	var commands []nftObject
	if replace {
		commands = append(commands, nftObject{"delete": nftObject{"table": nftObject{"family": "inet", "name": guardTable}}})
	}
	commands = append(commands,
		nftObject{"add": nftObject{"table": nftObject{"family": "inet", "name": guardTable}}},
		nftObject{"add": nftObject{"chain": nftObject{"family": "inet", "table": guardTable, "name": "output", "type": "filter", "hook": "output", "prio": 100, "policy": "drop"}}})
	for _, rule := range guardNFTRules() {
		commands = append(commands, nftObject{"add": nftObject{"rule": rule}})
	}
	return json.Marshal(nftObject{"nftables": commands})
}

// 逐条核对内核实际规则，不用表存在或配置开关冒充保护成功。
func verifyGuardNFT(raw []byte) error {
	var listing struct {
		NFTables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &listing); err != nil {
		return err
	}
	var rules []nftObject
	chains := 0
	for _, item := range listing.NFTables {
		if raw := item["chain"]; raw != nil {
			var chain struct {
				Name, Type, Hook, Policy string
				Prio                     int
			}
			if err := json.Unmarshal(raw, &chain); err != nil {
				return err
			}
			if chain.Name != "output" || chain.Type != "filter" || chain.Hook != "output" || chain.Policy != "drop" || chain.Prio != 100 {
				return fmt.Errorf("nftables 断网保护链属性不匹配")
			}
			chains++
		}
		if raw := item["rule"]; raw != nil {
			var rule nftObject
			if err := json.Unmarshal(raw, &rule); err != nil {
				return err
			}
			delete(rule, "handle")
			rules = append(rules, rule)
		}
	}
	expected, _ := json.Marshal(guardNFTRules())
	var canonical []nftObject
	_ = json.Unmarshal(expected, &canonical)
	if chains != 1 || !reflect.DeepEqual(rules, canonical) {
		return fmt.Errorf("nftables 断网保护规则缺失或已被修改")
	}
	return nil
}

func guardIPTRules(ipv6 bool) []string {
	rules := []string{
		"-o lo -j RETURN",
		fmt.Sprintf("-m mark --mark 0x%x -j RETURN", guardMihomoMark),
		fmt.Sprintf("-m mark --mark 0x%x -j RETURN", guardTimMark),
		"-o " + guardDevice + " -j RETURN",
	}
	for _, protocol := range []string{"tcp", "udp"} {
		for _, port := range []int{53, 853} {
			rules = append(rules, fmt.Sprintf("-p %s -m %s --dport %d -j DROP", protocol, protocol, port))
		}
	}
	networks := guardLAN4
	if ipv6 {
		networks = guardLAN6
	}
	for _, network := range networks {
		rules = append(rules, "-d "+network+" -j RETURN")
	}
	if !ipv6 {
		rules = append(rules, "-d 255.255.255.255/32 -p udp -m udp --sport 68 --dport 67 -j RETURN")
	} else {
		rules = append(rules, "-d ff02::1:2/128 -p udp -m udp --sport 546 --dport 547 -j RETURN")
		for _, kind := range []int{130, 131, 132, 133, 135, 136, 143} {
			hops := 255
			if kind == 130 || kind == 131 || kind == 132 || kind == 143 {
				hops = 1
			}
			rules = append(rules, fmt.Sprintf("-p ipv6-icmp -m icmp6 --icmpv6-type %d -m hl --hl-eq %d -j RETURN", kind, hops))
		}
	}
	return append(rules, "-j DROP")
}

func guardIPTProgram(ipv6 bool) []byte {
	var out bytes.Buffer
	fmt.Fprintf(&out, "*filter\n:%s - [0:0]\n-F %s\n", guardChain, guardChain)
	for _, rule := range guardIPTRules(ipv6) {
		fmt.Fprintf(&out, "-A %s %s\n", guardChain, rule)
	}
	out.WriteString("COMMIT\n")
	return out.Bytes()
}

func verifyGuardIPT(raw []byte, ipv6 bool) error {
	var actual []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.HasPrefix(line, "-N ") {
			continue
		}
		actual = append(actual, strings.TrimPrefix(line, "-A "+guardChain+" "))
	}
	if !reflect.DeepEqual(actual, guardIPTRules(ipv6)) {
		return fmt.Errorf("iptables 断网保护规则缺失或已被修改")
	}
	return nil
}
