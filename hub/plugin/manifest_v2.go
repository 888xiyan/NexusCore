package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidDependencySpec      = errors.New("依赖声明非法")
	ErrInvalidInterfaceDefinition = errors.New("接口定义非法")
	ErrInvalidInterfacesDoc       = errors.New("接口注册文档非法")
)

// DependencySpec 业务插件声明的接口契约依赖 (Manifest v2)
type DependencySpec struct {
	Interface    string `json:"interface"`               // 依赖的目标接口名，如 "nexus.groupchat.v1"
	VersionRange string `json:"version_range"`           // SemVer 范围语法，如 "^1.0.0", ">=1.0.0 <2.0.0"
	Optional     bool   `json:"optional"`                // 是否为可选依赖
	Reason       string `json:"reason,omitempty"`        // 引入依赖的业务诉求与说明
}

// InterfaceDefinition 基础插件暴露的接口契约
type InterfaceDefinition struct {
	InterfaceID  string   `json:"interface_id"`  // 接口唯一标识，如 "nexus.groupchat.v1"
	Version      string   `json:"version"`       // 语义化版本，如 "1.0.0"
	Protocol     string   `json:"protocol"`      // "http_rest" | "grpc" | "transport_frame"
	Endpoint     string   `json:"endpoint"`      // 监听地址、URL 或 UNIX Domain Socket
	HealthCheck  string   `json:"health_check"`  // 探活 URI
	Capabilities []string `json:"capabilities"`  // 细分能力特性标签
	Stability    string   `json:"stability"`     // "alpha" | "beta" | "stable"
}

// PluginInterfacesDoc 配置中心存储的插件接口规范文档 (scope_type='plugin', config_key='interfaces')
type PluginInterfacesDoc struct {
	PluginID           string                `json:"plugin_id"`
	PluginVersion      string                `json:"plugin_version"`
	ProvidesInterfaces []InterfaceDefinition `json:"provides_interfaces"`
}

// ValidateDependencySpec 校验单个依赖声明的合法性
func ValidateDependencySpec(spec *DependencySpec) error {
	if spec == nil {
		return fmt.Errorf("%w: 依赖项不能为空", ErrInvalidDependencySpec)
	}
	interfaceID := strings.TrimSpace(spec.Interface)
	if interfaceID == "" {
		return fmt.Errorf("%w: interface 不能为空", ErrInvalidDependencySpec)
	}
	if !IsValidIdentifier(interfaceID) {
		return fmt.Errorf("%w: interface %q 命名不符合规范 (必须为字母/数字/下划线/点/中划线)", ErrInvalidDependencySpec, interfaceID)
	}
	if spec.VersionRange != "" {
		if _, err := ParseRange(spec.VersionRange); err != nil {
			return fmt.Errorf("%w: version_range %q 非法: %v", ErrInvalidDependencySpec, spec.VersionRange, err)
		}
	}
	return nil
}

// ValidateInterfaceDefinition 校验单个接口定义
func ValidateInterfaceDefinition(def *InterfaceDefinition) error {
	if def == nil {
		return fmt.Errorf("%w: 接口定义不能为空", ErrInvalidInterfaceDefinition)
	}
	interfaceID := strings.TrimSpace(def.InterfaceID)
	if interfaceID == "" {
		return fmt.Errorf("%w: interface_id 不能为空", ErrInvalidInterfaceDefinition)
	}
	if !IsValidIdentifier(interfaceID) {
		return fmt.Errorf("%w: interface_id %q 命名不符合规范", ErrInvalidInterfaceDefinition, interfaceID)
	}
	if _, err := ParseVersion(def.Version); err != nil {
		return fmt.Errorf("%w: version %q 非法 SemVer: %v", ErrInvalidInterfaceDefinition, def.Version, err)
	}
	switch def.Protocol {
	case "http_rest", "grpc", "transport_frame":
	default:
		return fmt.Errorf("%w: 不支持的 protocol %q (支持 http_rest, grpc, transport_frame)", ErrInvalidInterfaceDefinition, def.Protocol)
	}
	if strings.TrimSpace(def.Endpoint) == "" {
		return fmt.Errorf("%w: endpoint 不能为空", ErrInvalidInterfaceDefinition)
	}
	if def.Stability == "" {
		def.Stability = "stable"
	}
	switch def.Stability {
	case "alpha", "beta", "stable":
	default:
		return fmt.Errorf("%w: 不支持的 stability %q (支持 alpha, beta, stable)", ErrInvalidInterfaceDefinition, def.Stability)
	}
	return nil
}

// ValidateInterfacesDoc 校验配置中心接口注册文档
func ValidateInterfacesDoc(doc *PluginInterfacesDoc) error {
	if doc == nil {
		return fmt.Errorf("%w: 接口文档不能为空", ErrInvalidInterfacesDoc)
	}
	if !pluginIDPattern.MatchString(doc.PluginID) {
		return fmt.Errorf("%w: plugin_id %q 不符合规范 ^plugin_[a-z0-9_]{3,24}$", ErrInvalidInterfacesDoc, doc.PluginID)
	}
	if _, err := ParseVersion(doc.PluginVersion); err != nil {
		return fmt.Errorf("%w: plugin_version %q 非法 SemVer: %v", ErrInvalidInterfacesDoc, doc.PluginVersion, err)
	}
	if len(doc.ProvidesInterfaces) == 0 {
		return fmt.Errorf("%w: provides_interfaces 不能为空", ErrInvalidInterfacesDoc)
	}
	seen := make(map[string]bool)
	for i := range doc.ProvidesInterfaces {
		def := &doc.ProvidesInterfaces[i]
		if err := ValidateInterfaceDefinition(def); err != nil {
			return fmt.Errorf("%w: 接口定义校验失败: %v", ErrInvalidInterfacesDoc, err)
		}
		if seen[def.InterfaceID] {
			return fmt.Errorf("%w: 重复声明的 interface_id %q", ErrInvalidInterfacesDoc, def.InterfaceID)
		}
		seen[def.InterfaceID] = true
	}
	return nil
}

// SerializeInterfacesDoc 序列化接口文档为 JSON 字节流 (经过合法性校验)
func SerializeInterfacesDoc(doc *PluginInterfacesDoc) ([]byte, error) {
	if err := ValidateInterfacesDoc(doc); err != nil {
		return nil, err
	}
	return json.MarshalIndent(doc, "", "  ")
}

// DeserializeInterfacesDoc 从 JSON 字节流反序列化并校验接口文档
func DeserializeInterfacesDoc(data []byte) (*PluginInterfacesDoc, error) {
	var doc PluginInterfacesDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: JSON 解析失败: %v", ErrInvalidInterfacesDoc, err)
	}
	if err := ValidateInterfacesDoc(&doc); err != nil {
		return nil, err
	}
	return &doc, nil
}
