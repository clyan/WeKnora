package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeWikiDirectoryContractV1(t *testing.T) {
	entries := CodeWikiDirectoryCategories()
	titles := make([]string, 0, len(entries))
	seen := make(map[string]bool)
	for _, entry := range entries {
		require.True(t, IsValidCodeWikiPageCategory(entry.Category))
		require.False(t, seen[entry.Category])
		seen[entry.Category] = true
		titles = append(titles, entry.Title)
	}
	require.Equal(t, []string{"项目概览", "系统架构", "仓库文档", "接口与跨端契约", "功能链路", "开发与联调", "分析范围与版本"}, titles)
	entries[0].Title = "mutated by caller"
	require.Equal(t, "项目概览", CodeWikiDirectoryCategories()[0].Title)
}
