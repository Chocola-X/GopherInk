package links

import "strings"

var zhCN = map[string]string{
	"Friend Links": "友链",
	"Manage friend link data for themes to consume.": "管理供主题读取的友链数据。",
	"Friend Link Settings":                  "友链设置",
	"Friend Link List":                      "友链列表",
	"Add Friend Link":                       "添加友链",
	"Friend links are displayed in this order; email is used for avatars and commenter identity matching and is not exposed on the frontend. Email or icon URL is required.": "按照这里的顺序展示友链；邮箱用于生成头像和识别评论者身份，不会在前台公开。邮箱与图标 URL 至少填写一项。",
	"Link name":                             "链接名称",
	"Email (required if icon URL is blank)": "邮箱（与图标 URL 至少填写一项）",
	"Friend icon URL (uses email avatar when blank)": "友链图标 URL（留空时使用邮箱头像）",
	"Link description":                      "链接描述",
	"Move up":                               "上移",
	"Move down":                             "下移",
	"Delete":                                "删除",
	"Save Settings":                         "保存设置",
	"No friend links yet. Use the top-right button to add one.": "暂无友链，点击右上角按钮开始添加。",
	"Friend link settings saved.":           "友链设置已保存。",
	"friend link form data is incomplete; refresh the page and try again": "友链表单数据不完整，请刷新页面后重试",
	"friend links cannot exceed %d entries": "友链数量不能超过 %d 条",
	"friend links exceed %d entries":        "友链数量超过 %d 条",
	"friend link %d is missing a name":      "第 %d 条友链缺少链接名称",
	"friend link %d name cannot exceed 100 characters":                    "第 %d 条友链的名称不能超过 100 个字符",
	"friend link %d is missing a description":                            "第 %d 条友链缺少链接描述",
	"friend link %d description cannot exceed 250 characters":            "第 %d 条友链的描述不能超过 250 个字符",
	"friend link %d URL must be a valid HTTP or HTTPS URL":               "第 %d 条友链的 URL 必须是有效的 HTTP 或 HTTPS 地址",
	"friend link %d must include either an email or icon URL":            "第 %d 条友链必须填写邮箱或友链图标 URL",
	"friend link %d email is invalid":                                    "第 %d 条友链的邮箱格式不正确",
	"friend link %d icon URL is invalid":                                 "第 %d 条友链的图标 URL 格式不正确",
	"unknown plugin page: %s":                                            "未知的插件页面：%s",
	"read friend link settings: %w":                                      "读取友链配置：%w",
	"render friend link settings: %w":                                    "渲染友链设置：%w",
	"unsupported friend link action":                                     "不支持的友链操作",
	"save friend link settings: %w":                                      "保存友链配置：%w",
}

func T(lang, key string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(lang)), "zh") {
		if value := zhCN[key]; value != "" {
			return value
		}
	}
	return key
}
