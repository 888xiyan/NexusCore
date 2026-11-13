package room

import (
	"reflect"
	"testing"
)

func TestUserMentionRouting(t *testing.T) {
	r := NewRoom("room_01", "研发协作群", 10)

	// 1. 用户发送 @ops_bot
	msg1, routed1, err := r.Append("user_alice", RoleUser, "@ops_bot 检查构建环境并汇报")
	if err != nil {
		t.Fatalf("Append 失败: %v", err)
	}
	if !reflect.DeepEqual(routed1, []string{"ops_bot"}) {
		t.Fatalf("用户 @ops_bot 期望唤醒 [ops_bot], 实际 %v", routed1)
	}
	if len(msg1.Mentions) != 1 || msg1.Mentions[0] != "ops_bot" {
		t.Errorf("消息 Mentions 记录不符合预期: %v", msg1.Mentions)
	}

	// 2. Bot 回复中带 @qa_bot 文本，严禁唤醒任何下游 Bot！
	msg2, routed2, err := r.Append("ops_bot", RoleBot, "构建环境检查完成，请 @qa_bot 执行集成验收测试")
	if err != nil {
		t.Fatalf("Bot Append 失败: %v", err)
	}
	if len(routed2) != 0 {
		t.Fatalf("Bot 回复中的 @ 严禁唤醒任何 Bot，但返回了 %v (违反死循环防护铁律)", routed2)
	}
	if len(msg2.Mentions) != 1 || msg2.Mentions[0] != "qa_bot" {
		t.Errorf("展示属性 Mentions 仍应正确记录: %v", msg2.Mentions)
	}
}

func TestSlidingWindow(t *testing.T) {
	r := NewRoom("room_02", "测试群", 5)

	for i := 1; i <= 8; i++ {
		_, _, _ = r.Append("user", RoleUser, "消息测试")
	}

	msgs := r.GetMessages(10)
	if len(msgs) != 5 {
		t.Fatalf("滑动窗口期望最多保留 5 条，实际 %d 条", len(msgs))
	}
}
