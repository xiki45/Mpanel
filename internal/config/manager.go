package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

type Validator interface {
	Validate(context.Context, string) error
}
type Reloader interface{ Reload(context.Context) error }

type BinaryValidator struct {
	Binary    string
	ConfigDir string
	Timeout   time.Duration
}

func (v BinaryValidator) Validate(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, v.Timeout)
	defer cancel()
	args := []string{"-t"}
	if v.ConfigDir != "" {
		args = append(args, "-d", v.ConfigDir)
	}
	args = append(args, "-f", path)
	out, err := exec.CommandContext(ctx, v.Binary, args...).CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("配置校验超时")
	}
	if err != nil {
		message := strings.TrimSpace(string(out))
		if len(message) > 2000 {
			message = message[:2000]
		}
		if message == "" {
			message = "mihomo 配置校验失败"
		}
		return errors.New(message)
	}
	return nil
}

type Manager struct {
	Path                string
	Validator           Validator
	Reloader            Reloader
	Now                 func() time.Time
	CompensationTimeout time.Duration
	mu                  sync.RWMutex
}

type Backup struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	Size      int64     `json:"size"`
}

type Listener struct {
	Name      string `json:"name" yaml:"name"`
	Type      string `json:"type" yaml:"type"`
	Listen    string `json:"listen" yaml:"listen"`
	Port      int    `json:"port" yaml:"port"`
	ExtraYAML string `json:"extraYaml" yaml:"-"`
	Vless     *Vless `json:"vless,omitempty" yaml:"-"`
}

// Vless is the structured VLESS listener configuration. The mihomo YAML stores
// it under `users` and `reality-config`; those keys are extracted into this
// struct when reading so the API can expose and edit them, while all other
// listener-specific fields remain in ExtraYAML.
type Vless struct {
	Users   []VlessUser `json:"users,omitempty"`
	Reality *Reality    `json:"reality,omitempty"`
}

type VlessUser struct {
	Username  string `json:"username,omitempty"`
	UUID      string `json:"uuid,omitempty"`
	Flow      string `json:"flow,omitempty"`
	ExtraYAML string `json:"extraYaml,omitempty"`
}

// Reality mirrors a mihomo `reality-config` block. Only the well-known fields
// are structured; any other keys are preserved verbatim in ExtraYAML.
type Reality struct {
	Dest        string   `json:"dest,omitempty"`
	PrivateKey  string   `json:"privateKey,omitempty"`
	ShortIds    []string `json:"shortIds,omitempty"`
	ServerNames []string `json:"serverNames,omitempty"`
	ExtraYAML   string   `json:"extraYaml,omitempty"`
}

var listenerTypes = map[string]bool{"mixed": true, "http": true, "socks": true, "shadowsocks": true, "vmess": true, "vless": true, "trojan": true, "hysteria2": true}

func (m *Manager) Read() ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return os.ReadFile(m.Path)
}

func (m *Manager) Save(ctx context.Context, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked(ctx, data)
}

func (m *Manager) saveLocked(ctx context.Context, data []byte) error {
	var parsed yaml.Node
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("YAML 解析失败: %w", err)
	}
	if len(parsed.Content) == 0 || parsed.Content[0].Kind != yaml.MappingNode {
		return errors.New("配置根节点必须是 YAML 对象")
	}
	return m.commitLocked(ctx, data)
}

// commitLocked runs the complete on-disk and runtime transaction while m.mu is held.
func (m *Manager) commitLocked(ctx context.Context, data []byte) error {
	dir := filepath.Dir(m.Path)
	info, err := os.Stat(m.Path)
	if err != nil {
		return fmt.Errorf("读取当前配置失败: %w", err)
	}
	old, err := os.ReadFile(m.Path)
	if err != nil {
		return fmt.Errorf("读取当前配置失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".mpanel-validate-*.yaml")
	if err != nil {
		return errors.New("无法在配置目录创建临时文件")
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = applyFileMetadata(tmp, info); err == nil {
		_, err = tmp.Write(data)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("写入临时配置失败")
	}
	if err := m.Validator.Validate(ctx, tmpPath); err != nil {
		return fmt.Errorf("配置校验失败: %w", err)
	}
	backup, err := m.createBackupLocked(old, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer m.pruneBackupsLocked()
	if err := replaceFile(tmpPath, m.Path); err != nil {
		_ = os.Remove(backup)
		return fmt.Errorf("替换配置失败: %w", err)
	}
	if err := m.Reloader.Reload(ctx); err != nil {
		initialReloadErr := err
		rollbackErr := writeAtomic(m.Path, old, info)
		if rollbackErr != nil {
			return fmt.Errorf("热重载失败 (%v)，且配置回滚失败: %w", initialReloadErr, rollbackErr)
		}
		compensationTimeout := m.CompensationTimeout
		if compensationTimeout <= 0 {
			compensationTimeout = 5 * time.Second
		}
		compensationCtx, cancel := context.WithTimeout(context.Background(), compensationTimeout)
		defer cancel()
		if compensationErr := m.Reloader.Reload(compensationCtx); compensationErr != nil {
			return fmt.Errorf("热重载失败 (%v)，已恢复文件但旧配置重新加载失败: %w", initialReloadErr, compensationErr)
		}
		return fmt.Errorf("热重载失败，已恢复原配置: %w", initialReloadErr)
	}
	return nil
}

func (m *Manager) createBackupLocked(data []byte, mode os.FileMode) (string, error) {
	now := time.Now
	if m.Now != nil {
		now = m.Now
	}
	baseName := filepath.Base(m.Path) + ".bak." + now().UTC().Format("20060102T150405.000000000Z")
	for sequence := 0; sequence < 1000; sequence++ {
		name := baseName
		if sequence > 0 {
			name += fmt.Sprintf("-%03d", sequence)
		}
		path := filepath.Join(filepath.Dir(m.Path), name)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", errors.New("创建配置备份失败")
		}
		_, writeErr := file.Write(data)
		if writeErr == nil {
			writeErr = file.Sync()
		}
		if closeErr := file.Close(); writeErr == nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			_ = os.Remove(path)
			return "", errors.New("创建配置备份失败")
		}
		return path, nil
	}
	return "", errors.New("创建配置备份失败")
}

func replaceFile(source, target string) error {
	if err := os.Rename(source, target); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(target)); err == nil {
		defer dir.Close()
		_ = dir.Sync()
	}
	return nil
}

func writeAtomic(target string, data []byte, info os.FileInfo) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".mpanel-write-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = applyFileMetadata(tmp, info); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceFile(name, target)
}

func applyFileMetadata(file *os.File, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("无法读取原配置属主信息")
	}
	if err := file.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
		return fmt.Errorf("保留配置属主失败: %w", err)
	}
	if err := file.Chmod(info.Mode().Perm()); err != nil {
		return fmt.Errorf("保留配置权限失败: %w", err)
	}
	return nil
}

func (m *Manager) Backups() ([]Backup, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.backupsLocked()
}

func (m *Manager) backupsLocked() ([]Backup, error) {
	pattern := filepath.Join(filepath.Dir(m.Path), filepath.Base(m.Path)+".bak.*")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	if len(paths) > 5 {
		paths = paths[:5]
	}
	result := make([]Backup, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		result = append(result, Backup{ID: strings.TrimPrefix(filepath.Base(path), filepath.Base(m.Path)+".bak."), CreatedAt: info.ModTime(), Size: info.Size()})
	}
	return result, nil
}

func (m *Manager) Restore(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == "" || strings.ContainsAny(id, `/\\`) || strings.Contains(id, "..") {
		return errors.New("无效的备份 ID")
	}
	path := filepath.Join(filepath.Dir(m.Path), filepath.Base(m.Path)+".bak."+id)
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.New("备份不存在")
	}
	return m.saveLocked(ctx, data)
}

func (m *Manager) pruneBackupsLocked() {
	pattern := filepath.Join(filepath.Dir(m.Path), filepath.Base(m.Path)+".bak.*")
	paths, _ := filepath.Glob(pattern)
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	for _, path := range paths[minimum(5, len(paths)):] {
		_ = os.Remove(path)
	}
}

func minimum(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (m *Manager) Listeners() ([]Listener, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	root, err := m.loadYAMLLocked()
	if err != nil {
		return nil, err
	}
	seq := mappingValue(root, "listeners")
	if seq == nil {
		return []Listener{}, nil
	}
	if seq.Kind != yaml.SequenceNode {
		return nil, errors.New("listeners 必须是列表")
	}
	result := make([]Listener, 0, len(seq.Content))
	for _, node := range seq.Content {
		listener, err := listenerFromNode(node)
		if err != nil {
			return nil, err
		}
		result = append(result, listener)
	}
	return result, nil
}

func (m *Manager) CreateListener(ctx context.Context, listener Listener) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.changeListenerLocked(ctx, "", listener, false)
}
func (m *Manager) UpdateListener(ctx context.Context, oldName string, listener Listener) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.changeListenerLocked(ctx, oldName, listener, false)
}
func (m *Manager) DeleteListener(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.changeListenerLocked(ctx, name, Listener{}, true)
}

func (m *Manager) changeListenerLocked(ctx context.Context, oldName string, listener Listener, remove bool) error {
	root, err := m.loadYAMLLocked()
	if err != nil {
		return err
	}
	seq := mappingValue(root, "listeners")
	if seq == nil {
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "listeners"}, seq)
	}
	if seq.Kind != yaml.SequenceNode {
		return errors.New("listeners 必须是列表")
	}
	index := -1
	for i, node := range seq.Content {
		name := scalarValue(node, "name")
		if name == oldName && oldName != "" {
			index = i
		}
		if !remove && name == listener.Name && name != oldName {
			return errors.New("入站名称已存在")
		}
	}
	if oldName != "" && index < 0 {
		return errors.New("入站不存在")
	}
	if remove {
		seq.Content = append(seq.Content[:index], seq.Content[index+1:]...)
	} else {
		if err := validateListener(listener); err != nil {
			return err
		}
		if index < 0 {
			node, err := listenerNode(listener, nil)
			if err != nil {
				return err
			}
			seq.Content = append(seq.Content, node)
		} else {
			node, err := listenerNode(listener, seq.Content[index])
			if err != nil {
				return err
			}
			seq.Content[index] = node
		}
	}
	var document yaml.Node
	document.Kind = yaml.DocumentNode
	document.Content = []*yaml.Node{root}
	data, err := yaml.Marshal(&document)
	if err != nil {
		return errors.New("生成 YAML 失败")
	}
	return m.commitLocked(ctx, data)
}

func (m *Manager) loadYAMLLocked() (*yaml.Node, error) {
	data, err := os.ReadFile(m.Path)
	if err != nil {
		return nil, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("YAML 解析失败: %w", err)
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("配置根节点必须是 YAML 对象")
	}
	return document.Content[0], nil
}

func validateListener(l Listener) error {
	l.Name = strings.TrimSpace(l.Name)
	if l.Name == "" {
		return errors.New("入站名称不能为空")
	}
	if !listenerTypes[l.Type] {
		return errors.New("不支持的入站类型")
	}
	if l.Port < 1 || l.Port > 65535 {
		return errors.New("端口必须为 1..65535")
	}
	if strings.TrimSpace(l.Listen) == "" {
		return errors.New("监听地址不能为空")
	}
	if l.Type == "vless" && l.Vless != nil {
		if err := validateVless(l.Vless); err != nil {
			return err
		}
	}
	return nil
}

// validateVless enforces the minimum VLESS structure required for a valid
// service config. Note this only runs on create/update via validateListener;
// historical configs read by Listeners do not pass through this validation.
func validateVless(v *Vless) error {
	if len(v.Users) == 0 {
		return errors.New("VLESS 至少需要一个用户")
	}
	for i, u := range v.Users {
		if strings.TrimSpace(u.UUID) == "" {
			return fmt.Errorf("VLESS 用户 %d 的 UUID 不能为空", i+1)
		}
	}
	if v.Reality != nil {
		if strings.TrimSpace(v.Reality.Dest) == "" {
			return errors.New("Reality 的 dest 不能为空")
		}
		if strings.TrimSpace(v.Reality.PrivateKey) == "" {
			return errors.New("Reality 的 private-key 不能为空")
		}
		if len(v.Reality.ShortIds) == 0 {
			return errors.New("Reality 的 short-id 不能为空")
		}
		if len(v.Reality.ServerNames) == 0 {
			return errors.New("Reality 的 server-names 不能为空")
		}
	}
	return nil
}

func listenerFromNode(node *yaml.Node) (Listener, error) {
	if node.Kind != yaml.MappingNode {
		return Listener{}, errors.New("listener 必须是对象")
	}
	l := Listener{Name: scalarValue(node, "name"), Type: scalarValue(node, "type"), Listen: scalarValue(node, "listen")}
	l.Port, _ = strconv.Atoi(scalarValue(node, "port"))
	if l.Type == "vless" {
		v, err := vlessFromNode(node)
		if err != nil {
			return Listener{}, err
		}
		l.Vless = v
	}
	extra := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if key != "name" && key != "type" && key != "listen" && key != "port" {
			if l.Type == "vless" && (key == "users" || key == "reality-config") {
				continue
			}
			extra.Content = append(extra.Content, node.Content[i], node.Content[i+1])
		}
	}
	if len(extra.Content) > 0 {
		data, _ := yaml.Marshal(extra)
		l.ExtraYAML = strings.TrimSpace(string(data))
	}
	return l, nil
}

func vlessFromNode(node *yaml.Node) (*Vless, error) {
	v := &Vless{}
	if usersNode := mappingValue(node, "users"); usersNode != nil {
		if usersNode.Kind != yaml.SequenceNode {
			return nil, errors.New("vless users 必须是列表")
		}
		for _, u := range usersNode.Content {
			user, err := vlessUserFromNode(u)
			if err != nil {
				return nil, err
			}
			v.Users = append(v.Users, user)
		}
	}
	if rc := mappingValue(node, "reality-config"); rc != nil {
		if rc.Kind != yaml.MappingNode {
			return nil, errors.New("reality-config 必须是对象")
		}
		r, err := realityFromNode(rc)
		if err != nil {
			return nil, err
		}
		v.Reality = r
	}
	return v, nil
}

func vlessUserFromNode(node *yaml.Node) (VlessUser, error) {
	if node.Kind != yaml.MappingNode {
		return VlessUser{}, errors.New("vless user 必须是对象")
	}
	u := VlessUser{Username: scalarValue(node, "username"), UUID: scalarValue(node, "uuid"), Flow: scalarValue(node, "flow")}
	extra := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if key != "username" && key != "uuid" && key != "flow" {
			extra.Content = append(extra.Content, node.Content[i], node.Content[i+1])
		}
	}
	if len(extra.Content) > 0 {
		data, _ := yaml.Marshal(extra)
		u.ExtraYAML = strings.TrimSpace(string(data))
	}
	return u, nil
}

func realityFromNode(node *yaml.Node) (*Reality, error) {
	r := &Reality{}
	if dest := mappingValue(node, "dest"); dest != nil {
		r.Dest = dest.Value
	}
	if pk := mappingValue(node, "private-key"); pk != nil {
		r.PrivateKey = pk.Value
	}
	if si := mappingValue(node, "short-id"); si != nil {
		if si.Kind == yaml.SequenceNode {
			for _, s := range si.Content {
				r.ShortIds = append(r.ShortIds, s.Value)
			}
		} else {
			r.ShortIds = append(r.ShortIds, si.Value)
		}
	}
	if sn := mappingValue(node, "server-names"); sn != nil {
		if sn.Kind == yaml.SequenceNode {
			for _, s := range sn.Content {
				r.ServerNames = append(r.ServerNames, s.Value)
			}
		} else {
			r.ServerNames = append(r.ServerNames, sn.Value)
		}
	}
	extra := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if key != "dest" && key != "private-key" && key != "short-id" && key != "server-names" {
			extra.Content = append(extra.Content, node.Content[i], node.Content[i+1])
		}
	}
	if len(extra.Content) > 0 {
		data, _ := yaml.Marshal(extra)
		r.ExtraYAML = strings.TrimSpace(string(data))
	}
	return r, nil
}

func listenerNode(l Listener, existing *yaml.Node) (*yaml.Node, error) {
	node := existing
	if node == nil {
		node = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	} else {
		common := make([]*yaml.Node, 0, 8)
		for i := 0; i < len(node.Content); i += 2 {
			if isListenerCommonField(node.Content[i].Value) {
				common = append(common, node.Content[i], node.Content[i+1])
			}
		}
		node.Content = common
	}
	setScalar(node, "name", l.Name)
	setScalar(node, "type", l.Type)
	setScalar(node, "listen", l.Listen)
	setScalar(node, "port", strconv.Itoa(l.Port))
	if strings.TrimSpace(l.ExtraYAML) != "" {
		var extra yaml.Node
		if err := yaml.Unmarshal([]byte(l.ExtraYAML), &extra); err != nil {
			return nil, fmt.Errorf("专属 YAML 解析失败: %w", err)
		}
		if len(extra.Content) == 0 || extra.Content[0].Kind != yaml.MappingNode {
			return nil, errors.New("专属 YAML 必须是对象")
		}
		for i := 0; i < len(extra.Content[0].Content); i += 2 {
			key := extra.Content[0].Content[i].Value
			if key == "name" || key == "type" || key == "listen" || key == "port" {
				return nil, fmt.Errorf("专属 YAML 不能覆盖通用字段 %s", key)
			}
			setNode(node, key, extra.Content[0].Content[i+1])
		}
	}
	if l.Type == "vless" {
		// users and reality-config are owned by the structured Vless field so
		// they can be added, removed or edited without losing unknown keys.
		deleteNodeKey(node, "users")
		deleteNodeKey(node, "reality-config")
		if l.Vless != nil {
			if err := applyVlessToNode(node, l.Vless); err != nil {
				return nil, err
			}
		}
	}
	return node, nil
}

func applyVlessToNode(node *yaml.Node, v *Vless) error {
	if v.Users != nil {
		users := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, u := range v.Users {
			un, err := vlessUserNode(u)
			if err != nil {
				return err
			}
			users.Content = append(users.Content, un)
		}
		setNode(node, "users", users)
	}
	if v.Reality != nil {
		rc, err := realityNode(v.Reality)
		if err != nil {
			return err
		}
		setNode(node, "reality-config", rc)
	}
	return nil
}

func vlessUserNode(u VlessUser) (*yaml.Node, error) {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if u.Username != "" {
		setScalar(n, "username", u.Username)
	}
	if u.UUID != "" {
		setScalar(n, "uuid", u.UUID)
	}
	if u.Flow != "" {
		setScalar(n, "flow", u.Flow)
	}
	if err := mergeExtraYAML(n, u.ExtraYAML, []string{"username", "uuid", "flow"}, "用户专属 YAML"); err != nil {
		return nil, err
	}
	return n, nil
}

func realityNode(r *Reality) (*yaml.Node, error) {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if r.Dest != "" {
		setScalar(n, "dest", r.Dest)
	}
	if r.PrivateKey != "" {
		setScalar(n, "private-key", r.PrivateKey)
	}
	if len(r.ShortIds) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, s := range r.ShortIds {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s})
		}
		setNode(n, "short-id", seq)
	}
	if len(r.ServerNames) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, s := range r.ServerNames {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s})
		}
		setNode(n, "server-names", seq)
	}
	if err := mergeExtraYAML(n, r.ExtraYAML, []string{"dest", "private-key", "short-id", "server-names"}, "reality 专属 YAML"); err != nil {
		return nil, err
	}
	return n, nil
}

// mergeExtraYAML parses an ExtraYAML string into a mapping and merges its keys
// into node, rejecting any key in reserved (owned by the structured fields).
func mergeExtraYAML(node *yaml.Node, extraYAML string, reserved []string, label string) error {
	if strings.TrimSpace(extraYAML) == "" {
		return nil
	}
	var extra yaml.Node
	if err := yaml.Unmarshal([]byte(extraYAML), &extra); err != nil {
		return fmt.Errorf("%s 解析失败: %w", label, err)
	}
	if len(extra.Content) == 0 || extra.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s 必须是对象", label)
	}
	for i := 0; i < len(extra.Content[0].Content); i += 2 {
		key := extra.Content[0].Content[i].Value
		if contains(reserved, key) {
			return fmt.Errorf("%s 不能覆盖通用字段 %s", label, key)
		}
		setNode(node, key, extra.Content[0].Content[i+1])
	}
	return nil
}

func deleteNodeKey(node *yaml.Node, key string) {
	out := node.Content[:0]
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			continue
		}
		out = append(out, node.Content[i], node.Content[i+1])
	}
	node.Content = out
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func isListenerCommonField(key string) bool {
	return key == "name" || key == "type" || key == "listen" || key == "port"
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
func scalarValue(node *yaml.Node, key string) string {
	v := mappingValue(node, key)
	if v == nil {
		return ""
	}
	return v.Value
}
func setScalar(node *yaml.Node, key, value string) {
	tag := "!!str"
	if key == "port" {
		tag = "!!int"
	}
	setNode(node, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
}
func setNode(node *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1] = value
			return
		}
	}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}
