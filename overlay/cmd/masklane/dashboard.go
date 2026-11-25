package main

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/luckyPipewrench/pipelock/internal/proxy"
)

//go:embed web/index.html web/app.css web/app.js web/restore.mjs web/favicon.png
var assets embed.FS

type activity struct {
	ID              uint64         `json:"id"`
	At              time.Time      `json:"at"`
	State           string         `json:"state"`
	Reason          string         `json:"reason,omitempty"`
	Company         int            `json:"company"`
	Project         int            `json:"project"`
	Other           int            `json:"other"`
	Person          int            `json:"person"`
	Confidential    int            `json:"confidential"`
	MappingRevision uint64         `json:"mappingRevision,omitempty"`
	Automatic       map[int]string `json:"automatic,omitempty"`
}

type activityLog struct {
	mu     sync.Mutex
	nextID uint64
	rows   []activity
}

func (log *activityLog) add(decision proxy.MaskingDecision) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.nextID++
	log.rows = append(log.rows, activity{
		ID: log.nextID, At: decision.At, State: decision.State, Reason: decision.Reason,
		Company: decision.Company, Project: decision.Project, Other: decision.Other,
		Person: decision.Person, Confidential: decision.Confidential,
		MappingRevision: decision.MappingRevision, Automatic: decision.Automatic,
	})
	if len(log.rows) > 20 {
		log.rows = slices.Clone(log.rows[len(log.rows)-20:])
	}
}

func (log *activityLog) snapshot() []activity {
	log.mu.Lock()
	defer log.mu.Unlock()
	rows := append([]activity{}, log.rows...)
	slices.Reverse(rows)
	return rows
}

func newDashboard(host, token, proxyAddress, certPath string, store *mappingStore, log *activityLog) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", serveAsset("web/index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /app.css", serveAsset("web/app.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /app.js", serveAsset("web/app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /restore.mjs", serveAsset("web/restore.mjs", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /favicon.png", serveAsset("web/favicon.png", "image/png"))

	command := kiroLaunchCommand("http://"+proxyAddress, certPath)
	mux.HandleFunc("GET /api/kiro-launch", func(w http.ResponseWriter, _ *http.Request) {
		respond(w, http.StatusOK, map[string]string{"command": command})
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, _ *http.Request) {
		snapshot := store.active.Load()
		respond(w, http.StatusOK, map[string]any{
			"proxy": "http://" + proxyAddress, "mappingCount": len(snapshot.Rules), "activity": log.snapshot(),
		})
	})
	mux.HandleFunc("GET /api/mappings", func(w http.ResponseWriter, _ *http.Request) {
		respond(w, http.StatusOK, store.active.Load())
	})
	mux.HandleFunc("PUT /api/mappings", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Revision uint64    `json:"revision"`
			Rules    []mapping `json:"rules"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDocumentBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || decoder.Decode(new(any)) != io.EOF || input.Rules == nil {
			respondError(w, http.StatusBadRequest, errors.New("マスキングルールの形式を確認してください。"))
			return
		}
		snapshot, err := store.update(input.Revision, input.Rules)
		switch {
		case errors.Is(err, errStale):
			respondError(w, http.StatusConflict, err)
		case err != nil:
			respondError(w, http.StatusBadRequest, err)
		default:
			respond(w, http.StatusOK, snapshot)
		}
	})
	mux.HandleFunc("POST /api/import-preview", func(w http.ResponseWriter, r *http.Request) {
		doc, err := parseDocument(r.Body)
		if err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		plan, err := planImport(store.active.Load(), doc.Rules)
		if err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		respond(w, http.StatusOK, plan)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.Host != host {
			http.Error(w, "Invalid dashboard host", http.StatusMisdirectedRequest)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+host {
			http.Error(w, "Invalid dashboard origin", http.StatusForbidden)
			return
		}
		if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
			key := r.Header.Get("X-Masking-Session")
			if subtle.ConstantTimeCompare([]byte(key), []byte(token)) != 1 {
				respondError(w, http.StatusForbidden, errors.New("アプリの起動時に表示されたダッシュボードを開いてください。"))
				return
			}
			if r.Method == http.MethodPut || r.Method == http.MethodPost {
				contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if contentType != "application/json" {
					respondError(w, http.StatusUnsupportedMediaType, errors.New("JSON 形式を指定してください。"))
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func serveAsset(name, contentType string) http.HandlerFunc {
	data, err := assets.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(data)
	}
}

func respondError(w http.ResponseWriter, status int, err error) {
	respond(w, status, map[string]string{"error": err.Error()})
}

func respond(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
