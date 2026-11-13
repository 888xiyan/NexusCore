// Package ingress 是内核统一入口：IngressEnvelope 校验、三类决策同事务落审计、
// 加密上下文快照与载荷存储。依据：内核 MVP 详设 §3.4、架构总结 §6.10。
// 入口不解析业务载荷；接收 bot 按 payload_ref 自行拉取，读取授权由其收到的信封绑定。
package ingress

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/888xiyan/NexusCore/hub/outbox"
	"github.com/888xiyan/NexusCore/hub/registry"
	"github.com/888xiyan/NexusCore/hub/scheduler"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/888xiyan/NexusCore/internal/envelope"
	"github.com/jackc/pgx/v5"
)

// SnapshotKeyEnv 是 MVP 单把用户密钥（AES-GCM），由 env 注入。
const SnapshotKeyEnv = "NEXUS_KERNEL_SNAPSHOT_KEY"

// PayloadDirEnv 指定加密载荷与快照的本地存储目录（MVP 单机）。
const PayloadDirEnv = "NEXUS_KERNEL_PAYLOAD_DIR"

const (
	DecisionAccepted  = "accepted"
	DecisionRejected  = "rejected"
	DecisionDuplicate = "duplicate"
)

const snapshotRetention = 30 * 24 * time.Hour

var ErrPayloadForbidden = errors.New("载荷拉取未授权")

type Ingress struct {
	st        *store.Store
	reg       *registry.Registry
	sched     *scheduler.Scheduler
	key       []byte
	keyID     string
	dir       string
	retention time.Duration
}

// Route 是入口适配器在创建 job 时携带的路由提示（架构总结 §6.6）。
// 它是通讯元数据，不进信封、不来自业务载荷解析；内核据此寻址，不解释业务语义。
type Route struct {
	QueueName    string
	BotID        string
	JobType      string
	PartitionKey string
	ProjectID    string
}

type Options struct {
	SnapshotKey []byte
	KeyID       string
	PayloadDir  string
	Retention   time.Duration
}

func New(st *store.Store, reg *registry.Registry, sched *scheduler.Scheduler, opt Options) (*Ingress, error) {
	if len(opt.SnapshotKey) != 32 {
		return nil, fmt.Errorf("快照密钥必须是 32 字节（AES-256-GCM），当前 %d 字节", len(opt.SnapshotKey))
	}
	if opt.PayloadDir == "" {
		return nil, errors.New("必须指定加密载荷存储目录")
	}
	if err := os.MkdirAll(opt.PayloadDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建载荷目录: %w", err)
	}
	ret := opt.Retention
	if ret <= 0 {
		ret = snapshotRetention
	}
	keyID := opt.KeyID
	if keyID == "" {
		keyID = "kernel-snapshot-v1"
	}
	return &Ingress{st: st, reg: reg, sched: sched, key: opt.SnapshotKey, keyID: keyID, dir: opt.PayloadDir, retention: ret}, nil
}

// OptionsFromEnv 从 env 读取密钥与目录；缺失即报错，不生成内置默认密钥。
func OptionsFromEnv() (Options, error) {
	key := os.Getenv(SnapshotKeyEnv)
	if len(key) != 32 {
		return Options{}, fmt.Errorf("未设置 %s 或长度不是 32 字节", SnapshotKeyEnv)
	}
	dir := os.Getenv(PayloadDirEnv)
	if dir == "" {
		return Options{}, fmt.Errorf("未设置 %s", PayloadDirEnv)
	}
	return Options{SnapshotKey: []byte(key), PayloadDir: dir}, nil
}

// Result 是入口处理结果；三类决策都已落审计。
type Result struct {
	Decision       string
	EnvelopeID     string
	LogID          int64
	SnapshotID     string
	PayloadRef     string
	Reason         string
	Code           envelope.Code
	IdempotencyHit bool
}

// Submit 是所有入口消息的唯一通道。
// 校验顺序：schema_version → 必填字段 → 凭证 → causal_path 连续性 / 深度 → 幂等键去重。
// 接受 / 拒绝 / 重复三种结果都在与接收同事务内写 kernel_ingress_log。
func (i *Ingress) Submit(ctx context.Context, env *envelope.Envelope, credential string, payload []byte, route Route) (*Result, error) {
	res := &Result{EnvelopeID: env.EnvelopeID}

	// 1) 信封结构与因果链校验（凭证之前，拒绝也要留完整现场）
	if err := env.Validate(); err != nil {
		var ve *envelope.ValidationError
		if errors.As(err, &ve) {
			res.Code = ve.Code
			res.Reason = ve.Error()
		} else {
			res.Reason = err.Error()
		}
		res.Decision = DecisionRejected
		if logErr := i.writeLog(ctx, env, res, "", ""); logErr != nil {
			return nil, logErr
		}
		return res, err
	}

	// 2) 凭证校验：必须与 from_bot_id 一致
	actor, err := i.verifyActor(ctx, env, credential)
	if err != nil {
		res.Decision = DecisionRejected
		res.Code = envelope.CodeCausalRequired
		res.Reason = err.Error()
		if logErr := i.writeLog(ctx, env, res, "", ""); logErr != nil {
			return nil, logErr
		}
		return res, err
	}

	// 3) 幂等去重 + 载荷/快照落盘 + 审计，全部同事务
	var payloadRef, snapshotID string
	err = i.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		dup, err := i.isDuplicate(ctx, tx, env.IdempotencyKey)
		if err != nil {
			return err
		}
		if dup {
			res.Decision = DecisionDuplicate
			res.IdempotencyHit = true
			res.Reason = "idempotency_key 已存在"
			res.Code = envelope.CodeDuplicate
			return i.writeLogTx(ctx, tx, env, res, "", "")
		}
		if len(payload) > 0 {
			ref, err := i.storePayload(ctx, tx, env, payload)
			if err != nil {
				return err
			}
			payloadRef = ref
		}
		sid, err := i.storeSnapshot(ctx, tx, env, actor, payload)
		if err != nil {
			return err
		}
		snapshotID = sid
		res.Decision = DecisionAccepted
		res.PayloadRef = payloadRef
		res.SnapshotID = snapshotID
		if err := i.writeLogTx(ctx, tx, env, res, snapshotID, payloadRef); err != nil {
			return err
		}
		// 详设 §6.1 第 3 步：接受的 request 信封同事务入队 job。
		// 幂等键沿用信封的 idempotency_key，重复投递不会产生重复任务。
		if env.MsgType == envelope.MsgRequest && route.QueueName != "" {
			jobType := route.JobType
			if jobType == "" {
				jobType = "bot.request"
			}
			if _, err := i.sched.EnqueueTxIfAbsent(ctx, tx, scheduler.EnqueueRequest{
				QueueName:      route.QueueName,
				JobType:        jobType,
				BotID:          route.BotID,
				ProjectID:      route.ProjectID,
				PartitionKey:   route.PartitionKey,
				EnvelopeID:     env.EnvelopeID,
				CorrelationID:  env.CorrelationID,
				CausationID:    env.CausationID,
				RootEnvelopeID: env.RootEnvelopeID,
				CausalPath:     causalPathJSON(env),
				Traceparent:    env.Traceparent,
				IdempotencyKey: env.IdempotencyKey,
				PayloadHash:    env.PayloadHash,
				PayloadRef:     payloadRef,
			}); err != nil {
				return fmt.Errorf("入口入队: %w", err)
			}
		}
		// Bot 的业务输出以 reply 信封投递，同事务写 bot.reply 持久事件（详设 §6.1 第 7 步）。
		// 内核只按通讯语义转发，不解析业务载荷。
		if env.MsgType == envelope.MsgReply {
			summary, err := json.Marshal(map[string]any{
				"envelope_id": env.EnvelopeID, "from_bot_id": env.FromBotID,
				"payload_ref": payloadRef, "channel": env.Channel,
			})
			if err != nil {
				return fmt.Errorf("序列化回复事件摘要: %w", err)
			}
			if err := outbox.Publish(ctx, tx, outbox.Event{
				EventID: "evt_" + randomHex(12), EventType: "bot.reply",
				CorrelationID: env.CorrelationID, CausationID: env.EnvelopeID,
				RootEnvelopeID: env.RootEnvelopeID,
				AggregateType:  "job", AggregateID: env.EnvelopeID,
				PayloadJSON: summary,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (i *Ingress) verifyActor(ctx context.Context, env *envelope.Envelope, credential string) (config.Actor, error) {
	if credential == "" {
		return config.Actor{}, fmt.Errorf("%w: 缺少凭证", registry.ErrInvalidCredential)
	}
	actor, _, err := i.reg.VerifyCredential(ctx, credential)
	if err != nil {
		return config.Actor{}, err
	}
	if actor.ID != env.FromBotID {
		return config.Actor{}, fmt.Errorf("%w: 凭证主体 %q 与 from_bot_id %q 不一致",
			registry.ErrInvalidCredential, actor.ID, env.FromBotID)
	}
	return actor, nil
}

func (i *Ingress) isDuplicate(ctx context.Context, tx pgx.Tx, key string) (bool, error) {
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM kernel_ingress_log WHERE idempotency_key=$1 AND decision='accepted'`, key).Scan(&n); err != nil {
		return false, fmt.Errorf("查询幂等键: %w", err)
	}
	return n > 0, nil
}

func (i *Ingress) writeLog(ctx context.Context, env *envelope.Envelope, res *Result, snapshotID, payloadRef string) error {
	return i.st.WithKernelRuntimeTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return i.writeLogTx(ctx, tx, env, res, snapshotID, payloadRef)
	})
}

func (i *Ingress) writeLogTx(ctx context.Context, tx pgx.Tx, env *envelope.Envelope, res *Result, snapshotID, payloadRef string) error {
	pathJSON, err := json.Marshal(env.CausalPath)
	if err != nil {
		return fmt.Errorf("序列化因果路径: %w", err)
	}
	var receivedAt any
	if !env.ReceivedAt.IsZero() {
		receivedAt = env.ReceivedAt
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO kernel_ingress_log
		  (envelope_id, schema_version, msg_type, from_bot_id, channel, channel_ref,
		   correlation_id, causation_id, root_envelope_id, causal_path_json, traceparent,
		   idempotency_key, decision, reason, payload_hash, payload_ref, context_snapshot_id,
		   policy_version, received_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		RETURNING log_id`,
		nullable(env.EnvelopeID), env.SchemaVersion, nullable(env.MsgType), nullable(env.FromBotID),
		nullable(env.Channel), nullable(env.ChannelRef), nullable(env.CorrelationID),
		nullable(env.CausationID), nullable(env.RootEnvelopeID), string(pathJSON),
		nullable(env.Traceparent), nullable(env.IdempotencyKey), res.Decision, nullable(res.Reason),
		nullable(env.PayloadHash), nullable(payloadRef), nullable(snapshotID),
		nullable(env.PolicyVersion), receivedAt).Scan(&res.LogID)
	if err != nil {
		return fmt.Errorf("写入入口审计: %w", err)
	}
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// causalPathJSON 原样继承信封的祖先路径，内核不补造节点。
func causalPathJSON(env *envelope.Envelope) json.RawMessage {
	if len(env.CausalPath) == 0 {
		return nil
	}
	raw, err := json.Marshal(env.CausalPath)
	if err != nil {
		return nil
	}
	return raw
}

// storePayload 加密落盘业务载荷并登记引用；内核不解析内容。
func (i *Ingress) storePayload(ctx context.Context, tx pgx.Tx, env *envelope.Envelope, payload []byte) (string, error) {
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	if env.PayloadHash != "" && env.PayloadHash != hash {
		return "", &envelope.ValidationError{Code: envelope.CodeInvalidHash, Field: "payload_hash", Detail: "与实际载荷 sha256 不符"}
	}
	id := "pl_" + randomHex(12)
	uri, err := i.encryptToFile(id, payload)
	if err != nil {
		return "", err
	}
	expires := time.Now().Add(i.retention)
	if _, err := tx.Exec(ctx, `
		INSERT INTO kernel_payloads (payload_id, envelope_id, storage_uri, encryption_key_id, content_hash, size_bytes, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		id, env.EnvelopeID, uri, i.keyID, hash, len(payload), expires); err != nil {
		return "", fmt.Errorf("登记载荷引用: %w", err)
	}
	return id, nil
}

// storeSnapshot 写加密上下文快照；写入前脱敏（token / cookie / key 名单替换为引用）。
func (i *Ingress) storeSnapshot(ctx context.Context, tx pgx.Tx, env *envelope.Envelope, actor config.Actor, payload []byte) (string, error) {
	snapshot := map[string]any{
		"envelope":         env,
		"actor":            map[string]string{"kind": actor.Kind, "id": actor.ID},
		"payload_redacted": redact(payload),
		"policy_version":   env.PolicyVersion,
		"captured_at":      time.Now().UTC(),
	}
	raw, err := marshalNoEscape(snapshot)
	if err != nil {
		return "", fmt.Errorf("序列化上下文快照: %w", err)
	}
	sum := sha256.Sum256(raw)
	id := "snap_" + randomHex(12)
	uri, err := i.encryptToFile(id, raw)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO kernel_context_snapshots
		  (snapshot_id, envelope_id, content_hash, storage_uri, encryption_key_id, redaction_policy, size_bytes, expires_at)
		VALUES ($1,$2,$3,$4,$5,'default',$6,$7)`,
		id, env.EnvelopeID, hex.EncodeToString(sum[:]), uri, i.keyID, len(raw), time.Now().Add(i.retention)); err != nil {
		return "", fmt.Errorf("登记上下文快照: %w", err)
	}
	return id, nil
}

func (i *Ingress) encryptToFile(id string, plaintext []byte) (string, error) {
	block, err := aes.NewCipher(i.key)
	if err != nil {
		return "", fmt.Errorf("创建 AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("创建 GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("生成 nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	path := filepath.Join(i.dir, id+".bin")
	if err := os.WriteFile(path, sealed, 0o600); err != nil {
		return "", fmt.Errorf("写入加密对象: %w", err)
	}
	return "file://" + path, nil
}

func (i *Ingress) decryptFile(uri string) ([]byte, error) {
	path := strings.TrimPrefix(uri, "file://")
	if !strings.HasPrefix(path, i.dir) {
		return nil, fmt.Errorf("加密对象路径越界: %q", uri)
	}
	sealed, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取加密对象: %w", err)
	}
	block, err := aes.NewCipher(i.key)
	if err != nil {
		return nil, fmt.Errorf("创建 AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("创建 GCM: %w", err)
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errors.New("加密对象长度非法")
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("解密失败: %w", err)
	}
	return plain, nil
}

// FetchPayload 是接收 bot 的拉取入口：授权必须来自其收到的信封（内核按投递关系授予拉取权）。
// 投递关系以 kernel_jobs 中分配给该 bot 的任务为准；未收到的信封不能成为拉取依据。
func (i *Ingress) FetchPayload(ctx context.Context, actor config.Actor, payloadRef string) ([]byte, error) {
	var uri string
	var envelopeID string
	err := i.st.Pool().QueryRow(ctx, `SELECT storage_uri, envelope_id FROM kernel_payloads WHERE payload_id=$1`, payloadRef).
		Scan(&uri, &envelopeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: 载荷不存在", ErrPayloadForbidden)
	}
	if err != nil {
		return nil, fmt.Errorf("查询载荷: %w", err)
	}
	authorized, err := i.deliveryAuthorized(ctx, actor, payloadRef, envelopeID)
	if err != nil {
		return nil, err
	}
	if !authorized {
		return nil, fmt.Errorf("%w: %s 未收到引用该载荷的信封", ErrPayloadForbidden, actor.ID)
	}
	return i.decryptFile(uri)
}

func (i *Ingress) deliveryAuthorized(ctx context.Context, actor config.Actor, payloadRef, envelopeID string) (bool, error) {
	if actor.Kind == config.KindKernelRole && actor.ID == config.UserBot {
		return true, nil // 审计角色可查全量
	}
	var n int
	err := i.st.Pool().QueryRow(ctx, `
		SELECT count(*) FROM kernel_jobs
		 WHERE bot_id=$1 AND (payload_ref=$2 OR envelope_id=$3)`, actor.ID, payloadRef, envelopeID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("校验投递关系: %w", err)
	}
	return n > 0, nil
}

// redact 对载荷做字段级脱敏：敏感字段替换为 <redacted:field> 引用。
// 内核不解析业务语义，只按字段名名单脱敏，原文仍完整加密保存于快照之外不入日志。
func redact(payload []byte) any {
	var v any
	if err := json.Unmarshal(payload, &v); err != nil {
		return "<non-json payload>"
	}
	return redactValue(v)
}

var redactFields = []string{"token", "cookie", "key", "password", "secret", "api_key", "authorization", "private_key"}

func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if isRedactField(k) {
				out[k] = "<redacted:" + k + ">"
				continue
			}
			out[k] = redactValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for idx, item := range t {
			out[idx] = redactValue(item)
		}
		return out
	default:
		return v
	}
}

func isRedactField(k string) bool {
	lower := strings.ToLower(k)
	for _, f := range redactFields {
		if lower == f || strings.HasSuffix(lower, "_"+f) || strings.Contains(lower, f) {
			return true
		}
	}
	return false
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("生成随机 ID 失败: %v", err))
	}
	return hex.EncodeToString(b)
}

// marshalNoEscape 保留 <redacted:field> 的字面形式，便于审计直接阅读。
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
