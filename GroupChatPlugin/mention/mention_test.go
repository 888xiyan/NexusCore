package mention

import (
	"reflect"
	"testing"
)

func TestExtractMentions(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "无@",
			input:    "你好，今天天气真好",
			expected: nil,
		},
		{
			name:     "单个有效@",
			input:    "请 @ops_bot 检查构建环境",
			expected: []string{"ops_bot"},
		},
		{
			name:     "多个有效@与去重",
			input:    "@ops_bot 请与 @qa_bot 一起排查，@ops_bot 负责主调",
			expected: []string{"ops_bot", "qa_bot"},
		},
		{
			name:     "无效长度（少于5字符）",
			input:    "hello @bot @ab",
			expected: nil,
		},
		{
			name:     "非法字符",
			input:    "@OPS_BOT @ops-bot @ops.bot",
			expected: nil,
		},
		{
			name:     "标点符号截断",
			input:    "@ops_bot, 你好！请看 @qa_bot。",
			expected: []string{"ops_bot", "qa_bot"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractMentions(tt.input)
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("ExtractMentions(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}
