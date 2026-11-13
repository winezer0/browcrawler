// ws.go：WebSocket 帧捕获的存储结构。
//
// WS 是有状态会话协议，没有 HTTP 式单报文重放——交付形态为两级：
//  1. JSONL 帧流水（本文件）：一个会话 = created + handshake + N 帧记录 + closed，
//     以 request_id 关联，帧序号会话内递增；
//  2. 重放脚本（后续）：带录制握手头建连、按序重放发送帧。
//
// 帧捕获经 CDP Network 事件（rod），与代理链路无关，Obscura/Chrome 双后端通用。
package storage

import (
	"encoding/base64"
)

// WSInfo 是 WebSocket 记录的专属字段（Record.WS）。
type WSInfo struct {
	Event      string `json:"event"` // created|handshake|frame|closed|error
	RequestID  string `json:"request_id,omitempty"`
	Direction  string `json:"direction,omitempty"` // sent|received（仅 frame）
	Opcode     string `json:"opcode,omitempty"`    // text|binary|continue|close|ping|pong（仅 frame）
	FrameSeq   int    `json:"frame_seq,omitempty"` // 会话内帧序号（从 1 起，仅 frame）
	Code       int    `json:"code,omitempty"`      // 仅 closed
	Reason     string `json:"reason,omitempty"`    // 仅 closed
	Error      string `json:"error,omitempty"`     // 仅 error
	PayloadLen int    `json:"payload_len,omitempty"`
}

// WSOpcodeName 把 CDP opcode 数值映射为可读名（导出供驱动转换层复用）。
func WSOpcodeName(op float64) string {
	switch int(op) {
	case 0:
		return "continue"
	case 1:
		return "text"
	case 2:
		return "binary"
	case 8:
		return "close"
	case 9:
		return "ping"
	case 10:
		return "pong"
	default:
		return "unknown"
	}
}

// RecordWSEvent 把一条 WebSocket 事件写入 JSONL。payload 为帧载荷（文本帧
// 原文 / 二进制帧原始字节，由调用方决定编码），超过 maxBody 截断并标注。
// created/handshake/closed 事件 payload 为 nil。
func (r *Recorder) RecordWSEvent(wsURL string, info *WSInfo, payload []byte) {
	if info == nil {
		return
	}
	rec := &Record{
		URL:          wsURL,
		ResourceType: "websocket",
		WS:           info,
	}
	if len(payload) > 0 {
		rec.BodySize = int64(len(payload))
		if int64(len(payload)) > r.maxBody {
			payload = payload[:r.maxBody]
			rec.BodyOmitted = "ws_frame_truncated"
		}
		if info.Opcode == "binary" {
			rec.Body = base64.StdEncoding.EncodeToString(payload)
			rec.BodyEncoding = "base64"
		} else {
			rec.Body = string(payload)
		}
	}
	r.write(rec)
}
