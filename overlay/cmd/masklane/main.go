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

const (
	productName          = "MaskLane"
	defaultProxyPort     = 18888
	defaultDashboardPort = 18890
	bytesPerKiB          = 1024
	defaultMaxRequestKiB = 6 * bytesPerKiB
	maxRequestKiB        = proxy.MaxMaskingRequestBytes / bytesPerKiB
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
	proxyPort := flag.Int("proxy-port", defaultProxyPort, "loopback HTTPS proxy port")
	dashboardPort := flag.Int("dashboard-port", defaultDashboardPort, "loopback dashboard port (0 picks an available port)")
	requestLimit := flag.Int("max-request-kib", defaultMaxRequestKiB, fmt.Sprintf("maximum inspected request body size in KiB (1–%d)", maxRequestKiB))
	dataDir := flag.String("data-dir", "", "override the data directory")
	portable := flag.Bool("portable", true, "store data next to MaskLane.exe; set false to use the Windows profile")
	certificateOnly := flag.Bool("certificate-only", false, "prepare and print the CA certificate path")
	noBrowser := flag.Bool("no-browser", false, "print the dashboard URL without opening it")
	flag.Parse()
	if *proxyPort < 1 || *proxyPort > 65535 {
		return errors.New("プロキシのポート番号を 1〜65535 の範囲で指定してください。")
	}
	if *dashboardPort < 0 || *dashboardPort > 65535 {
		return errors.New("管理画面のポート番号を 0〜65535 の範囲で指定してください。0 は自動選択です。")
	}
	if *dashboardPort == *proxyPort {
		return errors.New("-proxy-port と -dashboard-port には異なるポート番号を指定してください。")
	}
	if *requestLimit < 1 || *requestLimit > maxRequestKiB {
		return fmt.Errorf("送信本文の上限を 1〜%d KiB の範囲で指定してください。", maxRequestKiB)
	}
	switch {
	case *dataDir != "":
		var portableSpecified bool
		flag.Visit(func(option *flag.Flag) {
			if option.Name == "portable" {
				portableSpecified = true
			}
		})
		if *portable && portableSpecified {
			return errors.New("-portable と -data-dir は同時に指定できません。")
		}
	case *portable:
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("MaskLane.exe の保存先を取得できませんでした。 %w", err)
		}
		*dataDir = filepath.Join(filepath.Dir(executable), "MaskLane-data")
	default:
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			return errors.New("Windows のローカルアプリデータフォルダーを取得できませんでした。")
		}
		dir, err := profileDataDirectory(local)
		if err != nil {
			return err
		}
		*dataDir = dir
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
	return serve(ctx, home, certPath, keyPath, *proxyPort, *dashboardPort, *requestLimit*bytesPerKiB, !*noBrowser)
}

func profileDataDirectory(local string) (string, error) {
	dir := filepath.Join(local, productName)
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		return dir, err
	}

	// Move the mappings and trusted CA together. Windows refuses to move the
	// directory while the previous executable has its lock file open.
	err := os.Rename(filepath.Join(local, "MaskingGateway"), dir)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return dir, nil
	}
	if _, statErr := os.Stat(dir); statErr == nil {
		return dir, nil // Another launch may have just completed the move.
	}
	var renameErr *os.LinkError
	if errors.As(err, &renameErr) {
		err = renameErr.Err
	}
	return "", fmt.Errorf("MaskLane のデータフォルダーに移行できません。以前のアプリを終了してから起動し直してください。 %w", err)
}

func ensureCA(dir string) (string, string, error) {
	certPath := filepath.Join(dir, "ca.pem")
	keyPath := filepath.Join(dir, "ca-key.pem")
	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	if errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist) {
		cert, key, _, err := certgen.GenerateCA(productName, 365*24*time.Hour)
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

func serve(ctx context.Context, home, certPath, keyPath string, proxyPort, dashboardPort, maxRequestBytes int, openBrowser bool) error {
	store, err := openMappings(filepath.Join(home, "mappings.json"))
	if err != nil {
		return fmt.Errorf("保存済みのマスキングルールを読み込めませんでした。 %w", err)
	}
	// Bind before opening the dashboard. A second instance on the same port
	// cannot offer an editor backed by an inactive proxy.
	proxyAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort))
	proxyListener, err := net.Listen("tcp4", proxyAddress)
	if err != nil {
		return fmt.Errorf("プロキシを %s で起動できません。アプリがすでに起動しているか確認し、必要なら -proxy-port で変更してください。 %w", proxyAddress, err)
	}
	defer proxyListener.Close()
	dashboardAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(dashboardPort))
	uiListener, err := net.Listen("tcp4", dashboardAddress)
	if err != nil {
		return fmt.Errorf("管理画面を %s で起動できません。-dashboard-port で別のポートを指定するか、-dashboard-port 0 で自動選択してください。 %w", dashboardAddress, err)
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
	cfg.RequestBodyScanning.MaxBodyBytes = maxRequestBytes
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
	if err := gateway.LoadCertCache(cfg); err != nil {
		return fmt.Errorf("HTTPS 通信の検査を準備できませんでした。 %w", err)
	}

	token := hex.EncodeToString(randBytes())
	dashboard := newDashboard(uiListener.Addr().String(), token, proxyAddress, certPath, store, status)
	proxyServer := newServer(ctx, kiroConnections(gateway.Handler(), cfg.APIAllowlist))
	uiServer := newServer(ctx, dashboard)
	errorsCh := make(chan error, 2)
	go func() { errorsCh <- proxyServer.Serve(proxyListener) }()
	go func() { errorsCh <- uiServer.Serve(uiListener) }()

	url := "http://" + uiListener.Addr().String() + "/#" + token
	fmt.Println(productName)
	fmt.Println("Dashboard:", url)
	fmt.Println("Kiro proxy:", "http://"+proxyAddress)
	fmt.Println("Request body limit (KiB):", maxRequestBytes/bytesPerKiB)
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
