package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RootPasswordPrompt 只在 sudo 要求密码时于界面线程外调用。
type RootPasswordPrompt func(ctx context.Context) (string, error)

type RootSession struct {
	mu         sync.Mutex
	prompt     RootPasswordPrompt
	configDir  string
	cmd        *exec.Cmd
	input      io.WriteCloser
	output     *json.Decoder
	outputPipe io.Closer
	done       chan error
	closed     bool
	lifetime   context.Context
	cancel     context.CancelFunc
}

type rootRequest struct {
	Cancel  bool
	URL     string
	Method  string
	Header  http.Header
	Backend string
	Action  string
	DataDir string
	Process Process
	Config  []byte
	Info    RuntimeInfo
	Args    []string
}
type rootReply struct {
	StatusCode int
	Header     http.Header
	Guard      GuardStatus
	Spec       *guardSpec
	Error      string
	Process    Process
	Live       bool
	Output     []byte
}

func (m *Manager) EnableRootSession(prompt RootPasswordPrompt) *RootSession {
	lifetime, cancel := context.WithCancel(context.Background())
	s := &RootSession{prompt: prompt, configDir: m.Store.ConfigDir, lifetime: lifetime, cancel: cancel}
	if os.Geteuid() != 0 {
		m.root = s
		m.guard.remote = m
	}
	return s
}

func (s *RootSession) Close() error {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.closeLocked()
}
func (s *RootSession) closeLocked() error {
	if s.input == nil {
		return nil
	}
	_ = s.input.Close()
	if s.outputPipe != nil {
		_ = s.outputPipe.Close()
		s.outputPipe = nil
	}
	s.input = nil
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
	}
	s.cmd = nil
	s.output = nil
	return nil
}

func (s *RootSession) start(ctx context.Context) error {
	if s.closed {
		return fmt.Errorf("root 授权会话已结束")
	}
	if s.cmd != nil {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	// -k 与命令一起使用，不读写 sudo 时间戳；权限仅由这条私有管道持有。
	cmd := exec.Command("sudo", "-k", "-S", "-p", "TIM_SUDO_PASSWORD\n", "--", executable, "--root-helper", s.configDir)
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = input.Close()
		_ = output.Close()
		return err
	}
	if err = cmd.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		_ = stderr.Close()
		return err
	}
	done := make(chan error, 1)
	s.cmd, s.input, s.output, s.done = cmd, input, json.NewDecoder(output), done
	s.outputPipe = output
	authCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	promptDone := make(chan error, 1)
	go func() {
		// sudo 的提示与密码共用 stdin，但助手握手前不会发送 RPC。
		var line strings.Builder
		buf := make([]byte, 1)
		for {
			_, e := stderr.Read(buf)
			if e != nil {
				promptDone <- nil
				return
			}
			if buf[0] != '\n' {
				if line.Len() < 4096 {
					line.WriteByte(buf[0])
				}
				continue
			}
			value := line.String()
			line.Reset()
			if strings.Contains(value, "TIM_SUDO_PASSWORD") {
				password, e := s.prompt(authCtx)
				if e != nil {
					promptDone <- e
					_ = input.Close()
					return
				}
				if strings.ContainsAny(password, "\r\n\x00") {
					promptDone <- fmt.Errorf("密码不能包含换行或 NUL")
					_ = input.Close()
					return
				}
				_, e = io.WriteString(input, password+"\n")
				password = ""
				if e != nil {
					promptDone <- e
					return
				}
			}
		}
	}()
	ready := make(chan error, 1)
	decoder := s.output
	go func() {
		var reply rootReply
		e := decoder.Decode(&reply)
		if e == nil && reply.Error != "" {
			e = errors.New(reply.Error)
		}
		ready <- e
	}()
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-ready:
		if err != nil {
			err = fmt.Errorf("sudo 授权失败（请确认当前用户拥有 sudo 权限）: %w", err)
		}
	case err = <-promptDone:
		if err == nil {
			err = fmt.Errorf("sudo 授权未完成")
		}
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		_ = s.closeLocked()
	}
	return err
}

func (s *RootSession) call(ctx context.Context, request rootRequest) (rootReply, error) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.lifetime, cancel)
	defer stop()
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	var reply rootReply
	if err := ctx.Err(); err != nil {
		return reply, err
	}
	if err := s.start(ctx); err != nil {
		return reply, err
	}

	input, decoder := s.input, s.output
	if err := json.NewEncoder(input).Encode(request); err != nil {
		_ = s.closeLocked()
		return reply, fmt.Errorf("root 助手连接已结束，下次操作将重新授权: %w", err)
	}
	result := make(chan error, 1)
	go func() { result <- decoder.Decode(&reply) }()
	select {
	case err := <-result:
		if err != nil {
			_ = s.closeLocked()
			return rootReply{}, fmt.Errorf("root 助手连接已结束，下次操作将重新授权: %w", err)
		}
		if reply.Error != "" {
			return reply, errors.New(reply.Error)
		}
		return reply, nil
	case <-ctx.Done():
		// 取消当前操作并收完对应回复，保留已经授权的助手和协议顺序。
		_ = json.NewEncoder(input).Encode(rootRequest{Cancel: true})
		select {
		case <-result:
			return rootReply{}, ctx.Err()
		case <-time.After(5 * time.Second):
			_ = s.closeLocked()
			return rootReply{}, fmt.Errorf("root 助手未响应取消，下次操作将重新授权: %w", ctx.Err())
		}
	}

}

// 助手仅暴露 tim 所需的固定操作，不提供 shell 或任意命令执行接口。
func RunRootHelper(configDir string, input io.Reader, output io.Writer) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("root 助手需要 sudo 启动")
	}
	if !filepath.IsAbs(configDir) {
		return fmt.Errorf("配置目录必须为绝对路径")
	}
	encoder := json.NewEncoder(output)
	if err := encoder.Encode(rootReply{}); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type operation struct {
		req    rootRequest
		ctx    context.Context
		cancel context.CancelFunc
	}
	requests := make(chan operation)
	go func() {
		defer cancel()
		defer close(requests)
		decoder := json.NewDecoder(input)
		var active context.CancelFunc
		defer func() {
			if active != nil {
				active()
			}
		}()
		for {
			var req rootRequest
			if decoder.Decode(&req) != nil {
				return
			}
			if req.Cancel {
				if active != nil {
					active()
				}
				continue
			}
			if active != nil {
				active()
			}
			opCtx, stop := context.WithTimeout(ctx, 4*time.Minute)
			active = stop
			select {
			case requests <- operation{req, opCtx, stop}:
			case <-ctx.Done():
				stop()
				return
			}
		}
	}()
	for op := range requests {
		reply, err := runRootRequest(op.ctx, configDir, op.req)
		op.cancel()
		if err != nil {
			reply.Error = err.Error()
		}
		if err = encoder.Encode(reply); err != nil {
			return err
		}
	}

	return nil
}

func runRootRequest(ctx context.Context, configDir string, req rootRequest) (rootReply, error) {
	var reply rootReply
	if !filepath.IsAbs(req.DataDir) {
		return reply, fmt.Errorf("data 目录必须为绝对路径")
	}
	// 不调用 Open，避免助手初始化或改写普通用户管理的配置文件。
	store := &Store{ConfigDir: configDir, DataOverride: req.DataDir}
	store.dataDir.Store(filepath.Clean(req.DataDir))
	m := NewManager(store)
	switch req.Action {
	case "process":
		p, live, err := m.process()
		reply.Process, reply.Live = p, live
		return reply, err
	case "verify", "matches", "signal":
		p, live, err := m.process()
		if err != nil {
			return reply, err
		}
		if !live || p != req.Process {
			if req.Action == "matches" {
				return reply, nil
			}
			return reply, fmt.Errorf("目标进程身份已改变，拒绝操作")
		}
		switch req.Action {
		case "matches":
			reply.Live, err = matchesProcess(p)
		case "signal":
			err = signalManaged(p)
		case "verify":
			var d *Document
			d, err = Parse(req.Config)
			if err == nil {
				err = verifyRuntime(p, d, req.Info)
			}
		}
		return reply, err
	case "http-get":
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			return reply, fmt.Errorf("后台下载只支持 GET/HEAD")
		}
		request, err := http.NewRequestWithContext(ctx, req.Method, req.URL, nil)
		if err != nil {
			return reply, err
		}
		if request.URL.Scheme != "http" && request.URL.Scheme != "https" {
			return reply, fmt.Errorf("下载只支持 HTTP/HTTPS")
		}
		request.Header = req.Header
		response, err := (guardTransport{manager: m}).RoundTrip(request)
		if err != nil {
			return reply, err
		}
		defer response.Body.Close()
		reply.StatusCode, reply.Header = response.StatusCode, response.Header
		const maxDownload = 80 << 20
		reply.Output, err = io.ReadAll(io.LimitReader(response.Body, maxDownload+1))
		if len(reply.Output) > maxDownload {
			return rootReply{}, fmt.Errorf("后台下载超过 80 MiB")
		}
		return reply, err

	case "guard-discover":
		var err error
		reply.Spec, err = m.guard.discover(ctx)
		return reply, err
	case "guard-select":
		var err error
		reply.Spec, err = m.guard.selectBackend(ctx)
		return reply, err
	case "guard-status":
		var err error
		reply.Guard, err = m.NetworkGuardStatus(ctx)
		return reply, err
	case "guard-apply":
		unlock, err := lockFile(m.guard.lockPath)
		if err != nil {
			return reply, err
		}
		defer unlock()
		spec, err := m.guard.discover(ctx)
		if err != nil {
			return reply, err
		}
		if spec == nil {
			spec, err = m.guard.selectBackend(ctx)
			if err != nil {
				return reply, err
			}
		}
		if spec.Backend != req.Backend {
			return reply, fmt.Errorf("防火墙后端已改变，请重试")
		}
		return reply, m.guard.apply(ctx, spec)
	case "guard-disable":
		return reply, m.DisableNetworkGuard(ctx)
	case "launch":
		binary, err := store.Kernel()
		if err != nil {
			return reply, err
		}
		if _, live, err := m.process(); err != nil {
			return reply, err
		} else if live {
			return reply, fmt.Errorf("内核已运行")
		}
		return reply, m.launch(ctx, binary, store.Path("config.yaml"))

	case "service-command":
		if len(req.Args) == 0 {
			return reply, fmt.Errorf("缺少服务操作")
		}
		switch req.Args[0] {
		case "start", "stop", "enable", "disable":
			if len(req.Args) != 2 || req.Args[1] != m.serviceName() {
				return reply, fmt.Errorf("服务操作范围无效")
			}
			if raw, _, err := m.serviceFile(); err != nil || raw == nil {
				if err == nil {
					err = fmt.Errorf("服务未安装")
				}
				return reply, err
			}
		case "daemon-reload", "--version":
			if len(req.Args) != 1 {
				return reply, fmt.Errorf("服务参数无效")
			}
		default:
			return reply, fmt.Errorf("不支持的后台服务操作")
		}
		var err error
		reply.Output, err = m.serviceCommand(ctx, req.Args...)
		return reply, err
	case "service-install":
		return reply, m.InstallService(ctx)
	case "service-uninstall":
		return reply, m.UninstallService(ctx)
	case "service-sync":
		binary, err := store.Kernel()
		if err != nil {
			return reply, err
		}
		return reply, m.syncService(ctx, binary, store.Path("config.yaml"))
	default:
		return reply, fmt.Errorf("不支持的 root 助手操作 %q", req.Action)
	}
}

func (m *Manager) rootCall(ctx context.Context, req rootRequest) (rootReply, error) {
	req.DataDir = m.Store.DataDir()
	return m.root.call(ctx, req)
}
func (m *Manager) verifyProcess(ctx context.Context, p Process, d *Document, info RuntimeInfo) error {
	err := verifyRuntime(p, d, info)
	if m.root == nil || !errors.Is(err, os.ErrPermission) {
		return err
	}
	raw, err := d.Bytes()
	if err != nil {
		return err
	}
	_, err = m.rootCall(ctx, rootRequest{Action: "verify", Process: p, Config: raw, Info: info})
	return err
}
func (m *Manager) matchesManaged(ctx context.Context, p Process) (bool, error) {
	live, err := matchesProcess(p)
	if m.root == nil || !errors.Is(err, os.ErrPermission) {
		return live, err
	}
	reply, err := m.rootCall(ctx, rootRequest{Action: "matches", Process: p})
	return reply.Live, err
}
func (m *Manager) signalProcess(ctx context.Context, p Process) error {
	if m.root == nil {
		return signalManaged(p)
	}
	_, err := m.rootCall(ctx, rootRequest{Action: "signal", Process: p})
	return err
}

func (s *RootSession) authorized() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cmd != nil && !s.closed
}
