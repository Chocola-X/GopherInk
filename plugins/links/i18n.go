package links

import "strings"

var zhCN = map[string]string{
	"Friend Links":                          "友链",
	"Manage friend links and choose the standalone page that uses the friend-link template.": "管理友链，并指定套用友链模板的独立页面。",
	"Friend Link Settings":                  "友链设置",
	"Friend Link List":                      "友链列表",
	"Add Friend Link":                       "添加友链",
	"Friend links are displayed in this order; email is used for avatars and commenter identity matching and is not exposed on the frontend. Email or icon URL is required.": "按照这里的顺序展示友链；邮箱用于生成头像和识别评论者身份，不会在前台公开。邮箱与图标 URL 至少填写一项。",
	"Page and Ordering":                     "页面与排序",
	"Target page CID or permalink":          "目标独立页面 CID 或固定链接",
	"Supports CID, custom slug, /page/slug.html, or full page URL; numeric values are parsed as CID first.": "支持 CID、自定义 slug、/page/slug.html 或完整页面 URL；纯数字优先按 CID 解析。",
	"Current page":                          "当前页面",
	"Shuffle friend links":                  "乱序排列友链",
	"Randomize display order on each render without changing the saved order below.": "每次渲染页面时随机调整展示顺序，不改变下方保存的顺序。",
	"Link name":                             "链接名称",
	"Email (required if icon URL is blank)": "邮箱（与图标 URL 至少填写一项）",
	"Friend icon URL (uses email avatar when blank)": "友链图标 URL（留空时使用邮箱头像）",
	"Link description":                      "链接描述",
	"Move up":                               "上移",
	"Move down":                             "下移",
	"Delete":                                "删除",
	"Save Settings":                         "保存设置",
	"No friend links have been added yet.":  "还没有添加友链。",
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
	"target page does not exist or the permalink is incorrect":           "目标独立页面不存在或固定链接不正确",
	"target content is not a page":                                       "目标内容不是独立页面",
	"target page must be published":                                      "目标独立页面必须处于已发布状态",
	"enter a target page CID or permalink":                               "请填写目标独立页面 CID 或固定链接",
	"target page permalink is invalid":                                   "目标独立页面固定链接格式不正确",
	"permalink must point to a page under /page/":                        "固定链接必须指向 /page/ 下的独立页面",
	"unknown plugin page: %s":                                            "未知的插件页面：%s",
	"read friend link settings: %w":                                      "读取友链配置：%w",
	"render friend link settings: %w":                                    "渲染友链设置：%w",
	"unsupported friend link action":                                     "不支持的友链操作",
	"save friend link settings: %w":                                      "保存友链配置：%w",
	"Owner":  "博主",
	"Friend": "好友",
}

func T(lang, key string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(lang)), "zh") {
		if value := zhCN[key]; value != "" {
			return value
		}
	}
	return key
}
