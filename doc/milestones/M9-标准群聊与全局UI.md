# M9 详细设计定版：标准群聊插件（plugin_groupchat）与全局 UI 动态注册体系

> **文档状态**：正式定版归档（Approved & Finalized）  
> **版本标识**：NexusCore v0.1.0 旗舰核心  
> **定版日期**：2026-11-15  
> **核心主题**：将群聊插件确立为 NexusCore v0.1.0 核心业务枢纽，全面整合前期零散功能（多 Bot 群聊协同、Agent 物理探针调度、自定义 Bot 蓝图工厂、Bot 成员档案与单体记忆治理、PostgreSQL 独立 Schema 聊天记录存储）；并严格贯彻**“业务插件负责自己 UI，注册进全局 UI”**的标准微前端插件架构。

---

## 1. 业务愿景与战略定位

### 1.1 从“零散功能拼盘”走向“一体化协同智能工作台”
在 M3～M6 的渐进式演进中，NexusCore 验证了多项核心技术原语（微内核队列、单体记忆、探针调度、Bot 蓝图派生），但各功能在前端呈现与业务交互上存在割裂：
- 用户在独立抽屉中创建 Bot；
- 在独立监控面板中观察 Agent 探针日志；
- 在内存级群聊视区中发送消息，重启后聊天记录丢失。

**在 v0.1.0 中，标准群聊插件（`plugin_groupchat`）被确立为系统的核心业务承载体**。群聊是最高频的人机交互入口，先前分散的五大能力被有机高聚合并内嵌至群聊工作流中：
1. **多 Bot 群聊协同（Multi-Bot GroupChat）**：多房间与话题、Markdown 流式渲染、严格用户 @ 触发；
2. **内嵌式 Agent 探针执行流（Inline Agent Trace）**：Bot 调度物理探针执行时，终端流式回显直接内嵌折叠在对应消息气泡中；
3. **自定义 Bot 蓝图工厂（Integrated Bot Creator）**：群聊内一键向导派生新 Bot，单 API Key 统筹，直接勾选绑定已探测的本地 Agent；
4. **Bot 成员档案与单体记忆可视化治理（Bot Profile & Memory Drawer）**：实时查看/编辑 Persona、System Prompt 及 `MEMORY.md`（严格 ≤ 200 行校验），提供“保存并优雅重启”直接生效；
5. **PostgreSQL 独立 Schema 聊天记录存储（Persistent Chat History）**：独立 `groupchat` schema，全量持久化房间、时序消息、@ 唤醒审计与因果链追溯，支持增量漫游。

### 1.2 核心架构铁律：“业务插件负责自己 UI，注册进全局 UI”
- **宿主壳纯净化（Pure Host Shell）**：前端 `Frontend/` 仅保留 Header、Sidebar、全局状态总线与插件注册中心（`PluginRegistry`），**100% 剥离具体业务视区**；
- **业务插件全栈自治（Autonomous Business Plugin）**：
  - 后端：独立 L2 二进制服务（`:7300`），持有独立 PG 连接池与 DSN；
  - 前端：独立源码目录（`GroupChatPlugin/frontend`），独立打包为 ESM 模块包，由 L2 二进制在 `:7300/ui/` 提供内嵌静态托管；
  - 动态注册：插件遵循 `NexusPluginUI` 契约，宿主通过原生 `import()` 动态装载并挂载菜单与路由。

---

## 2. 总体架构拓扑与两级进程模型

```mermaid
flowchart TD
    subgraph HostShell["全局前端宿主 (Frontend Host Shell)"]
        NAV["顶部全局状态与通知栏"]
        SIDEBAR["Sidebar 动态导航菜单 (由注册表生成)"]
        VIEWPORT["工作台核心视区 (<RouterView />)"]
        REGISTRY["前端插件注册中心 (PluginRegistry)"]
        SHARED_LIBS["共享运行时底座 (Vue 3 / Pinia / Element Plus)"]
    end

    subgraph L1_BotLayer["L1: Owner Bot 进程 (bot-runtime)"]
        OWNER_BOT["groupchat_bot<br/>(总线常驻席位 / 凭证校验 / 委托投递代理)"]
    end

    subgraph L2_PluginLayer["L2: 插件独立服务进程 (plugin_groupchat :7300)"]
        API_WS["HTTP REST & WebSocket 实时双工网关"]
        ROUTER_GUARD["防死循环 @ 路由守卫 (仅用户@触发)"]
        CONTEXT_ENGINE["滑动窗口与单体记忆装配引擎"]
        STATIC_HOST["内嵌静态资源服务 (/ui/index.js)"]
    end

    subgraph PluginUI["GroupChat 插件自治前端 (ESM 模块包)"]
        ROOMS["多房间与话题管理"]
        CHAT_STREAM["时序消息流与 @Mention"]
        AGENT_CARD["消息流内嵌 Agent 探针卡片"]
        BOT_DRAWER["Bot 成员档案与 200 行记忆抽屉"]
        BOT_CREATOR["房间内嵌 Bot 创建向导"]
    end

    subgraph StorageLayer["独立存储层 (PostgreSQL)"]
        PG_GC[(schema: groupchat<br/>chat_rooms / chat_messages / chat_mentions)]
    end

    subgraph KernelLayer["微内核底座 (Kernel Hub :7200)"]
        HUB["Hub 微内核 (任务队列 / Ingress / 因果链)"]
        SUPERVISOR["Supervisor (Bot 进程守护与重启)"]
        TRANSPORT["Transport Router (反向 TCP 探针通道)"]
        CONFIG_CENTER["配置中心 (全域凭证 / 接口契约)"]
    end

    %% 流程关系
    SUPERVISOR ==>|1. 拉起并注入凭证| OWNER_BOT
    OWNER_BOT ==>|2. setsid 独立进程组 exec 拉起| API_WS
    API_WS --- STATIC_HOST
    STATIC_HOST -.->|编译托管| PluginUI

    REGISTRY ==>|3. 原生 dynamic import 载入| PluginUI
    PluginUI ==>|4. install(context) 注入菜单与路由| REGISTRY
    REGISTRY -.->|5. 动态挂载路由| VIEWPORT
    REGISTRY -.->|6. 动态挂载菜单| SIDEBAR

    PluginUI <==>|7. REST & WebSocket| API_WS
    API_WS <==>|8. 专属连接池| PG_GC
    API_WS ==>|9. 用户@唤醒 -> Ingress 任务投递| HUB
    API_WS -.->|10. 触发 Bot 优雅重启| SUPERVISOR
    API_WS -.->|11. 探针终端执行帧中继| TRANSPORT
    API_WS -.->|12. 提交 Bot 蓝图提案| CONFIG_CENTER
```

---

## 3. 全局 UI 插件注册机制与宿主契约

### 3.1 运行时依赖共享（Zero-Redundancy Runtime Sharing）
为避免业务插件重复打包大型运行时库，宿主在全局暴露核心底座实例：
- 宿主 `main.ts` 初始化时将 `Vue`、`Pinia`、`ElementPlus`、`Router` 挂载至 `window.__NEXUS_CORE__`；
- 插件前端 `vite.config.ts` 将核心库标记为 `external`，产物尺寸严格保持在 **100KB~300KB** 级；
- 插件完全共享宿主的主题 CSS 变量与 Pinia 用户态，零样式割裂。

### 3.2 宿主与插件契约接口规范

```typescript
// 宿主提供给业务插件的注册上下文 (NexusUIContext)
export interface NexusUIContext {
  // 1. 动态路由注入
  registerRoute(route: {
    path: string;
    name: string;
    component: any;
    meta?: Record<string, any>;
  }): void;

  // 2. 侧边栏菜单注册（支持动态响应式未读角标）
  registerMenu(menu: {
    id: string;
    title: string;
    icon: string;         // Element Plus 图标名称或 SVG
    path: string;
    order?: number;       // 排序权重
    badge?: () => number; // 未读消息数回调
  }): void;

  // 3. 全局扩展插槽注入（顶部栏、工作台状态区）
  registerSlotComponent(slotName: 'header_status' | 'workbench_widget', component: any): void;

  // 4. 全局总线与事件监听
  bus: {
    on(event: string, handler: (payload: any) => void): void;
    off(event: string, handler: (payload: any) => void): void;
    emit(event: string, payload: any): void;
  };

  // 5. 全局轻提示工具
  notify: {
    success(msg: string): void;
    warning(msg: string): void;
    error(msg: string): void;
  };
}

// 业务插件自治前端必须导出的入口接口 (NexusPluginUI)
export interface NexusPluginUI {
  id: string;
  version: string;
  install(ctx: NexusUIContext): void | Promise<void>;
  uninstall?(ctx: NexusUIContext): void;
}
```

### 3.3 插件自治前端入口实现（`GroupChatPlugin/frontend/src/index.ts`）

```typescript
import type { NexusPluginUI, NexusUIContext } from '../../types/nexus-ui';
import GroupChatWorkspace from './views/GroupChatWorkspace.vue';
import { useGroupChatStore } from './stores/groupchat';

const plugin: NexusPluginUI = {
  id: 'plugin_groupchat',
  version: '1.0.0',

  install(ctx: NexusUIContext) {
    const store = useGroupChatStore();

    // 注册工作台路由
    ctx.registerRoute({
      path: '/groupchat',
      name: 'GroupChatWorkspace',
      component: GroupChatWorkspace,
      meta: { title: '群聊协同工作台' }
    });

    // 注册侧边栏菜单
    ctx.registerMenu({
      id: 'menu_groupchat',
      title: '群聊工作台',
      icon: 'ChatDotRound',
      path: '/groupchat',
      order: 1,
      badge: () => store.totalUnreadCount
    });

    // 订阅全局内核事件（如 Bot 状态变动）
    ctx.bus.on('kernel.bot.status_changed', (evt) => {
      store.updateBotStatus(evt.bot_id, evt.status);
    });
  },

  uninstall(ctx: NexusUIContext) {
    console.log('[plugin_groupchat] 前端微模块已安全卸载');
  }
};

export default plugin;
```

---

## 4. 数据架构设计（PostgreSQL `groupchat` Schema）

严格遵守存储边界：在 PostgreSQL `nexus` 库中开辟独立 `groupchat` schema，**微内核 hub 不持有、不访问该 schema**，完全由 `plugin_groupchat` 独立连接并执行迁移（`migrations/plugins/plugin_groupchat/groupchat/0001_init_groupchat.up.sql`）。

```sql
CREATE SCHEMA IF NOT EXISTS groupchat;

-- 1. 房间元数据表
CREATE TABLE groupchat.chat_rooms (
    room_id VARCHAR(64) PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    topic TEXT NOT NULL DEFAULT '',
    room_type VARCHAR(32) NOT NULL DEFAULT 'public' CHECK (room_type IN ('public', 'private', 'topic_thread')),
    status VARCHAR(32) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived', 'frozen')),
    created_by VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

-- 2. 房间成员关系表（人与 Bot 共同作为成员席位）
CREATE TABLE groupchat.chat_room_members (
    room_id VARCHAR(64) NOT NULL REFERENCES groupchat.chat_rooms(room_id) ON DELETE CASCADE,
    member_id VARCHAR(64) NOT NULL,
    member_type VARCHAR(16) NOT NULL CHECK (member_type IN ('user', 'bot')),
    role VARCHAR(32) NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'admin', 'member')),
    joined_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    muted_until TIMESTAMPTZ,
    PRIMARY KEY (room_id, member_id)
);
CREATE INDEX idx_chat_members_member ON groupchat.chat_room_members(member_id);

-- 3. 消息时序主表（持久化 + 严格因果溯源 + 探针执行帧引用）
CREATE TABLE groupchat.chat_messages (
    message_id VARCHAR(64) PRIMARY KEY,
    room_id VARCHAR(64) NOT NULL REFERENCES groupchat.chat_rooms(room_id) ON DELETE CASCADE,
    sender_id VARCHAR(64) NOT NULL,
    sender_role VARCHAR(16) NOT NULL CHECK (sender_role IN ('user', 'bot', 'system')),
    content_type VARCHAR(32) NOT NULL DEFAULT 'text' CHECK (content_type IN ('text', 'markdown', 'card', 'agent_trace', 'system_event')),
    content TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    reply_to_id VARCHAR(64),
    thread_root_id VARCHAR(64),
    -- 内核总线因果链审计挂点
    root_envelope_id VARCHAR(64),
    causal_path TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    is_deleted BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE INDEX idx_chat_messages_room_time ON groupchat.chat_messages(room_id, created_at DESC);
CREATE INDEX idx_chat_messages_thread ON groupchat.chat_messages(thread_root_id) WHERE thread_root_id IS NOT NULL;

-- 4. @ 唤醒与路由审计表（记录唤醒决策与防死循环审计证据）
CREATE TABLE groupchat.chat_mentions (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    message_id VARCHAR(64) NOT NULL REFERENCES groupchat.chat_messages(message_id) ON DELETE CASCADE,
    target_bot_id VARCHAR(64) NOT NULL,
    trigger_sender_role VARCHAR(16) NOT NULL,
    is_dispatched BOOLEAN NOT NULL DEFAULT FALSE,
    task_job_id VARCHAR(64),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX idx_chat_mentions_msg ON groupchat.chat_mentions(message_id);

-- 5. 未读状态与已读游标表
CREATE TABLE groupchat.chat_read_states (
    room_id VARCHAR(64) NOT NULL REFERENCES groupchat.chat_rooms(room_id) ON DELETE CASCADE,
    member_id VARCHAR(64) NOT NULL,
    last_read_message_id VARCHAR(64),
    last_read_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (room_id, member_id)
);

-- 6. 房间 Bot 个性化配置覆盖表（滑动窗口、提示词微调）
CREATE TABLE groupchat.chat_room_bot_configs (
    room_id VARCHAR(64) NOT NULL REFERENCES groupchat.chat_rooms(room_id) ON DELETE CASCADE,
    bot_id VARCHAR(64) NOT NULL,
    context_window_size INT NOT NULL DEFAULT 30 CHECK (context_window_size BETWEEN 5 AND 100),
    enable_thinking_stream BOOLEAN NOT NULL DEFAULT TRUE,
    system_prompt_override TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (room_id, bot_id)
);
```

---

## 5. 核心业务机制与安全铁律

### 5.1 代码级三层锁防死循环路由机制（Zero Cascade Loop）
为了彻底规避 Bot 之间相互 @ 引发的级联唤醒、死循环与 Token 二次方膨胀，路由引擎确立三层刚性规则：
1. **单向唤醒门**：在消息进入 `domain/mention` 解析时，严格检查 `sender_role == 'user'`。若不满足，路由候选列表直接截断为空；
2. **纯展示降级（Display-Only Downgrade）**：Bot 回复文本中出现的 `@another_bot`，在入库时仅作为富文本标签保存，**插件绝不向微内核 Ingress 发起任何二次任务调度**；
3. **审计对账断言**：`chat_mentions` 记录所有 @ 动作。如果出现 `trigger_sender_role == 'bot'` 且 `is_dispatched == TRUE`，断言校验直接触发异常熔断。

### 5.2 上下文滑动切片与 200 行单体记忆保护
简单 Agent（禁长思考、禁直接 Shell）需要精确而精简的上下文输入：
1. **滑动窗口切片**：根据房间配置截取最近 30 条时序消息；
2. **楼层溯源补偿**：若消息为特定回复，沿 `reply_to_id` / `thread_root_id` 自动提取原楼层关键对话片段；
3. **噪音压缩**：历史消息中的大型 Agent 探针日志压缩为一行摘要（Distilled Summary），防止冲刷模型上下文；
4. **单体记忆硬约束**：Bot 挂载的 `MEMORY.md` 严格维持 ≤ 200 行上限，确保 Prompt Caching 命中率高达 85%~93%。

---

## 6. 接口契约与 Manifest v2 规范（`GroupChatPlugin/manifest.json`）

```json
{
  "$schema": "https://nexuscore.local/schemas/plugin-manifest.v2.json",
  "plugin_id": "plugin_groupchat",
  "name": "Standard GroupChat Plugin",
  "version": "1.0.0",
  "plugin_type": "workspace",
  "description": "NexusCore v0.1.0 核心协同工作台，集成多 Bot 群聊、探针调度、Bot 蓝图工厂与持久化存储",
  "binary": "/home/yhy/Projects/NexusCore/bin/plugins/plugin_groupchat",
  "binary_digest": "AUTO_COMPUTED_SHA256_HEX",
  "schema_version": 2,
  "dependencies": [],
  "provides_interfaces": [
    {
      "interface_id": "nexus.groupchat.v1",
      "version": "1.0.0",
      "protocol": "http_rest_ws",
      "endpoint": "http://127.0.0.1:7300/v1/chat",
      "health_check": "http://127.0.0.1:7300/v1/chat/health",
      "capabilities": [
        "room_lifecycle",
        "persistent_messages",
        "inline_agent_trace",
        "bot_profile_governance",
        "bot_factory_integration",
        "websocket_realtime"
      ]
    }
  ],
  "publishing": [
    "plugin_groupchat.room.created",
    "plugin_groupchat.message.posted",
    "plugin_groupchat.mention.dispatched"
  ],
  "subscribing": [
    "bot.reply",
    "agent.execution.completed",
    "kernel.bot.status_changed"
  ],
  "providing_tools": [
    "groupchat.create_room",
    "groupchat.post_message",
    "groupchat.get_context_slice"
  ],
  "requiring_tools": [],
  "bot_templates": [
    {
      "bot_id": "groupchat_bot",
      "role": "plugin",
      "display_name": "GroupChat Coordinator Bot",
      "activation_mode": "resident",
      "capabilities": [
        "groupchat_management",
        "context_broker"
      ]
    }
  ],
  "ui": {
    "entry": "http://127.0.0.1:7300/ui/index.js",
    "style": "http://127.0.0.1:7300/ui/style.css",
    "menu": {
      "title": "群聊工作台",
      "icon": "ChatDotRound",
      "path": "/groupchat"
    }
  }
}
```

---

## 7. 实施演进规划（4 个竖切阶段，共 10-12 人日）

| 阶段 | 周期 | 核心开发任务 | 交付物与验证基准 |
| --- | --- | --- | --- |
| **Phase 1: 宿主纯进化与微模块注册表** | 2 人日 | 1. 清空 `Frontend/` 宿主中现存的业务视图，收敛为纯净 Shell<br/>2. 建立 `PluginRegistry`，实现基于原生 `import()` 的动态加载器<br/>3. 注入 `NexusUIContext` 契约，支持动态挂载 Mock 路由与菜单 | 宿主无任何内置业务代码但正常启动；控制台验证可成功动态载入外部 ESM 模块并渲染 |
| **Phase 2: 独立存储与后端领域聚合** | 3 人日 | 1. 编写独立 `groupchat` schema 迁移（6 张核心表）<br/>2. 实现 PostgreSQL 事务仓储与分页消息游标 API<br/>3. 代码级封死防死循环路由守卫与滑动上下文切片<br/>4. 内嵌静态资源服务（`/ui/` 路由） | `go test ./GroupChatPlugin/...`<br/>PG 真实数据持久化全绿；死循环注入测试全通 |
| **Phase 3: 插件自治前端一体化研发** | 3-4 人日 | 1. 搭建 `GroupChatPlugin/frontend`（Vite Library 构型）<br/>2. 将 Bot 抽屉（≤200行/Save&Restart）、Bot 工厂创建向导、探针卡片有机聚合至群聊主屏<br/>3. 打通 WebSocket 双工流（实时打字机流、探针终端帧中继）<br/>4. 打包输出 100KB 级纯净 `dist/index.js` | `npm run build`<br/>产出标准 ESM bundle，由 L2 二进制在 `:7300` 正常提供服务 |
| **Phase 4: 全链路装配与端到端冒烟** | 2 人日 | 1. 插件 Manifest v2 签名与配置中心接口注册<br/>2. 两级进程生命周期管控验证（先停 L2，后停 L1）<br/>3. 编排并固化 `make smoke-m9`（`make smoke-v0.1.0`）全链路自动化冒烟脚本 | `make smoke-m9` 10 步端到端自动化测试全绿，全要素协同实机演示通过 |

---

## 8. 自动化冒烟验收标准（`make smoke-m9`）

自动化冒烟脚本 `scripts/smoke-m9.sh` 覆盖全生命周期与业务场景：
1. **纯净宿主启动探活**：前端宿主首次加载，侧边栏无预置业务菜单；
2. **插件激活与动态入册**：提交安装 `plugin_groupchat` 并激活后，宿主感知激活事件，动态拉取 `http://127.0.0.1:7300/ui/index.js`，侧边栏无刷新实时出现“群聊工作台”；
3. **时序持久化验证**：在房间发送多条消息，重启插件 L2 进程，再次拉取消息历史完好无损；
4. **内嵌 Bot 蓝图派生**：在群聊视区内唤出创建向导，新建 `review_bot` 并绑定本地 Agent，验证配置提案闭环并成功加入当前房间；
5. **内嵌探针卡片流式回显**：用户发送 `@ops_bot 巡检`，消息流实时展开折叠终端卡片，流式打印终端输出；
6. **200 行记忆硬约束与毫秒级重启**：在 Bot 抽屉修改 `MEMORY.md`，199 行成功保存并触发 Supervisor 优雅重启，201 行即刻被前端与后端双重 400 拦截；
7. **死循环注入硬拦截**：注入模拟 Bot 输出包含 `@groupchat_bot @ops_bot` 的文本，断言新生成的任务分发数严格为 0；
8. **两级进程优雅停机**：执行 CLI `plugin stop plugin_groupchat`，宿主动态卸载菜单与路由，无白屏，无孤儿进程残留。
