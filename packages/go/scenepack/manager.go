// manager —— T16 注册中心（m3-spec §7 包契约 F 的运行面）：两阶段原子安装
// （staging→commit；升级三步 rename 序列，崩溃恢复回滚上一完整版本——CH-08
// 同语义）、卸载全清（0 残留）、场景切换事件（Activate→SceneCtx）与考卷台账
// （安装即执行——T16-G1-03）。
package scenepack

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// 注册中心目录布局（root 下）：
//
//	registry/<packID>/         已安装包（manifest+资源+.scenepack-meta.json）
//	registry/<packID>.prev/    升级中上一完整版本（commit 间态；恢复面收敛）
//	staging/<packID>/          两阶段阶段一落点（安装/卸载中——随时可弃）
//	ledger.json                考卷台账（append-only 审计面）
const (
	metaName   = ".scenepack-meta.json"
	ledgerName = "ledger.json"
	prevSuffix = ".prev"
)

// EvalRecord 考卷台账条目（T16-G1-03：全包 eval_set 100% 执行+结果入台账——
// 报告 note 列每包得分）。Installed=false=考卷未过/负优化被拒（审计面照登）。
type EvalRecord struct {
	PackID      string  `json:"pack_id"`
	Version     string  `json:"version"`
	Entries     int     `json:"entries"`
	Passed      int     `json:"passed"`
	Score       float64 `json:"score"`
	Installed   bool    `json:"installed"`
	ContentHash string  `json:"content_hash"`
}

// Manager 场景包注册中心。单流串行使用（与 emotion/motionmap 同资产定性——
// loop 单线程驱动；互斥锁仅防测试并发观测）。
type Manager struct {
	root string

	mu     sync.Mutex
	active string
	ledger []EvalRecord

	// onStep 两阶段关键步注入面（nil=生产态）：CommitInstall 步①②后/Uninstall
	// 步①后回调——返回 error=该步失败（Commit 回滚已做迁移）；panic=崩溃仿真
	// （调用方 recover 后重启 Manager→Recover 收敛到完整版本态）。
	onStep func(step int) error
}

// NewManager 构造注册中心：建目录 + 启动即崩溃恢复（CH-08：回滚上一完整
// 版本/清 staging）+ 加载既有台账。
func NewManager(root string) (*Manager, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("scenepack: Manager root 为空")
	}
	m := &Manager{root: root}
	for _, d := range []string{filepath.Join(root, "registry"), filepath.Join(root, "staging")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("scenepack: 建目录 %s 失败: %w", d, err)
		}
	}
	if err := m.Recover(); err != nil {
		return nil, err
	}
	if err := m.loadLedger(); err != nil {
		return nil, err
	}
	return m, nil
}

// Root 注册根目录（观测面）。
func (m *Manager) Root() string { return m.root }

// Install 全量安装（两阶段合体：自动崩溃恢复先行 → Stage → Commit）。任一步
// 失败=registry 未变或已回滚上一完整版本（原子性——T16-G0-02）。
func (m *Manager) Install(p *Pack, classify SafetyClassifyFunc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.recoverLocked(); err != nil {
		return err
	}
	if err := m.stageInstallLocked(p, classify); err != nil {
		return err
	}
	return m.commitInstallLocked(p.Man.PackID)
}

// StageInstall 两阶段·阶段一（staging）：全量校验（结构/资源/考卷可执行）
// + T9 内容全量预检（fail-closed：classify nil=拒绝——T16-G0-01）+ 考卷执行
// （得分 ≥ min_pass；升级不得负优化）→ 落 staging/<id>（含 meta）。本阶段不
// 碰 registry（随时可弃）。
func (m *Manager) StageInstall(p *Pack, classify SafetyClassifyFunc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stageInstallLocked(p, classify)
}

func (m *Manager) stageInstallLocked(p *Pack, classify SafetyClassifyFunc) error {
	if classify == nil {
		return errors.New("scenepack: 须注入 T9 分类器（fail-closed——内容安全不可豁免，T16-G0-01）")
	}
	if err := p.Validate(); err != nil {
		return err
	}
	id := p.Man.PackID
	if !safePackID(id) {
		return fmt.Errorf("scenepack: pack_id %q 非安全目录名（注册面要求单段非点前缀名）", id)
	}
	if vs := p.CheckContentSafety(classify); len(vs) > 0 {
		return fmt.Errorf("scenepack: 包内容安全预检 %d 处违规（拒绝入包，T16-G0-01）: 首处 %s:%d %q",
			len(vs), vs[0].File, vs[0].Line, vs[0].Text)
	}
	rep, err := ExecuteEvalSet(p, classify)
	if err != nil {
		return err
	}
	rec := EvalRecord{PackID: id, Version: p.Man.Version, Entries: rep.Executed,
		Passed: rep.Passed, Score: rep.Score, ContentHash: p.ContentHash()}
	if rep.Score < p.Man.EvalSet.MinPass {
		if err := m.appendLedgerLocked(rec); err != nil { // 未过考卷照登台账（审计面）
			return err
		}
		return fmt.Errorf("scenepack: 包 %q 考卷得分 %.4f < min_pass %.4f（内容自带验收——BI-16.2，拒绝安装）",
			id, rep.Score, p.Man.EvalSet.MinPass)
	}
	if old := m.installedMeta(id); old != nil && rep.Score < old.Score {
		if err := m.appendLedgerLocked(rec); err != nil {
			return err
		}
		return fmt.Errorf("scenepack: 包 %q 升级负优化：考卷得分 %.4f < 已装 %.4f（内容不许负优化）", id, rep.Score, old.Score)
	}
	stg := filepath.Join(m.root, "staging", id)
	if err := os.RemoveAll(stg); err != nil {
		return fmt.Errorf("scenepack: 清旧 staging/%s 失败: %w", id, err)
	}
	if err := os.MkdirAll(stg, 0o755); err != nil {
		return fmt.Errorf("scenepack: 建 staging/%s 失败: %w", id, err)
	}
	keys := make([]string, 0, len(p.Files))
	for k := range p.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys) // 写入序确定（同包同落盘序）
	for _, k := range keys {
		dst := filepath.Join(stg, filepath.FromSlash(k))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("scenepack: staging 建目录 %s 失败: %w", k, err)
		}
		if err := os.WriteFile(dst, p.Files[k], 0o644); err != nil {
			return fmt.Errorf("scenepack: staging 写 %s 失败: %w", k, err)
		}
	}
	rec.Installed = false // staging 态未安装；commit 成功后按已装落账
	mb, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stg, metaName), append(mb, '\n'), 0o644); err != nil {
		return fmt.Errorf("scenepack: staging 写 meta 失败: %w", err)
	}
	return nil
}

// CommitInstall 两阶段·阶段二（commit）：staging→registry 原子迁移。升级三步：
// ①旧版让位（registry/<id>→.prev）②新版上位（staging→registry/<id>）③弃旧
// （删 .prev）。每步后 onStep 回调（注入面）：返回 error=本函数回滚已做迁移；
// panic=崩溃仿真（registry 恒为完整版本或空——重启 Recover 收敛，0 中间态）。
func (m *Manager) CommitInstall(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.commitInstallLocked(id)
}

func (m *Manager) commitInstallLocked(id string) error {
	if !safePackID(id) {
		return fmt.Errorf("scenepack: pack_id %q 非安全目录名", id)
	}
	stg := filepath.Join(m.root, "staging", id)
	if _, err := os.Stat(stg); err != nil {
		return fmt.Errorf("scenepack: staging/%s 不在（先 StageInstall）: %w", id, err)
	}
	cur := filepath.Join(m.root, "registry", id)
	prev := cur + prevSuffix
	// rollback 错误路径统一收口：旧版复位（若曾让位）+ staging 全弃（随时可弃面）
	// ——优雅失败 0 残留（T16-G0-02）；崩溃（panic）不走此路，归 Recover 收敛。
	rollback := func() {
		if _, err := os.Stat(prev); err == nil {
			os.Rename(prev, cur)
		}
		os.RemoveAll(stg)
	}
	hadOld := false
	if _, err := os.Stat(cur); err == nil {
		hadOld = true
		if err := os.Rename(cur, prev); err != nil { // ① 旧版让位
			return fmt.Errorf("scenepack: 旧版让位失败: %w", err)
		}
	}
	if err := m.step(1); err != nil { // 崩溃点①：旧版已让位、新版未上位
		rollback()
		return err
	}
	if err := os.Rename(stg, cur); err != nil { // ② 新版上位
		rollback()
		return fmt.Errorf("scenepack: 新版上位失败: %w", err)
	}
	if err := m.step(2); err != nil { // 崩溃点②：新版已完整上位、旧版残 .prev
		os.Rename(cur, stg) // 回滚：新版退回 staging（随 rollback 弃）
		rollback()
		return err
	}
	if hadOld {
		if err := os.RemoveAll(prev); err != nil { // ③ 弃旧（此步后状态即终态）
			return fmt.Errorf("scenepack: 清 %s 失败: %w", prev, err)
		}
	}
	rec, err := readMeta(cur)
	if err != nil {
		return err
	}
	rec.Installed = true
	if err := m.appendLedgerLocked(*rec); err != nil { // 考卷结果入台账（T16-G1-03）
		return err
	}
	if err := writeMeta(cur, rec); err != nil {
		return err
	}
	return nil
}

// Uninstall 两阶段卸载·全清：①registry/<id>→staging/<id>（注册面即时原子
// 消失；active 指向该包时同步回基线）②全清 staging 目录。之后 Residues=0
// （BI-16.3 卸载干净）。
func (m *Manager) Uninstall(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.recoverLocked(); err != nil {
		return err
	}
	if !safePackID(id) {
		return fmt.Errorf("scenepack: pack_id %q 非安全目录名", id)
	}
	cur := filepath.Join(m.root, "registry", id)
	stg := filepath.Join(m.root, "staging", id)
	if _, err := os.Stat(cur); err != nil {
		return fmt.Errorf("scenepack: 包 %q 未安装", id)
	}
	if err := os.Rename(cur, stg); err != nil { // ① 出注册面（原子）
		return fmt.Errorf("scenepack: 卸载迁移失败: %w", err)
	}
	if m.active == id {
		m.active = "" // 卸载即回无包基线
	}
	if err := m.step(4); err != nil { // 崩溃点：已出注册面、staging 残留（Recover 清）
		os.RemoveAll(stg) // 优雅失败收口：staging 全弃——0 残留（崩溃态归 Recover）
		return err
	}
	if err := os.RemoveAll(stg); err != nil { // ② 全清
		return fmt.Errorf("scenepack: 卸载清理失败: %w", err)
	}
	return nil
}

// Activate 场景切换事件（基线↔包上下文）：id=""→无包基线（零值 SceneCtx——
// 核心资产输出与基线一致）；id≠""→加载 registry/<id> 组装 SceneCtx（字节
// 负载注入面，消费归 loop——本包零 import 核心资产包）。
func (m *Manager) Activate(id string) (SceneCtx, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.recoverLocked(); err != nil {
		return SceneCtx{}, err
	}
	if id == "" {
		m.active = ""
		return SceneCtx{}, nil
	}
	if !safePackID(id) {
		return SceneCtx{}, fmt.Errorf("scenepack: pack_id %q 非安全目录名", id)
	}
	dir := filepath.Join(m.root, "registry", id)
	p, err := LoadManifest(dir)
	if err != nil {
		return SceneCtx{}, fmt.Errorf("scenepack: 激活 %q 失败（未安装或包破损）: %w", id, err)
	}
	if p.Man.PackID != id {
		return SceneCtx{}, fmt.Errorf("scenepack: 注册表破损：目录 %q 内 manifest.pack_id=%q", id, p.Man.PackID)
	}
	m.active = id
	return BuildSceneCtx(p), nil
}

// Active 当前激活包（""=无包基线）。
func (m *Manager) Active() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

// Installed 已安装包清单（registry 目录名排序；.prev 不计）。
func (m *Manager) Installed() []string {
	ents, err := os.ReadDir(filepath.Join(m.root, "registry"))
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range ents {
		if e.IsDir() && !strings.HasSuffix(e.Name(), prevSuffix) {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids
}

// Residues 全通道残留观测（0 残留断言口径——BI-16.3/T16-G0-02）：staging
// 任何条目 + registry/*.prev + active 指向不存在包。空清单=干净。
func (m *Manager) Residues() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	if ents, err := os.ReadDir(filepath.Join(m.root, "staging")); err == nil {
		for _, e := range ents {
			out = append(out, filepath.ToSlash(filepath.Join("staging", e.Name())))
		}
	}
	if ents, err := os.ReadDir(filepath.Join(m.root, "registry")); err == nil {
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), prevSuffix) {
				out = append(out, filepath.ToSlash(filepath.Join("registry", e.Name())))
			}
		}
	}
	if m.active != "" {
		if _, err := os.Stat(filepath.Join(m.root, "registry", m.active)); err != nil {
			out = append(out, "active→"+m.active+"(missing)")
		}
	}
	sort.Strings(out)
	return out
}

// Recover 崩溃恢复（幂等）：staging 全清 + .prev 收敛——registry/<id> 缺则
// 回滚 .prev（上一完整版本），否则弃 .prev（新版已完整提交）。恢复后
// Residues=0 且 registry 每包均为完整版本（CH-08 同语义）。
func (m *Manager) Recover() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recoverLocked()
}

func (m *Manager) recoverLocked() error {
	stg := filepath.Join(m.root, "staging")
	if ents, err := os.ReadDir(stg); err == nil {
		for _, e := range ents {
			if err := os.RemoveAll(filepath.Join(stg, e.Name())); err != nil {
				return fmt.Errorf("scenepack: 清 staging/%s 失败: %w", e.Name(), err)
			}
		}
	}
	reg := filepath.Join(m.root, "registry")
	ents, err := os.ReadDir(reg)
	if err != nil {
		return fmt.Errorf("scenepack: 读 registry 失败: %w", err)
	}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), prevSuffix) {
			continue
		}
		id := strings.TrimSuffix(e.Name(), prevSuffix)
		cur := filepath.Join(reg, id)
		prev := filepath.Join(reg, e.Name())
		if _, err := os.Stat(cur); errors.Is(err, os.ErrNotExist) {
			if err := os.Rename(prev, cur); err != nil { // 回滚上一完整版本
				return fmt.Errorf("scenepack: 回滚 registry/%s 失败: %w", e.Name(), err)
			}
		} else if err := os.RemoveAll(prev); err != nil { // 新版已完整 → 弃旧
			return fmt.Errorf("scenepack: 清 registry/%s 失败: %w", e.Name(), err)
		}
	}
	return nil
}

// Ledger 考卷台账快照（append-only 审计面——T16-G1-03）。
func (m *Manager) Ledger() []EvalRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]EvalRecord{}, m.ledger...)
}

func (m *Manager) loadLedger() error {
	data, err := os.ReadFile(filepath.Join(m.root, ledgerName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scenepack: 读台账失败: %w", err)
	}
	var ledger []EvalRecord
	if err := json.Unmarshal(data, &ledger); err != nil {
		return fmt.Errorf("scenepack: 台账 %s 不可解析: %w", ledgerName, err)
	}
	m.ledger = ledger
	return nil
}

func (m *Manager) appendLedgerLocked(rec EvalRecord) error {
	m.ledger = append(m.ledger, rec)
	data, err := json.MarshalIndent(m.ledger, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(m.root, ledgerName), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("scenepack: 写台账失败: %w", err)
	}
	return nil
}

// installedMeta 已装包 meta（负优化判定的旧分来源；未装=nil）。
func (m *Manager) installedMeta(id string) *EvalRecord {
	rec, err := readMeta(filepath.Join(m.root, "registry", id))
	if err != nil {
		return nil
	}
	return rec
}

func readMeta(dir string) (*EvalRecord, error) {
	data, err := os.ReadFile(filepath.Join(dir, metaName))
	if err != nil {
		return nil, fmt.Errorf("scenepack: 读 %s 失败: %w", metaName, err)
	}
	var rec EvalRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("scenepack: %s 不可解析: %w", metaName, err)
	}
	return &rec, nil
}

func writeMeta(dir string, rec *EvalRecord) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, metaName), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("scenepack: 写 %s 失败: %w", metaName, err)
	}
	return nil
}

// step 注入面回调（生产态 nil=直通）。
func (m *Manager) step(n int) error {
	if m.onStep == nil {
		return nil
	}
	return m.onStep(n)
}

// safePackID 注册面安全名：非空、单段（无路径分隔符）、非 "."/".."、无前导点
// （注册表目录名=pack_id，越界名拒绝）。
func safePackID(id string) bool {
	if id == "" || id == "." || id == ".." || strings.HasPrefix(id, ".") {
		return false
	}
	return !strings.ContainsAny(id, `/\`)
}
