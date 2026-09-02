package synthgen

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Generator 注册记录：恰含四字段（spec §3.7），JSON 键与 register CLI flag 一一对应。
type Generator struct {
	ID              string `json:"id"`
	Version         string `json:"version"`
	SeedPolicy      string `json:"seed_policy"`
	OutputsManifest string `json:"outputs_manifest"`
}

// ErrDuplicateGenerator：(id, version) 已注册。
var ErrDuplicateGenerator = errors.New("生成器已注册")

// LoadRegistry 读注册表 jsonl（每行一条记录）；文件不存在视作空表。
func LoadRegistry(path string) ([]Generator, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []Generator
	for i, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var g Generator
		if err := json.Unmarshal([]byte(line), &g); err != nil {
			return nil, fmt.Errorf("注册表第 %d 行非法 JSON: %w", i+1, err)
		}
		records = append(records, g)
	}
	return records, nil
}

// FindGenerator 按 id 查注册记录；未注册报错；多条命中取最近注册（最新版本）。
func FindGenerator(records []Generator, id string) (Generator, error) {
	var g Generator
	found := false
	for _, r := range records {
		if r.ID == id {
			g, found = r, true
		}
	}
	if !found {
		return g, fmt.Errorf("生成器未注册: %s", id)
	}
	return g, nil
}

// RegisterGenerator 追加注册一个生成器；(id, version) 重复时报错且不落盘。
func RegisterGenerator(path, id, version, seedPolicy, outputsManifest string) (Generator, error) {
	g := Generator{ID: id, Version: version, SeedPolicy: seedPolicy, OutputsManifest: outputsManifest}
	records, err := LoadRegistry(path)
	if err != nil {
		return g, err
	}
	for _, r := range records {
		if r.ID == id && r.Version == version {
			return g, fmt.Errorf("%w: %s@%s", ErrDuplicateGenerator, id, version)
		}
	}
	data, err := json.Marshal(g)
	if err != nil {
		return g, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return g, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return g, err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return g, err
	}
	return g, nil
}
