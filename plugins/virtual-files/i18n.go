package virtualfiles

import "strings"

var zhCN = map[string]string{
	"Serve administrator-managed text files from fallback public paths for SEO and domain verification.": "通过后备公开路径提供管理员维护的文本文件，用于 SEO 和域名验证。",
	"Virtual Files": "虚拟文件",
	"Files":         "文件管理",
	"Virtual files": "虚拟文件",
	"Manage small text responses used by robots, SEO tools, and domain ownership verification.":                                     "管理用于 Robots、SEO 工具和域名所有权验证的小型文本响应。",
	"Configured files are only served after all CMS, theme, and plugin routes fail to match. Existing routes always take priority.": "只有当 CMS、主题和插件路由均未匹配时才会提供虚拟文件，现有路由始终优先。",
	"Add virtual file":                 "添加虚拟文件",
	"New virtual file":                 "新虚拟文件",
	"Virtual path":                     "虚拟路径",
	"Text content":                     "文本内容",
	"Enter the text file content":      "输入文本文件内容",
	"Save":                             "保存",
	"Delete":                           "删除",
	"Cancel":                           "取消",
	"Add":                              "添加",
	"No virtual files are configured.": "尚未配置虚拟文件。",
	"Delete virtual file?":             "删除虚拟文件？",
	"The configured response will stop being available immediately.": "删除后，该路径配置的响应将立即停止提供。",
	"Unknown plugin page.":                               "未知的插件页面。",
	"Unsupported action.":                                "不支持的操作。",
	"Virtual file saved.":                                "虚拟文件已保存。",
	"Virtual file deleted.":                              "虚拟文件已删除。",
	"Stored virtual file data is invalid.":               "保存的虚拟文件数据格式无效。",
	"At most 100 virtual files are allowed.":             "最多允许配置 100 个虚拟文件。",
	"A virtual file with this path already exists.":      "该路径已存在虚拟文件。",
	"A virtual file cannot exceed 512 KiB.":              "单个虚拟文件不能超过 512 KiB。",
	"Virtual file content cannot exceed 4 MiB in total.": "虚拟文件内容总量不能超过 4 MiB。",
	"Enter a valid absolute path beginning with /.":      "请输入以 / 开头的有效绝对路径。",
	"The path must be canonical and cannot contain a query, fragment, percent escape, or trailing slash.": "路径必须是规范路径，不能包含查询参数、片段、百分号转义或末尾斜杠。",
	"The path contains unsupported characters.":                                                           "路径包含不支持的字符。",
	"The path must include a filename and extension.":                                                     "路径必须包含文件名和扩展名。",
	"The virtual file no longer exists.":                                                                  "该虚拟文件已不存在。",
}

func T(lang, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(lang)), "zh") {
		if translated := strings.TrimSpace(zhCN[key]); translated != "" {
			return translated
		}
	}
	return key
}
