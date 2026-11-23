package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/luckyPipewrench/pipelock/internal/audit"
	"github.com/luckyPipewrench/pipelock/internal/certgen"
	"github.com/luckyPipewrench/pipelock/internal/config"
	"github.com/luckyPipewrench/pipelock/internal/metrics"
	"github.com/luckyPipewrench/pipelock/internal/proxy"
	"github.com/luckyPipewrench/pipelock/internal/scanner"
	"github.com/luckyPipewrench/pipelock/internal/signing"
)

var modelHosts = []string{
	"q.us-east-1.amazonaws.com", "q.eu-central-1.amazonaws.com",
	"runtime.us-east-1.kiro.dev", "runtime.eu-central-1.kiro.dev",
}

var connectionHosts = []string{
	"prod.us-east-1.auth.desktop.kiro.dev",
	"prod.download.desktop.kiro.dev",
	"app.kiro.dev", "eu-central-1.app.kiro.dev", "assets.app.kiro.dev",
	"management.us-east-1.kiro.dev", "management.eu-central-1.kiro.dev",
	"oidc.us-east-1.amazonaws.com", "oidc.eu-central-1.amazonaws.com",
	"cognito-identity.us-east-1.amazonaws.com",
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	proxyPort := flag.Int("proxy-port", 18888, "loopback HTTPS proxy port")
	dataDir := flag.String("data-dir", "", "override the Windows profile data directory")
	certificateOnly := flag.Bool("certificate-only", false, "prepare and print the CA certificate path")
	noBrowser := flag.Bool("no-browser", false, "print the dashboard URL without opening it")
	flag.Parse()
	if *proxyPort < 1 || *proxyPort > 65535 {
		return errors.New("プロキシのポート番号を 1〜65535 の範囲で指定してください。")
	}
	if *dataDir == "" {
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			return errors.New("Windows のローカルアプリデータフォルダーを取得できませんでした。")
		}
		// Keep the existing mappings and trusted CA after the product rename.
		*dataDir = filepath.Join(local, "MaskingGateway")
	}
	home, err := filepath.Abs(*dataDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return err
	}
	lock, err := lockDataDirectory(filepath.Join(home, "gateway.lock"))
	if err != nil {
		return fmt.Errorf("データフォルダーを使用できません。アプリが起動済みか確認してください。 %w", err)
	}
	defer lock.Close()
	certPath, keyPath, err := ensureCA(home)
	if err != nil {
		return fmt.Errorf("証明書を準備できませんでした。 %w", err)
	}
	if *certificateOnly {
		fmt.Println(certPath)
		return nil
	}
	signing.PipelockHome = home
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return serve(ctx, home, certPath, keyPath, *proxyPort, !*noBrowser)
}

func ensureCA(dir string) (string, string, error) {
	certPath := filepath.Join(dir, "ca.pem")
	keyPath := filepath.Join(dir, "ca-key.pem")
	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	if errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist) {
		cert, key, _, err := certgen.GenerateCA("MaskLane", 365*24*time.Hour)
		if err != nil {
			return "", "", err
		}
		return certPath, keyPath, certgen.SaveCA(certPath, keyPath, cert, key)
	}
	if err := errors.Join(certErr, keyErr); err != nil {
		return "", "", err
	}
	cert, _, err := certgen.LoadCA(certPath, keyPath)
	if err != nil {
		return "", "", err
	}
	if time.Now().After(cert.NotAfter) {
		return "", "", errors.New("証明書の有効期限が切れています。試用担当者に更新を依頼してください。")
	}
	return certPath, keyPath, nil
}

func serve(ctx context.Context, home, certPath, keyPath string, proxyPort int, openBrowser bool) error {
	store, err := openMappings(filepath.Join(home, "mappings.json"))
	if err != nil {
		return fmt.Errorf("保存済みのマスキングルールを読み込めませんでした。 %w", err)
	}
	// Bind before opening the dashboard. A second instance on the same port
	// cannot offer an editor backed by an inactive proxy.
	proxyAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort))
	proxyListener, err := net.Listen("tcp4", proxyAddress)
	if err != nil {
		return fmt.Errorf("プロキシを起動できません。アプリがすでに起動しているか、ポートが使用中です。 %w", err)
	}
	defer proxyListener.Close()
	uiListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer uiListener.Close()

	cfg := config.Defaults()
	cfg.Mode = config.ModeStrict
	cfg.APIAllowlist = append(append([]string{}, modelHosts...), connectionHosts...)
	cfg.FetchProxy.Listen = proxyAddress
	cfg.ForwardProxy.Enabled = true
	cfg.WebSocketProxy.Enabled = false
	cfg.TLSInterception.Enabled = true
	cfg.TLSInterception.CACertPath = certPath
	cfg.TLSInterception.CAKeyPath = keyPath
	cfg.TLSInterception.PassthroughDomains = connectionHosts
	cfg.RequestBodyScanning.Enabled = true
	cfg.RequestBodyScanning.IssuerBoundSessionCookies = false
	cfg.DLP.ScanEnv = false
	if err := cfg.Validate(); err != nil {
		return err
	}
	sc, err := scanner.New(cfg)
	if err != nil {
		return err
	}
	logger := audit.NewNop()
	defer logger.Close()
	status := &activityLog{}
	gateway, err := proxy.New(cfg, logger, sc, metrics.New(), proxy.WithMaskingGateway(proxy.MaskingGateway{
		Rules: func() proxy.MaskingRules {
			snapshot := store.active.Load()
			return proxy.MaskingRules{Matcher: snapshot.matcher, Revision: snapshot.Revision, AutomaticTokens: snapshot.automaticTokens}
		},
		Record: status.add,
	}))
	if err != nil {
		sc.Close()
		return err
	}
	defer gateway.Close()

	token := hex.EncodeToString(randBytes())
	dashboard := newDashboard(uiListener.Addr().String(), token, proxyAddress, store, status)
	proxyServer := newServer(ctx, kiroConnections(gateway.Handler(), cfg.APIAllowlist))
	uiServer := newServer(ctx, dashboard)
	errorsCh := make(chan error, 2)
	go func() { errorsCh <- proxyServer.Serve(proxyListener) }()
	go func() { errorsCh <- uiServer.Serve(uiListener) }()

	url := "http://" + uiListener.Addr().String() + "/#" + token
	fmt.Println("MaskLane")
	fmt.Println("Dashboard:", url)
	fmt.Println("Kiro proxy:", "http://"+proxyAddress)
	fmt.Println("CA:", certPath)
	if openBrowser {
		command := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
		command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := command.Start(); err == nil {
			go command.Wait()
		}
	}

	select {
	case <-ctx.Done():
	case err = <-errorsCh:
	}
	_ = uiServer.Close()
	_ = proxyServer.Close()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func randBytes() []byte {
	var token [32]byte
	_, _ = rand.Read(token[:]) // crypto/rand terminates the process if the OS fails.
	return token[:]
}

func newServer(ctx context.Context, handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

func kiroConnections(next http.Handler, hosts []string) http.Handler {
	allowed := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		allowed[host] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.Host)
		urlHost := r.URL.Host
		if r.Method != http.MethodConnect || err != nil || port != "443" ||
			!allowed[strings.ToLower(host)] || (urlHost != "" && !strings.EqualFold(urlHost, r.Host)) {
			http.Error(w, "Destination is outside this Kiro pilot configuration", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
