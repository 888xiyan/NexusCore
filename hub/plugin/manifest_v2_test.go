package plugin

import (
	"strings"
	"testing"
)

func TestValidateDependencySpec(t *testing.T) {
	valid := &DependencySpec{
		Interface:    "nexus.groupchat.v1",
		VersionRange: "^1.0.0",
		Optional:     false,
		Reason:       "调用群聊接口",
	}
	if err := ValidateDependencySpec(valid); err != nil {
		t.Fatalf("合法依赖校验失败: %v", err)
	}

	// nil spec
	if err := ValidateDependencySpec(nil); err == nil {
		t.Errorf("nil spec 未报错")
	}

	// 空 interface
	badSpec1 := *valid
	badSpec1.Interface = ""
	if err := ValidateDependencySpec(&badSpec1); err == nil {
		t.Errorf("空 interface 未报错")
	}

	// 非法 interface 字符
	badSpec2 := *valid
	badSpec2.Interface = "nexus groupchat"
	if err := ValidateDependencySpec(&badSpec2); err == nil {
		t.Errorf("包含空格的 interface 未报错")
	}

	// 非法 SemVer range
	badSpec3 := *valid
	badSpec3.VersionRange = ">=bad.version"
	if err := ValidateDependencySpec(&badSpec3); err == nil {
		t.Errorf("非法 version_range 未报错")
	}

	// 空 version_range (允许，视为任意版本)
	emptyRangeSpec := *valid
	emptyRangeSpec.VersionRange = ""
	if err := ValidateDependencySpec(&emptyRangeSpec); err != nil {
		t.Errorf("空 version_range 校验报错: %v", err)
	}
}

func TestValidateInterfaceDefinition(t *testing.T) {
	valid := &InterfaceDefinition{
		InterfaceID:  "nexus.groupchat.v1",
		Version:      "1.0.0",
		Protocol:     "http_rest",
		Endpoint:     "http://127.0.0.1:7300/v1/chat",
		HealthCheck:  "http://127.0.0.1:7300/v1/chat/health",
		Capabilities: []string{"room_lifecycle"},
		Stability:    "stable",
	}
	if err := ValidateInterfaceDefinition(valid); err != nil {
		t.Fatalf("合法接口定义校验失败: %v", err)
	}

	// nil def
	if err := ValidateInterfaceDefinition(nil); err == nil {
		t.Errorf("nil def 未报错")
	}

	// 空 interface_id
	badDef1 := *valid
	badDef1.InterfaceID = ""
	if err := ValidateInterfaceDefinition(&badDef1); err == nil {
		t.Errorf("空 interface_id 未报错")
	}

	// 非法 interface_id
	badDef2 := *valid
	badDef2.InterfaceID = "bad id!"
	if err := ValidateInterfaceDefinition(&badDef2); err == nil {
		t.Errorf("非法字符 interface_id 未报错")
	}

	// 非法 version
	badDef3 := *valid
	badDef3.Version = "not_a_version"
	if err := ValidateInterfaceDefinition(&badDef3); err == nil {
		t.Errorf("非法 version 未报错")
	}

	// 不支持的 protocol
	badDef4 := *valid
	badDef4.Protocol = "websocket"
	if err := ValidateInterfaceDefinition(&badDef4); err == nil {
		t.Errorf("不支持的 protocol 未报错")
	}

	// 空 endpoint
	badDef5 := *valid
	badDef5.Endpoint = ""
	if err := ValidateInterfaceDefinition(&badDef5); err == nil {
		t.Errorf("空 endpoint 未报错")
	}

	// 缺省 stability 默认为 stable
	defaultStab := *valid
	defaultStab.Stability = ""
	if err := ValidateInterfaceDefinition(&defaultStab); err != nil {
		t.Errorf("缺省 stability 报错: %v", err)
	}
	if defaultStab.Stability != "stable" {
		t.Errorf("缺省 stability 未置为 stable: %s", defaultStab.Stability)
	}

	// 非法 stability
	badDef6 := *valid
	badDef6.Stability = "experimental"
	if err := ValidateInterfaceDefinition(&badDef6); err == nil {
		t.Errorf("不支持的 stability 未报错")
	}
}

func TestValidateInterfacesDoc(t *testing.T) {
	valid := &PluginInterfacesDoc{
		PluginID:      "plugin_groupchat",
		PluginVersion: "1.0.0",
		ProvidesInterfaces: []InterfaceDefinition{
			{
				InterfaceID: "nexus.groupchat.v1",
				Version:     "1.0.0",
				Protocol:    "http_rest",
				Endpoint:    "http://127.0.0.1:7300/v1/chat",
			},
		},
	}
	if err := ValidateInterfacesDoc(valid); err != nil {
		t.Fatalf("合法接口文档校验失败: %v", err)
	}

	// nil doc
	if err := ValidateInterfacesDoc(nil); err == nil {
		t.Errorf("nil doc 未报错")
	}

	// 非法 plugin_id
	badDoc1 := *valid
	badDoc1.PluginID = "bad_name"
	if err := ValidateInterfacesDoc(&badDoc1); err == nil {
		t.Errorf("非法 plugin_id 未报错")
	}

	// 非法 plugin_version
	badDoc2 := *valid
	badDoc2.PluginVersion = "abc"
	if err := ValidateInterfacesDoc(&badDoc2); err == nil {
		t.Errorf("非法 plugin_version 未报错")
	}

	// 空 provides_interfaces
	badDoc3 := *valid
	badDoc3.ProvidesInterfaces = nil
	if err := ValidateInterfacesDoc(&badDoc3); err == nil {
		t.Errorf("空 provides_interfaces 未报错")
	}

	// 重复 interface_id
	badDoc4 := *valid
	badDoc4.ProvidesInterfaces = []InterfaceDefinition{
		{InterfaceID: "nexus.dup.v1", Version: "1.0.0", Protocol: "grpc", Endpoint: "localhost:50051"},
		{InterfaceID: "nexus.dup.v1", Version: "1.0.1", Protocol: "grpc", Endpoint: "localhost:50051"},
	}
	if err := ValidateInterfacesDoc(&badDoc4); err == nil {
		t.Errorf("重复 interface_id 未报错")
	}

	// 接口定义本身校验失败
	badDoc5 := *valid
	badDoc5.ProvidesInterfaces = []InterfaceDefinition{
		{InterfaceID: "nexus.invalid.v1", Version: "1.0.0", Protocol: "unknown_proto", Endpoint: "localhost:50051"},
	}
	if err := ValidateInterfacesDoc(&badDoc5); err == nil {
		t.Errorf("非法接口定义的文档未报错")
	}
}

func TestSerializeDeserializeInterfacesDoc(t *testing.T) {
	doc := &PluginInterfacesDoc{
		PluginID:      "plugin_agent_mesh",
		PluginVersion: "1.2.0",
		ProvidesInterfaces: []InterfaceDefinition{
			{
				InterfaceID:  "nexus.agent_mesh.v1",
				Version:      "1.2.0",
				Protocol:     "transport_frame",
				Endpoint:     "127.0.0.1:7200",
				HealthCheck:  "/health",
				Capabilities: []string{"distillation", "heartbeat"},
				Stability:    "stable",
			},
		},
	}

	data, err := SerializeInterfacesDoc(doc)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}

	deserialized, err := DeserializeInterfacesDoc(data)
	if err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}

	if deserialized.PluginID != doc.PluginID || deserialized.PluginVersion != doc.PluginVersion {
		t.Errorf("反序列化内容不符: %+v", deserialized)
	}
	if len(deserialized.ProvidesInterfaces) != 1 || deserialized.ProvidesInterfaces[0].InterfaceID != "nexus.agent_mesh.v1" {
		t.Errorf("接口定义不符: %+v", deserialized.ProvidesInterfaces)
	}

	// 非法 doc 序列化报错
	badDoc := &PluginInterfacesDoc{PluginID: "bad"}
	if _, err := SerializeInterfacesDoc(badDoc); err == nil {
		t.Errorf("非法 doc 序列化未报错")
	}

	// 非法 json 反序列化报错
	if _, err := DeserializeInterfacesDoc([]byte("invalid json")); err == nil {
		t.Errorf("非法 JSON 反序列化未报错")
	}

	// 合法 JSON 但业务内容非法反序列化报错
	if _, err := DeserializeInterfacesDoc([]byte(`{"plugin_id": "bad"}`)); err == nil {
		t.Errorf("语义非法 JSON 反序列化未报错")
	}
}

func TestManifestV2Compatibility(t *testing.T) {
	// Manifest v1: 仅 deps，无 dependencies
	v1Mf := &Manifest{
		PluginID:   "plugin_v1_demo",
		Name:       "V1 Demo",
		Version:    "1.0.0",
		PluginType: "workspace",
		BotTemplates: []BotTemplate{
			{BotID: "demo_v1_bot", Role: "plugin", DisplayName: "Demo V1", ActivationMode: "lazy"},
		},
		Deps: []string{"plugin_groupchat"},
	}
	if err := ValidateManifest(v1Mf); err != nil {
		t.Fatalf("Manifest v1 兼容性校验失败: %v", err)
	}

	// Manifest v2: 带有 dependencies
	v2Mf := &Manifest{
		PluginID:      "plugin_v2_demo",
		Name:          "V2 Demo",
		Version:       "2.0.0",
		PluginType:    "workspace",
		SchemaVersion: 2,
		BotTemplates: []BotTemplate{
			{BotID: "demo_v2_bot", Role: "plugin", DisplayName: "Demo V2", ActivationMode: "lazy"},
		},
		Dependencies: []DependencySpec{
			{
				Interface:    "nexus.groupchat.v1",
				VersionRange: "^1.0.0",
				Optional:     false,
				Reason:       "调用群聊接口",
			},
		},
	}
	if err := ValidateManifest(v2Mf); err != nil {
		t.Fatalf("Manifest v2 校验失败: %v", err)
	}

	// 非法 dependencies 导致 Manifest 校验失败
	badMf := *v2Mf
	badMf.Dependencies = []DependencySpec{
		{Interface: "bad interface", VersionRange: "bad range"},
	}
	if err := ValidateManifest(&badMf); err == nil {
		t.Errorf("非法 dependencies 未使 Manifest 校验失败")
	} else if !strings.Contains(err.Error(), "依赖声明校验失败") {
		t.Errorf("未按预期提示依赖声明校验失败: %v", err)
	}
}
