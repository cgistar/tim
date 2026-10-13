package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"

	"clisimplehub/tui/internal/core"
	"clisimplehub/tui/internal/ui"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "用法: tim\n配置固定为当前目录 setting.yaml；数据目录优先使用 DATA，其次使用 setting.yaml 的 data-dir。")
	}
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if runtime.GOOS != "linux" {
		fmt.Fprintln(os.Stderr, "tim 进程管理仅支持 Linux 5.3+")
		os.Exit(1)
	}
	s, err := core.Open(".")
	if err == nil {
		err = ui.Run(core.NewManager(s))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
