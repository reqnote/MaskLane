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
	maxKeywordRunes  = 160
	maxDocumentBytes = 2 << 20
)

var mappingKinds = [...]string{"company", "project", "person", "confidential", "other"}

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

func (snapshot *mappingSnapshot) automaticTokens(redactor *redact.Redactor) map[int]string {
	tokens := map[int]string{}
	for i, rule := range snapshot.Rules {
		if rule.Replacement != "" {
			continue
		}
		if token, ok := redactor.UsedReplacement(redact.Class(rule.Kind), rule.Original); ok {
			tokens[i] = token
		}
	}
	return tokens
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
		return nil, errors.New("ルールを保存できませんでした。保存先の空き容量とアクセス権を確認してください。")
	}
	snapshot := &mappingSnapshot{Revision: revision + 1, Rules: rules, matcher: matcher}
	store.active.Store(snapshot)
	return snapshot, nil
}

func compileMappings(input []mapping) ([]mapping, *redact.Matcher, error) {
	if len(input) > maxMappings {
		return nil, nil, fmt.Errorf("登録できるキーワードは最大 %d 件です。", maxMappings)
	}
	rules := append([]mapping{}, input...)
	originals := make(map[string]struct{}, len(rules))
	grouped := make(map[string]*redact.Dictionary, len(mappingKinds))
	for _, kind := range mappingKinds {
		grouped[kind] = &redact.Dictionary{Class: redact.Class(kind), Replacements: map[string]string{}}
	}
	for i := range rules {
		rule := &rules[i]
		rule.Original = strings.TrimSpace(rule.Original)
		rule.Replacement = strings.TrimSpace(rule.Replacement)
		if _, ok := grouped[rule.Kind]; !ok {
			return nil, nil, fmt.Errorf("%d 件目の種類が無効です。", i+1)
		}
		if rule.Original == "" || !validKeyword(rule.Original) || !validKeyword(rule.Replacement) {
			return nil, nil, fmt.Errorf("%d 件目のキーワードと置換後の表記を確認してください。各入力は %d 文字以内です。", i+1, maxKeywordRunes)
		}
		if _, exists := originals[rule.Original]; exists {
			return nil, nil, fmt.Errorf("%d 件目のマスキング対象のキーワードが重複しています。", i+1)
		}
		originals[rule.Original] = struct{}{}
	}
	for i, rule := range rules {
		dictionary := grouped[rule.Kind]
		dictionary.Entries = append(dictionary.Entries, rule.Original)
		if rule.Replacement == "" {
			continue
		}
		for original := range originals {
			if strings.Contains(rule.Replacement, original) {
				return nil, nil, fmt.Errorf("%d 件目の置換後の表記に、マスキング対象のキーワードが含まれています。", i+1)
			}
		}
		dictionary.Replacements[rule.Original] = rule.Replacement
	}
	if len(rules) == 0 {
		return rules, nil, nil
	}
	matcher := &redact.Matcher{}
	for _, kind := range mappingKinds {
		dictionary := grouped[kind]
		if len(dictionary.Entries) == 0 {
			continue
		}
		if err := matcher.AddDictionary(*dictionary); err != nil {
			return nil, nil, errors.New("マスキングルールを適用できませんでした。")
		}
	}
	return rules, matcher, nil
}

func validKeyword(text string) bool {
	return utf8.ValidString(text) && utf8.RuneCountInString(text) <= maxKeywordRunes &&
		!strings.ContainsFunc(text, unicode.IsControl)
}

func parseDocument(body io.Reader) (mappingDocument, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxDocumentBytes+1))
	if err != nil || len(data) > maxDocumentBytes || !utf8.Valid(data) {
		return mappingDocument{}, errors.New("UTF-8 の JSON ファイルを読み込めませんでした。")
	}
	var doc mappingDocument
	decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(string(data), "\uFEFF")))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil || decoder.Decode(new(any)) != io.EOF || doc.Version != 1 || doc.Rules == nil {
		return mappingDocument{}, errors.New("マスキングルールの JSON 形式を確認してください。")
	}
	if len(doc.Rules) > maxMappings {
		return mappingDocument{}, fmt.Errorf("読み込めるキーワードは最大 %d 件です。", maxMappings)
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
