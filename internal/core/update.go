package core

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}
type Release struct {
	Tag        string  `json:"tag_name"`
	Prerelease bool    `json:"prerelease"`
	Draft      bool    `json:"draft"`
	Assets     []Asset `json:"assets"`
}

const releasesURL = "https://github.com/MetaCubeX/mihomo/releases/latest"

func selectAsset(r Release, arch string) (Asset, error) {
	if r.Draft || r.Prerelease || !strings.HasPrefix(r.Tag, "v") || strings.ContainsAny(r.Tag, "/\\") {
		return Asset{}, fmt.Errorf("不是有效稳定版发布")
	}
	variant := ""
	switch arch {
	case "amd64":
		variant = "amd64-compatible"
	case "arm64":
		variant = "arm64"
	default:
		return Asset{}, fmt.Errorf("自动更新暂支持 Linux amd64/arm64，当前 %s", arch)
	}
	name := "mihomo-linux-" + variant + "-" + r.Tag + ".gz"
	for _, a := range r.Assets {
		if a.Name == name {
			if !strings.HasPrefix(a.Digest, "sha256:") || len(a.Digest) != 71 {
				return Asset{}, fmt.Errorf("官方资产没有有效 SHA-256 摘要，拒绝更新")
			}
			if _, err := hex.DecodeString(a.Digest[7:]); err != nil {
				return Asset{}, err
			}
			u, err := url.Parse(a.URL)
			if err != nil || u.Scheme != "https" || u.Host != "github.com" || !strings.HasPrefix(u.Path, "/MetaCubeX/mihomo/releases/download/") {
				return Asset{}, fmt.Errorf("发布资产不是官方 GitHub 下载地址")
			}
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("稳定版缺少 %s", name)
}
func (m *Manager) Latest(ctx context.Context) (Release, error) {
	client, err := m.ProxyClient(ctx)
	if err != nil {
		return Release{}, err
	}
	defer client.CloseIdleConnections()
	return latestRelease(ctx, client)
}

var stableTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
var assetRow = regexp.MustCompile(`(?s)<li\b[^>]*>.*?</li>`)
var assetLink = regexp.MustCompile(`href="(/MetaCubeX/mihomo/releases/download/[^"]+)"`)
var assetDigest = regexp.MustCompile(`sha256:[0-9a-f]{64}`)

func githubGet(ctx context.Context, client *http.Client, address string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "tim")
	res, err := client.Do(req)
	if err != nil {
		return nil, safeNetworkError(err)
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("GitHub 官方发布页面 HTTP %d", res.StatusCode)
	}
	return res, nil
}
func latestRelease(ctx context.Context, client *http.Client) (Release, error) {
	res, err := githubGet(ctx, client, releasesURL)
	if err != nil {
		return Release{}, err
	}
	defer res.Body.Close()
	u := res.Request.URL
	tag := strings.TrimPrefix(u.Path, "/MetaCubeX/mihomo/releases/tag/")
	if u.Scheme != "https" || u.Host != "github.com" || !stableTag.MatchString(tag) {
		return Release{}, fmt.Errorf("GitHub latest 未指向有效的官方稳定版本")
	}
	return Release{Tag: tag}, nil
}
func releaseAssets(ctx context.Context, client *http.Client, release Release) (Release, error) {
	if !stableTag.MatchString(release.Tag) {
		return release, fmt.Errorf("无效发布标签")
	}
	res, err := githubGet(ctx, client, "https://github.com/MetaCubeX/mihomo/releases/expanded_assets/"+release.Tag)
	if err != nil {
		return release, err
	}
	defer res.Body.Close()
	raw, err := readBounded(res.Body, 4<<20)
	if err != nil {
		return release, err
	}
	for _, row := range assetRow.FindAllString(string(raw), -1) {
		link := assetLink.FindStringSubmatch(row)
		if len(link) != 2 {
			continue
		}
		prefix := "/MetaCubeX/mihomo/releases/download/" + release.Tag + "/"
		if !strings.HasPrefix(link[1], prefix) {
			continue
		}
		name := strings.TrimPrefix(link[1], prefix)
		if strings.ContainsAny(name, "/\\") {
			continue
		}
		release.Assets = append(release.Assets, Asset{Name: name, URL: "https://github.com" + link[1], Digest: assetDigest.FindString(row)})
	}
	if len(release.Assets) == 0 {
		return release, fmt.Errorf("官方发布页面没有可识别资产")
	}
	return release, nil
}
func (m *Manager) Update(ctx context.Context) error {
	return m.locked(func() error {
		client, err := m.ProxyClient(ctx)
		if err != nil {
			return err
		}
		defer client.CloseIdleConnections()
		return m.updateWithClient(ctx, client)
	})
}

// 首次安装不能依赖尚未存在的代理内核，明确使用当前网络下载。
func (m *Manager) InstallKernel(ctx context.Context) error {
	return m.locked(func() error {
		missing, err := m.Store.KernelMissing()
		if err != nil {
			return err
		}
		if !missing {
			return fmt.Errorf("配置的内核已存在，请使用内核更新")
		}
		return m.updateWithClient(ctx, m.HTTP)
	})
}
func (m *Manager) updateWithClient(ctx context.Context, client *http.Client) error {
	r, err := latestRelease(ctx, client)
	if err != nil {
		return err
	}
	r, err = releaseAssets(ctx, client, r)
	if err != nil {
		return err
	}
	a, err := selectAsset(r, runtime.GOARCH)
	if err != nil {
		return err
	}
	path, err := m.downloadKernel(ctx, r, a, client)
	if err != nil {
		return err
	}
	// 更新以已应用配置为准，不顺带应用尚未确认的订阅刷新或手工修改。
	active, err := m.Store.Active()
	if err != nil {
		return err
	}
	settings, err := m.Store.Settings()
	if err != nil {
		return err
	}
	settings.MihomoPath = path
	sb, err := settings.Bytes()
	if err != nil {
		return err
	}
	if active.Runtime == "" {
		return AtomicWrite(m.Store.Path("setting.yaml"), sb, 0600)
	}
	b, err := os.ReadFile(active.Runtime)
	if err != nil {
		return err
	}
	cfg, err := Parse(b)
	if err != nil {
		return err
	}
	if err = m.validate(ctx, path, cfg); err != nil {
		return err
	}
	old, live, err := m.process()
	if err != nil {
		return err
	}
	if live {
		if err = m.stop(ctx); err != nil {
			return err
		}
		if err = m.launch(ctx, path, active.Runtime); err != nil {
			return m.restoreKernel(old, err)
		}
	}
	if err = commitFiles(m.Store, []fileWrite{{"setting.yaml", sb}}); err != nil {
		if live {
			return m.restoreKernel(old, err)
		}
		return err
	}
	return nil
}
func (m *Manager) restoreKernel(old Process, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := m.stop(ctx); err != nil {
		return fmt.Errorf("更新失败: %w；停止新版本失败: %v", cause, err)
	}
	if err := m.launch(ctx, old.Binary, m.Store.Path("config.yaml")); err != nil {
		return fmt.Errorf("更新失败: %w；旧版本恢复失败: %v", cause, err)
	}
	return fmt.Errorf("更新失败，已恢复旧内核: %w", cause)
}
func (m *Manager) downloadKernel(ctx context.Context, r Release, a Asset, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", a.URL, nil)
	if err != nil {
		return "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", safeNetworkError(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("内核下载 HTTP %d", res.StatusCode)
	}
	dir, err := os.MkdirTemp(m.Store.Path("kernels"), r.Tag+"-")
	if err != nil {
		return "", err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(dir)
		}
	}()
	compressed, err := os.CreateTemp(dir, "download-*.gz")
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(compressed, hash), io.LimitReader(res.Body, (80<<20)+1))
	closeErr := compressed.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if n > 80<<20 || (a.Size > 0 && n != a.Size) || (res.ContentLength >= 0 && n != res.ContentLength) {
		return "", fmt.Errorf("下载文件大小不符")
	}
	if hex.EncodeToString(hash.Sum(nil)) != a.Digest[7:] {
		return "", fmt.Errorf("内核 SHA-256 校验失败")
	}
	f, err := os.Open(compressed.Name())
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	path := filepath.Join(dir, "mihomo")
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return "", err
	}
	n, err = io.Copy(out, io.LimitReader(gz, (200<<20)+1))
	closeErr = out.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if n > 200<<20 {
		return "", fmt.Errorf("解压内核超过大小限制")
	}
	version, err := Version(ctx, path)
	if err != nil {
		return "", err
	}
	if !contains(strings.Fields(version), r.Tag) {
		return "", fmt.Errorf("内核报告版本与发布标签不符: %s", version)
	}
	if err = os.Remove(compressed.Name()); err != nil {
		return "", err
	}
	success = true
	return path, nil
}
