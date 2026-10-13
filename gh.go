package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// GH 是极简的 GitHub REST 客户端，用 Releases 资产做对象存储
type GH struct {
	token string
	repo  string // owner/name
	hc    *http.Client
}

type Asset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type Release struct {
	ID      int64   `json:"id"`
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return fmt.Sprintf("GitHub API %d: %s", e.Status, e.Message) }

func isNotFound(err error) bool {
	ae, ok := err.(*apiError)
	return ok && ae.Status == http.StatusNotFound
}

func NewGH(token, repo string) *GH {
	return &GH{token: token, repo: repo, hc: &http.Client{Timeout: 15 * time.Minute}}
}

// API 地址支持环境变量覆盖（测试用 mock 服务器）
func apiBase() string {
	if s := os.Getenv("GSSYNC_API_BASE"); s != "" {
		return strings.TrimRight(s, "/")
	}
	return "https://api.github.com"
}

func uploadBase() string {
	if s := os.Getenv("GSSYNC_UPLOAD_BASE"); s != "" {
		return strings.TrimRight(s, "/")
	}
	return "https://uploads.github.com"
}

func (g *GH) do(method, fullURL string, headers map[string]string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, fullURL, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "gssync")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return g.hc.Do(req)
}

func (g *GH) api(method, path string, body any, out any) error {
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	resp, err := g.do(method, apiBase()+path, map[string]string{"Content-Type": "application/json"}, data)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return handleResp(resp, out)
}

func handleResp(resp *http.Response, out any) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode >= 300 {
		msg := string(b)
		var ge struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(b, &ge) == nil && ge.Message != "" {
			msg = ge.Message
		}
		return &apiError{Status: resp.StatusCode, Message: msg}
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("解析响应失败: %w", err)
		}
	}
	return nil
}

func (g *GH) Login() (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := g.api("GET", "/user", nil, &u); err != nil {
		return "", err
	}
	return u.Login, nil
}

func (g *GH) repoExists() (bool, error) {
	resp, err := g.do("GET", apiBase()+"/repos/"+g.repo, nil, nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	b, _ := io.ReadAll(resp.Body)
	return false, &apiError{Status: resp.StatusCode, Message: string(b)}
}

func (g *GH) EnsureRepo() error {
	ok, err := g.repoExists()
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	parts := strings.SplitN(g.repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("仓库格式错误，应为 owner/name: %s", g.repo)
	}
	fmt.Println("创建私有仓库:", g.repo)
	return g.api("POST", "/user/repos", map[string]any{
		"name": parts[1], "private": true, "auto_init": true,
		"description": "gssync cloud saves",
	}, nil)
}

func (g *GH) GetRelease(tag string) (*Release, error) {
	var rel Release
	err := g.api("GET", "/repos/"+g.repo+"/releases/tags/"+url.PathEscape(tag), nil, &rel)
	if err != nil {
		return nil, err
	}
	return &rel, nil
}

// EnsureRelease 取得或创建滚动 Release（我们的存储桶）
func (g *GH) EnsureRelease(tag string) (*Release, error) {
	rel, err := g.GetRelease(tag)
	if err == nil {
		return rel, nil
	}
	if isNotFound(err) {
		var rel2 Release
		if err := g.api("POST", "/repos/"+g.repo+"/releases", map[string]any{
			"tag_name": tag, "name": "game saves", "body": "managed by gssync",
		}, &rel2); err != nil {
			return nil, fmt.Errorf("创建 Release 失败（检查 Token 权限）: %w", err)
		}
		return &rel2, nil
	}
	return nil, err
}

func (g *GH) DeleteAsset(id int64) error {
	return g.api("DELETE", fmt.Sprintf("/repos/%s/releases/assets/%d", g.repo, id), nil, nil)
}

// UploadAsset 上传资产，带 3 次重试（网络错误/5xx/429）
func (g *GH) UploadAsset(relID int64, name string, data []byte) error {
	u := fmt.Sprintf("%s/repos/%s/releases/%d/assets?name=%s", uploadBase(), g.repo, relID, url.QueryEscape(name))
	var lastErr error
	for i := 0; i < 3; i++ {
		resp, err := g.do("POST", u, map[string]string{"Content-Type": "application/octet-stream"}, data)
		if err != nil {
			lastErr = err
		} else {
			err = handleResp(resp, nil)
			if err == nil {
				return nil
			}
			lastErr = err
			if ae, ok := err.(*apiError); ok && ae.Status < 500 && ae.Status != http.StatusTooManyRequests {
				return err
			}
		}
		time.Sleep(time.Duration(2*(i+1)) * time.Second)
	}
	return fmt.Errorf("上传资产 %s 失败: %w", name, lastErr)
}

// ReplaceAsset 删除同名旧资产后上传新资产（Releases 无原地替换）
func (g *GH) ReplaceAsset(rel *Release, name string, data []byte) error {
	if old := g.AssetByName(rel, name); old != nil {
		if err := g.DeleteAsset(old.ID); err != nil {
			return fmt.Errorf("删除旧资产 %s 失败: %w", name, err)
		}
		g.removeAssetFromList(rel, name)
	}
	return g.UploadAsset(rel.ID, name, data)
}

func (g *GH) DownloadAsset(a Asset) ([]byte, error) {
	u := fmt.Sprintf("%s/repos/%s/releases/assets/%d", apiBase(), g.repo, a.ID)
	var lastErr error
	for i := 0; i < 3; i++ {
		resp, err := g.do("GET", u, map[string]string{"Accept": "application/octet-stream"}, nil)
		if err != nil {
			lastErr = err
		} else if resp.StatusCode == http.StatusOK {
			return io.ReadAll(resp.Body)
		} else {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = &apiError{Status: resp.StatusCode, Message: string(b)}
		}
		time.Sleep(time.Duration(2*(i+1)) * time.Second)
	}
	return nil, fmt.Errorf("下载资产 %s 失败: %w", a.Name, lastErr)
}

func (g *GH) AssetByName(rel *Release, name string) *Asset {
	for i := range rel.Assets {
		if rel.Assets[i].Name == name {
			return &rel.Assets[i]
		}
	}
	return nil
}

func (g *GH) removeAssetFromList(rel *Release, name string) {
	out := rel.Assets[:0]
	for _, a := range rel.Assets {
		if a.Name != name {
			out = append(out, a)
		}
	}
	rel.Assets = out
}
