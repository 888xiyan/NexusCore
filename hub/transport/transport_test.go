package transport

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// 线格式往返：内核只搬运字节，不解释 payload（硬约束 17）
func TestFrameRoundTripPreservesOpaquePayload(t *testing.T) {
	opaque := []byte{0x00, 0xff, 0x7b, 0x22, 0x01, 0xfe} // 任意二进制，含非 UTF-8
	in := Frame{ProtocolID: "nexus.echo.v1", ProtocolVersion: 1, StreamID: 7, FrameType: FrameData, Payload: opaque}
	var buf bytes.Buffer
	if err := WriteFrame(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if out.ProtocolID != in.ProtocolID || out.ProtocolVersion != in.ProtocolVersion ||
		out.StreamID != in.StreamID || out.FrameType != in.FrameType {
		t.Fatalf("帧头不符: %+v", out)
	}
	if !bytes.Equal(out.Payload, opaque) {
		t.Fatalf("payload 必须逐字节原样保留: %x", out.Payload)
	}
}

func TestFrameEmptyPayload(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, Frame{FrameType: FrameHeartbeat}); err != nil {
		t.Fatal(err)
	}
	out, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Payload) != 0 {
		t.Fatalf("心跳帧不应有载荷，实际 %d 字节", len(out.Payload))
	}
}

// 超过 4MB 上限：写侧直接拒绝，不产生半截帧
func TestWriteRejectsOversizeFrame(t *testing.T) {
	var buf bytes.Buffer
	err := WriteFrame(&buf, Frame{FrameType: FrameData, Payload: make([]byte, MaxFrameSize+1)})
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("超限帧应拒绝，实际 %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("拒绝后不应写出任何字节，实际 %d", buf.Len())
	}
}

// 读侧独立设防：恶意对端声明超长帧头 / 载荷时不得分配巨大缓冲
func TestReadRejectsOversizeHeader(t *testing.T) {
	var head [4]byte
	binary.BigEndian.PutUint32(head[:], MaxFrameSize+1)
	if _, err := ReadFrame(bytes.NewReader(head[:])); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("超长帧头应拒绝，实际 %v", err)
	}
}

func TestReadRejectsOversizePayloadLen(t *testing.T) {
	hdr := []byte(`{"frame_type":"data"}`)
	buf := make([]byte, 0, 8+len(hdr))
	var head [4]byte
	binary.BigEndian.PutUint32(head[:], uint32(len(hdr)))
	buf = append(buf, head[:]...)
	buf = append(buf, hdr...)
	var pl [4]byte
	binary.BigEndian.PutUint32(pl[:], MaxFrameSize)
	buf = append(buf, pl[:]...)
	if _, err := ReadFrame(bytes.NewReader(buf)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("超长载荷声明应拒绝，实际 %v", err)
	}
}

func TestReadRejectsTruncatedFrame(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, Frame{FrameType: FrameData, Payload: []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	full := buf.Bytes()
	for _, cut := range []int{0, 2, 6, len(full) - 1} {
		if _, err := ReadFrame(bytes.NewReader(full[:cut])); err == nil {
			t.Fatalf("截断到 %d 字节应报错", cut)
		}
	}
}

func TestReadRejectsMalformedHeaderJSON(t *testing.T) {
	hdr := []byte(`{不是 JSON`)
	buf := make([]byte, 0, 8+len(hdr))
	var head [4]byte
	binary.BigEndian.PutUint32(head[:], uint32(len(hdr)))
	buf = append(buf, head[:]...)
	buf = append(buf, hdr...)
	var pl [4]byte
	buf = append(buf, pl[:]...)
	if _, err := ReadFrame(bytes.NewReader(buf)); err == nil {
		t.Fatal("非法帧头 JSON 应报错")
	}
}

// 多帧连续读写：流式分帧不得错位
func TestMultipleFramesInSequence(t *testing.T) {
	var buf bytes.Buffer
	want := []string{"a", "bb", "ccc"}
	for i, p := range want {
		if err := WriteFrame(&buf, Frame{StreamID: uint32(i + 1), FrameType: FrameData, Payload: []byte(p)}); err != nil {
			t.Fatal(err)
		}
	}
	for i, p := range want {
		f, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("第 %d 帧: %v", i+1, err)
		}
		if string(f.Payload) != p || f.StreamID != uint32(i+1) {
			t.Fatalf("第 %d 帧错位: %+v", i+1, f)
		}
	}
	if _, err := ReadFrame(&buf); !errors.Is(err, io.EOF) {
		t.Fatalf("读尽后应为 EOF，实际 %v", err)
	}
}

func TestRouterOnFrameCallback(t *testing.T) {
	rt := NewRouter(nil, nil)
	var captured []FrameEvent
	rt.OnFrame = func(evt FrameEvent) {
		captured = append(captured, evt)
	}

	testEvt := FrameEvent{
		StreamID:   10,
		JobID:      "job_100",
		BotID:      "ops_bot",
		AgentID:    "loopback_agent",
		ProtocolID: "nexus.echo.v1",
		FrameType:  "open",
		Status:     "running",
		Payload:    "open request",
		ElapsedMS:  0,
		Timestamp:  "2026-11-14T04:00:00Z",
	}
	rt.emitFrame(testEvt)

	if len(captured) != 1 {
		t.Fatalf("expected 1 captured event, got %d", len(captured))
	}
	if captured[0].StreamID != 10 || captured[0].FrameType != "open" || captured[0].BotID != "ops_bot" {
		t.Fatalf("unexpected captured event: %+v", captured[0])
	}
}
