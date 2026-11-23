package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/luckyPipewrench/pipelock/internal/redact"
)

const (
	maxMappings      = 500
	maxNameRunes     = 160
	maxDocumentBytes = 2 << 20
)

type mapping struct {
	Kind        string `json:"kind"`
	Original    string `json:"original"`
	Replacement string `json:"replacement"`
}

type mappingDocument struct {
	Version int       `json:"version"`
	Rules   []mapping `json:"rules"`
}

type mappingSnapshot struct {
	Revision uint64    `json:"revision"`
	Rules    []mapping `json:"rules"`
	matcher  *redact.Matcher
}

type mappingStore struct {
	file   string
	mu     sync.Mutex
	active atomic.Pointer[mappingSnapshot]
}

func openMappings(file string) (*mappingStore, error) {
	store := &mappingStore{file: file}
	source, err := os.Open(file)
	if errors.Is(err, os.ErrNotExist) {
		store.active.Store(&mappingSnapshot{Revision: 1, Rules: []mapping{}})
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	defer source.Close()
	doc, err := parseDocument(source)
	if err != nil {
		return nil, err
	}
	rules, matcher, err := compileMappings(doc.Rules)
	if err != nil {
		return nil, err
	}
	store.active.Store(&mappingSnapshot{Revision: 1, Rules: rules, matcher: matcher})
	return store, nil
}

var errStale = errors.New("別の画面で設定が更新されました。画面を開き直してください。")

func (store *mappingStore) update(revision uint64, rules []mapping) (*mappingSnapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.active.Load().Revision != revision {
		return nil, errStale
	}
	rules, matcher, err := compileMappings(rules)
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(mappingDocument{Version: 1, Rules: rules}, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeAtomically(store.file, data); err != nil {
		return nil, errors.New("マッピングを保存できませんでした。保存先の空き容量とアクセス権を確認してください。")
	}
	snapshot := &mappingSnapshot{Revision: revision + 1, Rules: rules, matcher: matcher}
	store.active.Store(snapshot)
	return snapshot, nil
}

func compileMappings(input []mapping) ([]mapping, *redact.Matcher, error) {
	if len(input) > maxMappings {
		return nil, nil, fmt.Errorf("登録できる名前は最大 %d 件です。", maxMappings)
	}
	rules := append([]mapping{}, input...)
	names := make(map[string]struct{}, len(rules))
	for i := range rules {
		rule := &rules[i]
		rule.Original = strings.TrimSpace(rule.Original)
		rule.Replacement = strings.TrimSpace(rule.Replacement)
		if rule.Kind != "company" && rule.Kind != "project" {
			return nil, nil, fmt.Errorf("%d 件目の種類を企業名または案件名にしてください。", i+1)
		}
		if rule.Original == "" || !validName(rule.Original) || !validName(rule.Replacement) {
			return nil, nil, fmt.Errorf("%d 件目の名前を確認してください。各入力は %d 文字以内です。", i+1, maxNameRunes)
		}
		if _, exists := names[rule.Original]; exists {
			return nil, nil, fmt.Errorf("%d 件目の「伏せたい名前」が重複しています。", i+1)
		}
		names[rule.Original] = struct{}{}
	}
	grouped := map[string]*redact.Dictionary{
		"company": {Class: "company", Replacements: map[string]string{}},
		"project": {Class: "project", Replacements: map[string]string{}},
	}
	for i, rule := range rules {
		dictionary := grouped[rule.Kind]
		dictionary.Entries = append(dictionary.Entries, rule.Original)
		if rule.Replacement == "" {
			continue
		}
		for original := range names {
			if strings.Contains(rule.Replacement, original) {
				return nil, nil, fmt.Errorf("%d 件目の置換後の名前に、伏せたい名前が含まれています。", i+1)
			}
		}
		dictionary.Replacements[rule.Original] = rule.Replacement
	}
	if len(rules) == 0 {
		return rules, nil, nil
	}
	matcher := &redact.Matcher{}
	for _, kind := range []string{"company", "project"} {
		dictionary := grouped[kind]
		if len(dictionary.Entries) == 0 {
			continue
		}
		if err := matcher.AddDictionary(*dictionary); err != nil {
			return nil, nil, errors.New("マッピングを適用できませんでした。")
		}
	}
	return rules, matcher, nil
}

func validName(name string) bool {
	return utf8.ValidString(name) && utf8.RuneCountInString(name) <= maxNameRunes &&
		!strings.ContainsFunc(name, unicode.IsControl)
}

func parseDocument(body io.Reader) (mappingDocument, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxDocumentBytes+1))
	if err != nil || len(data) > maxDocumentBytes || !utf8.Valid(data) {
		return mappingDocument{}, errors.New("UTF-8 のマッピングファイルを読み込めませんでした。")
	}
	var doc mappingDocument
	decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(string(data), "\uFEFF")))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil || decoder.Decode(new(any)) != io.EOF || doc.Version != 1 || doc.Rules == nil {
		return mappingDocument{}, errors.New("マッピングファイルの形式を確認してください。")
	}
	if len(doc.Rules) > maxMappings {
		return mappingDocument{}, fmt.Errorf("取り込める名前は最大 %d 件です。", maxMappings)
	}
	return doc, nil
}

type importPlan struct {
	Revision  uint64    `json:"revision"`
	Added     []mapping `json:"added"`
	Duplicate int       `json:"duplicate"`
}

func planImport(snapshot *mappingSnapshot, incoming []mapping) (importPlan, error) {
	plan := importPlan{Revision: snapshot.Revision, Added: []mapping{}}
	names := make(map[string]struct{}, len(snapshot.Rules))
	for _, rule := range snapshot.Rules {
		names[rule.Original] = struct{}{}
	}
	for _, rule := range incoming {
		rule.Original = strings.TrimSpace(rule.Original)
		if _, exists := names[rule.Original]; exists {
			plan.Duplicate++
			continue
		}
		names[rule.Original] = struct{}{}
		plan.Added = append(plan.Added, rule)
	}
	combined := append(slices.Clone(snapshot.Rules), plan.Added...)
	normalized, _, err := compileMappings(combined)
	if err != nil {
		return importPlan{}, err
	}
	plan.Added = normalized[len(snapshot.Rules):]
	return plan, nil
}

func writeAtomically(filename string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(filename), ".mappings-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	if err := errors.Join(writeErr, syncErr, file.Close()); err != nil {
		return err
	}
	return replaceFile(file.Name(), filename)
}
