package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckyPipewrench/pipelock/internal/redact"
)

type maskingRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip maskingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return roundTrip(r)
}

func TestMaskingGatewayIntercept(t *testing.T) {
	t.Setenv("PIPELOCK_HOME", t.TempDir())
	cfg := testScannerConfig()
	cfg.Internal = nil // No DNS lookups for the in-memory mock origin.
	cfg.RequestBodyScanning.Enabled = true
	p := newTestProxyWithConfig(t, cfg)
	var matcher *redact.Matcher
	var results []MaskingDecision
	WithMaskingGateway(MaskingGateway{
		Rules:  func() MaskingRules { return MaskingRules{Matcher: matcher} },
		Record: func(result MaskingDecision) { results = append(results, result) },
	})(p)
	var upstream []string
	upstreamError := false
	handler := newInterceptHandler(&InterceptContext{
		TargetHost: "api.vendor.example", TargetPort: "443", Config: cfg, Scanner: p.scannerPtr.Load(),
		Logger: p.logger, Metrics: p.metrics, ClientIP: "127.0.0.1", RequestID: "masking-test", Proxy: p,
	}, maskingRoundTripper(func(r *http.Request) (*http.Response, error) {
		if upstreamError {
			return nil, errors.New("mock origin unavailable")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		upstream = append(upstream, string(body))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")),
			Header: http.Header{"Content-Type": {"application/json"}}, ContentLength: 2, Request: r}, nil
	}))

	send := func(body string, status int, state, reason string, company, project int) {
		t.Helper()
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/chat", strings.NewReader(body))
		r.Host = "api.vendor.example"
		r.Header.Set("Content-Type", "application/x-amz-json-1.0")
		w := httptest.NewRecorder()
		count := len(results)
		handler.ServeHTTP(w, r)
		if w.Code != status || len(results) != count+1 {
			t.Fatalf("status=%d results=%d body=%s", w.Code, len(results)-count, w.Body.String())
		}
		result := results[count]
		if result.State != state || result.Reason != reason || result.Company != company || result.Project != project {
			t.Fatalf("unexpected decision: %+v", result)
		}
	}

	send(`{"input":"青葉商事"}`, 403, "blocked", "no_mapping", 0, 0)
	load := func(alias string) {
		t.Helper()
		candidate := &redact.Matcher{}
		if err := candidate.AddDictionary(redact.Dictionary{Class: "company", Entries: []string{"青葉商事"},
			Replacements: map[string]string{"青葉商事": alias}}); err != nil {
			t.Fatal(err)
		}
		if err := candidate.AddDictionary(redact.Dictionary{Class: "project", Entries: []string{"刷新計画"}}); err != nil {
			t.Fatal(err)
		}
		matcher = candidate
	}
	load("")
	send(`{"input":"青葉商事の刷新計画と青葉商事"}`, 200, "sent", "", 1, 1)
	load("顧客A")
	send(`{"files":[{"text":"青葉商事の刷新計画です。"}]}`, 200, "sent", "", 1, 1)
	send(`{"input":"日本語の短いメモです。"}`, 200, "sent", "", 0, 0)
	send(`{"input":`, 403, "blocked", "inspection", 0, 0)
	upstreamError = true
	send(`{"input":"青葉商事"}`, 502, "unconfirmed", "upstream", 0, 0)
	if len(upstream) != 3 ||
		!strings.Contains(upstream[0], "<pl:company:1>の<pl:project:1>と<pl:company:1>") ||
		!strings.Contains(upstream[1], "顧客Aの<pl:project:1>です。") ||
		strings.Contains(strings.Join(upstream, ""), "青葉商事") || strings.Contains(strings.Join(upstream, ""), "刷新計画") {
		t.Fatalf("unexpected forwarded test messages: %v", upstream)
	}
}

func TestMaskingGatewayTLS(t *testing.T) {
	t.Setenv("PIPELOCK_HOME", t.TempDir())
	cache, roots, cfg, sc, logger, metrics := testInterceptSetup(t)
	p, err := New(cfg, logger, sc, metrics)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	matcher := &redact.Matcher{}
	if err := matcher.AddDictionary(redact.Dictionary{Class: "company", Entries: []string{"青葉商事"}}); err != nil {
		t.Fatal(err)
	}
	if err := matcher.AddDictionary(redact.Dictionary{Class: "other", Entries: []string{"ALPHA-2048"}}); err != nil {
		t.Fatal(err)
	}
	if err := matcher.AddDictionary(redact.Dictionary{Class: "person", Entries: []string{"港野ミナ", "森丘リオ"}}); err != nil {
		t.Fatal(err)
	}
	if err := matcher.AddDictionary(redact.Dictionary{Class: "confidential", Entries: []string{"未発表計画A"}}); err != nil {
		t.Fatal(err)
	}
	decisions := make(chan MaskingDecision, 1)
	WithMaskingGateway(MaskingGateway{
		Rules: func() MaskingRules {
			return MaskingRules{Matcher: matcher, Revision: 7, AutomaticTokens: func(r *redact.Redactor) map[int]string {
				first, _ := r.UsedReplacement("person", "港野ミナ")
				second, _ := r.UsedReplacement("person", "森丘リオ")
				secret, _ := r.UsedReplacement("confidential", "未発表計画A")
				return map[int]string{1: first, 2: second, 3: secret}
			}}
		},
		Record: func(decision MaskingDecision) { decisions <- decision },
	})(p)
	forwarded := make(chan string, 1)
	upstream := maskingRoundTripper(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		forwarded <- string(body)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")),
			Header: http.Header{"Content-Type": {"application/json"}}, ContentLength: 2, Request: r}, nil
	})
	const host = "api.vendor.example"
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- interceptTunnel(ctx, server, &InterceptContext{
			TargetHost: host, TargetPort: "443", Config: cfg, Scanner: sc, CertCache: cache,
			Logger: logger, Metrics: metrics, ClientIP: "127.0.0.1", RequestID: "masking-tls-test",
			Proxy: p, UpstreamRT: upstream,
		})
	}()
	if deadline, ok := ctx.Deadline(); ok {
		if err := client.SetDeadline(deadline); err != nil {
			t.Fatal(err)
		}
	}
	tlsClient := tls.Client(client, &tls.Config{RootCAs: roots, ServerName: host, MinVersion: tls.VersionTLS12})
	if err := tlsClient.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+host+"/chat",
		strings.NewReader(`{"input":"青葉商事のALPHA-2048とALPHA-2048。森丘リオと港野ミナの未発表計画A"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := req.Write(tlsClient); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(tlsClient), req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(response.Body)
		t.Fatalf("TLS request returned %s: %s", response.Status, message)
	}
	select {
	case value := <-forwarded:
		if value != `{"input":"<pl:company:1>の<pl:other:1>と<pl:other:1>。<pl:person:1>と<pl:person:2>の<pl:confidential:1>"}` {
			t.Fatalf("unexpected TLS forwarded body: %q", value)
		}
	case <-ctx.Done():
		t.Fatal("upstream did not receive the request")
	}
	select {
	case decision := <-decisions:
		wantTokens := map[int]string{1: "<pl:person:2>", 2: "<pl:person:1>", 3: "<pl:confidential:1>"}
		if decision.State != "sent" || decision.Company != 1 || decision.Other != 1 ||
			decision.Person != 2 || decision.Confidential != 1 || decision.MappingRevision != 7 ||
			!maps.Equal(decision.Automatic, wantTokens) {
			t.Fatalf("unexpected TLS decision: %+v", decision)
		}
	case <-ctx.Done():
		t.Fatal("missing TLS event")
	}
	_ = client.Close()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("TLS tunnel did not shut down")
	}
}
