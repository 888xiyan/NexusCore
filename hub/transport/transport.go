// Package transport 是内核传输路由：反向 TCP 会话、transport_frame 校验、多路复用与心跳。
// 依据：内核 MVP 详设 §3.8、架构总结 §5.1。
// 内核只校验 length / protocol_id / stream_id，payload 是插件自定义字节，绝不解析。
// UDP 完全延后。
package transport

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/888xiyan/NexusCore/hub/registry"
	"github.com/888xiyan/NexusCore/hub/store"
	"github.com/jackc/pgx/v5"
)

const (
	MaxFrameSize       = 4 << 20 // 4MB
	HeartbeatInterval  = 15 * time.Second
	HeartbeatMissLimit = 2

	FrameAuth      = "auth"
	FrameAuthAck   = "auth_ack"
	FrameOpen      = "open"
	FrameOpenAck   = "open_ack"
	FrameData      = "data"
	FrameAck       = "ack"
	FrameHeartbeat = "heartbeat"
	FrameClose     = "close"
)

var (
	ErrFrameTooLarge   = errors.New("transport_frame 超过长度上限")
	ErrProtocolDenied  = errors.New("protocol_id 未授权")
	ErrStreamUnknown   = errors.New("stream_id 未注册")
	ErrAuthRequired    = errors.New("首帧必须是鉴权帧")
	ErrPeerUnavailable = errors.New("目标对端不可用")
)

// Frame 是最小传输信封；Payload 由插件协议解释。
type Frame struct {
	ProtocolID      string `json:"protocol_id,omitempty"`
	ProtocolVersion uint16 `json:"protocol_version,omitempty"`
	StreamID        uint32 `json:"stream_id,omitempty"`
	FrameType       string `json:"frame_type"`
	Payload         []byte `json:"-"`
}

type frameHeader struct {
	ProtocolID      string `json:"protocol_id,omitempty"`
	ProtocolVersion uint16 `json:"protocol_version,omitempty"`
	StreamID        uint32 `json:"stream_id,omitempty"`
	FrameType       string `json:"frame_type"`
}

// WriteFrame 线格式：[uint32 headerLen][header JSON][uint32 payloadLen][payload]。
func WriteFrame(w io.Writer, f Frame) error {
	hdr, err := json.Marshal(frameHeader{
		ProtocolID: f.ProtocolID, ProtocolVersion: f.ProtocolVersion,
		StreamID: f.StreamID, FrameType: f.FrameType,
	})
	if err != nil {
		return fmt.Errorf("序列化帧头: %w", err)
	}
	total := 8 + len(hdr) + len(f.Payload)
	if total > MaxFrameSize {
		return fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, total, MaxFrameSize)
	}
	buf := make([]byte, 8+len(hdr)+len(f.Payload))
	binary.BigEndian.PutUint32(buf[0:4], uint32(len(hdr)))
	copy(buf[4:4+len(hdr)], hdr)
	binary.BigEndian.PutUint32(buf[4+len(hdr):8+len(hdr)], uint32(len(f.Payload)))
	copy(buf[8+len(hdr):], f.Payload)
	_, err = w.Write(buf)
	return err
}

// ReadFrame 读取并校验长度上限；不解析 payload。
func ReadFrame(r io.Reader) (Frame, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return Frame{}, err
	}
	hdrLen := binary.BigEndian.Uint32(head[:])
	if hdrLen > MaxFrameSize {
		return Frame{}, fmt.Errorf("%w: 帧头 %d", ErrFrameTooLarge, hdrLen)
	}
	hdr := make([]byte, hdrLen)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return Frame{}, fmt.Errorf("读取帧头: %w", err)
	}
	var h frameHeader
	if err := json.Unmarshal(hdr, &h); err != nil {
		return Frame{}, fmt.Errorf("解析帧头: %w", err)
	}
	var pl [4]byte
	if _, err := io.ReadFull(r, pl[:]); err != nil {
		return Frame{}, fmt.Errorf("读取载荷长度: %w", err)
	}
	plLen := binary.BigEndian.Uint32(pl[:])
	if 8+int(hdrLen)+int(plLen) > MaxFrameSize {
		return Frame{}, fmt.Errorf("%w: %d", ErrFrameTooLarge, 8+int(hdrLen)+int(plLen))
	}
	payload := make([]byte, plLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Frame{}, fmt.Errorf("读取载荷: %w", err)
	}
	return Frame{ProtocolID: h.ProtocolID, ProtocolVersion: h.ProtocolVersion,
		StreamID: h.StreamID, FrameType: h.FrameType, Payload: payload}, nil
}

// OpenRequest 是 Bot 侧开流请求（payload 内的传输语义，非业务语义）。
type OpenRequest struct {
	TargetAgentID string `json:"target_agent_id"`
	ProtocolID    string `json:"protocol_id"`
	JobID         string `json:"job_id,omitempty"`
}

type stream struct {
	id         uint32
	protocolID string
	botConn    string
	agentConn  string
	jobID      string
}

type session struct {
	connID   string
	peer     config.Actor
	conn     net.Conn
	writeMu  sync.Mutex
	lastSeen time.Time
}

func (s *session) send(f Frame) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return WriteFrame(s.conn, f)
}

// Router 维护会话与流，按 stream_id 在 Bot 与 Agent 之间路由字节。
type Router struct {
	st       *store.Store
	reg      *registry.Registry
	mu       sync.Mutex
	sessions map[string]*session
	streams  map[uint32]*stream
	nextID   uint32
	nextConn uint64
	// OnViolation 记录协议违规审计（由 hub 接到 ingress / 日志）
	OnViolation func(peer, reason string)
}

func NewRouter(st *store.Store, reg *registry.Registry) *Router {
	return &Router{st: st, reg: reg, sessions: map[string]*session{}, streams: map[uint32]*stream{}, nextID: 1}
}

// pluginOfBot 读取 Bot 的插件归属；无归属返回空串（内核侧 Bot）。
func (rt *Router) pluginOfBot(botID string) (string, error) {
	var owner *string
	err := rt.st.Pool().QueryRow(context.Background(),
		`SELECT parent_plugin FROM bot_manifest WHERE bot_id=$1`, botID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("%w: bot %q 未注册", ErrProtocolDenied, botID)
	}
	if err != nil {
		return "", fmt.Errorf("查询 bot 插件归属: %w", err)
	}
	if owner == nil {
		return "", nil
	}
	return *owner, nil
}

// Serve 接受反向连接，阻塞直到 ctx 结束。
func (rt *Router) Serve(ctx context.Context, ln net.Listener) error {
	go rt.heartbeatLoop(ctx)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("接受传输连接: %w", err)
		}
		go rt.handleConn(ctx, conn)
	}
}

func (rt *Router) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	first, err := ReadFrame(conn)
	if err != nil {
		return
	}
	if first.FrameType != FrameAuth {
		rt.violate("", ErrAuthRequired.Error())
		return
	}
	actor, _, err := rt.reg.VerifyCredential(ctx, string(first.Payload))
	if err != nil {
		rt.violate("", "传输鉴权失败: "+err.Error())
		return
	}
	rt.mu.Lock()
	rt.nextConn++
	connID := fmt.Sprintf("%s#%d", actor.ID, rt.nextConn)
	sess := &session{connID: connID, peer: actor, conn: conn, lastSeen: time.Now()}
	rt.sessions[connID] = sess
	rt.mu.Unlock()

	defer rt.closeSession(connID, "connection_closed")

	if err := sess.send(Frame{FrameType: FrameAuthAck, Payload: []byte(connID)}); err != nil {
		return
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(HeartbeatInterval * (HeartbeatMissLimit + 1)))
		f, err := ReadFrame(conn)
		if err != nil {
			return
		}
		rt.mu.Lock()
		sess.lastSeen = time.Now()
		rt.mu.Unlock()
		if err := rt.dispatch(ctx, sess, f); err != nil {
			rt.violate(actor.ID, err.Error())
			return
		}
	}
}

func (rt *Router) dispatch(ctx context.Context, sess *session, f Frame) error {
	switch f.FrameType {
	case FrameHeartbeat:
		return sess.send(Frame{FrameType: FrameHeartbeat})
	case FrameOpen:
		return rt.openStream(sess, f)
	case FrameData, FrameAck:
		return rt.route(sess, f)
	case FrameClose:
		return rt.closeStream(sess, f.StreamID)
	default:
		return fmt.Errorf("非法 frame_type %q", f.FrameType)
	}
}

func (rt *Router) openStream(sess *session, f Frame) error {
	var req OpenRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		return fmt.Errorf("解析开流请求: %w", err)
	}
	if req.ProtocolID == "" || req.TargetAgentID == "" {
		return errors.New("开流必须携带 protocol_id 与 target_agent_id")
	}
	// protocol_id 授权：Bot 的插件归属取自 bot_manifest.parent_plugin，不臆造。
	// 无插件归属的内核夹具 Bot 只允许预置夹具协议；真实插件协议必须经 3.4 授权。
	if sess.peer.Kind == config.KindBot {
		owner, err := rt.pluginOfBot(sess.peer.ID)
		if err != nil {
			return err
		}
		if owner == "" {
			if !isKernelFixtureProtocol(req.ProtocolID) {
				return fmt.Errorf("%w: 内核夹具 Bot 只允许夹具协议，收到 %s", ErrProtocolDenied, req.ProtocolID)
			}
		} else {
			ok, err := rt.reg.HasGrant(context.Background(), owner, "transport", req.ProtocolID)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("%w: %s", ErrProtocolDenied, req.ProtocolID)
			}
		}
	}
	rt.mu.Lock()
	agentSess := rt.findSessionLocked(req.TargetAgentID, config.KindAgent)
	if agentSess == nil {
		rt.mu.Unlock()
		return fmt.Errorf("%w: agent %s", ErrPeerUnavailable, req.TargetAgentID)
	}
	id := rt.nextID
	rt.nextID++
	st := &stream{id: id, protocolID: req.ProtocolID, botConn: sess.connID, agentConn: agentSess.connID, jobID: req.JobID}
	rt.streams[id] = st
	rt.mu.Unlock()

	if err := sess.send(Frame{FrameType: FrameOpenAck, StreamID: id, ProtocolID: req.ProtocolID, Payload: f.Payload}); err != nil {
		return err
	}
	// 通知 Agent 侧建立对应逻辑流
	return agentSess.send(Frame{FrameType: FrameOpen, StreamID: id, ProtocolID: req.ProtocolID, Payload: f.Payload})
}

func (rt *Router) route(sess *session, f Frame) error {
	rt.mu.Lock()
	st, ok := rt.streams[f.StreamID]
	if !ok {
		rt.mu.Unlock()
		return fmt.Errorf("%w: %d", ErrStreamUnknown, f.StreamID)
	}
	if f.ProtocolID != "" && f.ProtocolID != st.protocolID {
		rt.mu.Unlock()
		return fmt.Errorf("%w: 流 %d 绑定 %s，收到 %s", ErrProtocolDenied, st.id, st.protocolID, f.ProtocolID)
	}
	var target *session
	if sess.connID == st.botConn {
		target = rt.sessions[st.agentConn]
	} else if sess.connID == st.agentConn {
		target = rt.sessions[st.botConn]
	} else {
		rt.mu.Unlock()
		return fmt.Errorf("%w: 会话不属于流 %d", ErrStreamUnknown, st.id)
	}
	rt.mu.Unlock()
	if target == nil {
		return fmt.Errorf("%w: 流 %d 对端已断开", ErrPeerUnavailable, st.id)
	}
	// 原样转发 payload，内核不解析
	return target.send(Frame{ProtocolID: st.protocolID, StreamID: st.id, FrameType: f.FrameType, Payload: f.Payload})
}

func (rt *Router) closeStream(sess *session, streamID uint32) error {
	rt.mu.Lock()
	st, ok := rt.streams[streamID]
	if !ok {
		rt.mu.Unlock()
		return fmt.Errorf("%w: %d", ErrStreamUnknown, streamID)
	}
	delete(rt.streams, streamID)
	bot, agent := rt.sessions[st.botConn], rt.sessions[st.agentConn]
	rt.mu.Unlock()
	closeFrame := Frame{FrameType: FrameClose, StreamID: streamID, ProtocolID: st.protocolID}
	if bot != nil && bot.connID != sess.connID {
		_ = bot.send(closeFrame)
	}
	if agent != nil && agent.connID != sess.connID {
		_ = agent.send(closeFrame)
	}
	return nil
}

func (rt *Router) closeSession(connID, reason string) {
	rt.mu.Lock()
	delete(rt.sessions, connID)
	var affected []*stream
	for id, st := range rt.streams {
		if st.botConn == connID || st.agentConn == connID {
			affected = append(affected, st)
			delete(rt.streams, id)
		}
	}
	var peers []*session
	for _, st := range affected {
		if s := rt.sessions[st.botConn]; s != nil {
			peers = append(peers, s)
		}
		if s := rt.sessions[st.agentConn]; s != nil {
			peers = append(peers, s)
		}
	}
	rt.mu.Unlock()
	// 本连接所有 stream 按协议错误关闭，不影响其他插件与内核
	for i, st := range affected {
		_ = peers[i].send(Frame{FrameType: FrameClose, StreamID: st.id, ProtocolID: st.protocolID, Payload: []byte(reason)})
	}
}

func (rt *Router) findSessionLocked(peerID, kind string) *session {
	for _, s := range rt.sessions {
		if s.peer.ID == peerID && s.peer.Kind == kind {
			return s
		}
	}
	return nil
}

func (rt *Router) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rt.mu.Lock()
			var dead []string
			cutoff := time.Now().Add(-HeartbeatInterval * HeartbeatMissLimit)
			for id, s := range rt.sessions {
				if s.lastSeen.Before(cutoff) {
					dead = append(dead, id)
				}
			}
			rt.mu.Unlock()
			for _, id := range dead {
				rt.violate(id, "心跳丢失超过 2 次，判定连接失联")
				rt.closeSession(id, "heartbeat_lost")
			}
		}
	}
}

func (rt *Router) violate(peer, reason string) {
	if rt.OnViolation != nil {
		rt.OnViolation(peer, reason)
	}
}

// SessionCount / StreamCount 供 /metrics 使用。
func (rt *Router) SessionCount() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return len(rt.sessions)
}

func (rt *Router) StreamCount() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return len(rt.streams)
}

// isKernelFixtureProtocol 只放行 M1 冒烟夹具协议；真实插件协议必须经 3.4 授权。
func isKernelFixtureProtocol(protocolID string) bool { return protocolID == "nexus.echo.v1" }
