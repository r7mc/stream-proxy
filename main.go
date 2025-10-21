package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ListenCfg struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type Config struct {
	Listen     ListenCfg          `json:"listen"`
	StreamHost string             `json:"stream_host"`
	Users      map[string]string  `json:"users"`
}

var (
	// 配置文件路径可由环境变量覆盖
	configPath = getenv("STREAM_CONFIG", "config.json")

	// 运行参数（启动时确定）
	bindHost   string
	bindPort   int
	streamHost string

	// users 热加载
	usersAtomic  atomic.Value // map[string]string
	usersMTimeNS int64
	usersMu      sync.Mutex

	// 高性能 HTTP 客户端
	httpClient = &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 60 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          512,
			MaxIdleConnsPerHost:   256,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   4 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
		},
		Timeout: 0, // 流式不设总超时
	}
)

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func ensureDefaultConfig() error {
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		def := Config{
			Listen:     ListenCfg{Host: "0.0.0.0", Port: 8000},
			StreamHost: "http://127.0.0.1:8080",
			Users:      map[string]string{"test": "123456"},
		}
		if dir := filepath.Dir(filepath.Clean(configPath)); dir != "." {
			_ = os.MkdirAll(dir, 0o755)
		}
		f, err := os.Create(configPath)
		if err != nil {
			return err
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		return enc.Encode(def)
	}
	return nil
}

func readConfigFromDisk() (cfg Config, mtimeNS int64, err error) {
	b, err := os.ReadFile(configPath)
	if err != nil {
		return cfg, 0, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, 0, err
	}
	// 合理默认
	if cfg.Listen.Host == "" {
		cfg.Listen.Host = "0.0.0.0"
	}
	if cfg.Listen.Port == 0 {
		cfg.Listen.Port = 8000
	}
	if cfg.Users == nil {
		cfg.Users = map[string]string{}
	}
	// 统一成字符串
	out := make(map[string]string, len(cfg.Users))
	for k, v := range cfg.Users {
		out[fmt.Sprint(k)] = fmt.Sprint(v)
	}
	cfg.Users = out

	fi, err := os.Stat(configPath)
	if err != nil {
		return cfg, 0, err
	}
	return cfg, fi.ModTime().UnixNano(), nil
}

// 启动时读取监听配置 + 预加载 users；支持环境变量覆盖监听/上游
func bootLoad() {
	if err := ensureDefaultConfig(); err != nil {
		log.Fatalf("init config: %v", err)
	}
	cfg, mt, err := readConfigFromDisk()
	if err != nil {
		log.Fatalf("read config: %v", err)
	}
	// 监听与上游：环境变量优先
	bindHost = getenv("HOST", cfg.Listen.Host)
	bindPort = getenvInt("PORT", cfg.Listen.Port)
	streamHost = getenv("STREAM_HOST", cfg.StreamHost)

	// 初始化 users 缓存
	usersAtomic.Store(cfg.Users)
	atomic.StoreInt64(&usersMTimeNS, mt)

	log.Printf("[StreamProxy] 启动配置 -> listen=%s:%d, stream_host=%s, users=%d",
		bindHost, bindPort, streamHost, len(cfg.Users))
}

// 仅热加载 users（监听地址与端口不在运行时变更）
func getUsers() map[string]string {
	fi, err := os.Stat(configPath)
	if err == nil {
		mt := fi.ModTime().UnixNano()
		if atomic.LoadInt64(&usersMTimeNS) == mt {
			if v := usersAtomic.Load(); v != nil {
				return v.(map[string]string)
			}
		}
	}
	usersMu.Lock()
	defer usersMu.Unlock()

	// 双检
	if fi2, err2 := os.Stat(configPath); err2 == nil {
		mt2 := fi2.ModTime().UnixNano()
		if atomic.LoadInt64(&usersMTimeNS) == mt2 {
			if v := usersAtomic.Load(); v != nil {
				return v.(map[string]string)
			}
		}
	}

	cfg, mt, err := readConfigFromDisk()
	if err != nil {
		log.Printf("[StreamProxy] 读取配置失败，沿用旧 users: %v", err)
		if v := usersAtomic.Load(); v != nil {
			return v.(map[string]string)
		}
		return map[string]string{}
	}
	usersAtomic.Store(cfg.Users)
	atomic.StoreInt64(&usersMTimeNS, mt)
	log.Printf("[StreamProxy] users 已热加载：%d 个", len(cfg.Users))
	return cfg.Users
}

func streamHandler(w http.ResponseWriter, r *http.Request) {
	users := getUsers()

	user := r.URL.Query().Get("user")
	pass := r.URL.Query().Get("pass")
	path := r.URL.Query().Get("path")
	if user == "" || pass == "" || path == "" {
		http.Error(w, "Missing parameters", http.StatusBadRequest)
		return
	}
	if users[user] != pass {
		http.Error(w, "Invalid credentials", http.StatusForbidden)
		return
	}

	path = strings.TrimLeft(path, "/")
	targetURL := fmt.Sprintf("%s/%s", strings.TrimRight(streamHost, "/"), path)
	log.Printf("[StreamProxy] Forwarding to: %s", targetURL)

	ctx := r.Context()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		http.Error(w, "Bad upstream request", http.StatusBadGateway)
		return
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Connection", "keep-alive")

	resp, err := httpClient.Do(req)
	if err != nil {
		http.Error(w, "Upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		w.WriteHeader(resp.StatusCode)
		io.CopyN(w, resp.Body, 4<<10)
		return
	}

	w.Header().Set("Content-Type", "video/mp2t")
	w.WriteHeader(http.StatusOK)

	buf := make([]byte, 64*1024)
	_, copyErr := io.CopyBuffer(w, resp.Body, buf)
	if copyErr != nil && !errors.Is(copyErr, context.Canceled) && !errors.Is(copyErr, net.ErrClosed) {
		log.Printf("[StreamProxy] stream copy error: %v", copyErr)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	users := getUsers()
	out := struct {
		OK         bool     `json:"ok"`
		Users      []string `json:"users"`
		ConfigFile string   `json:"config_file"`
		Listen     ListenCfg `json:"listen"`
		StreamHost string   `json:"stream_host"`
	}{
		OK:         true,
		Users:      make([]string, 0, len(users)),
		ConfigFile: abs(configPath),
		Listen:     ListenCfg{Host: bindHost, Port: bindPort},
		StreamHost: streamHost,
	}
	for k := range users {
		out.Users = append(out.Users, k)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(out)
}

func abs(p string) string {
	ap, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return ap
}

func main() {
	bootLoad() // 启动时读取监听/上游与 users

	mux := http.NewServeMux()
	mux.HandleFunc("/stream", streamHandler)
	mux.HandleFunc("/health", healthHandler)

	srv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", bindHost, bindPort),
		Handler:           mux,
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}

	log.Printf("[StreamProxy] 监听 http://%s:%d/stream", bindHost, bindPort)
	log.Printf("[StreamProxy] 配置文件: %s", abs(configPath))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("ListenAndServe: %v", err)
	}
}
