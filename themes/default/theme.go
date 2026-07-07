package defaulttheme

import (
	"crypto/md5"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"goblog/core/plugin"
)

//go:embed templates/* static/*
var themeFS embed.FS

func init() {
	static, _ := fs.Sub(themeFS, "static")
	colorOptions := []plugin.FieldOption{
		{Label: "Cuckoo 粉", Value: "#ff4081"},
		{Label: "紫色", Value: "#6750a4"},
		{Label: "粉色", Value: "#c2185b"},
		{Label: "靛蓝", Value: "#3f51b5"},
		{Label: "蓝色", Value: "#1976d2"},
		{Label: "青色", Value: "#00838f"},
		{Label: "绿色", Value: "#2e7d32"},
		{Label: "琥珀", Value: "#ff8f00"},
		{Label: "深橙", Value: "#e64a19"},
		{Label: "灰蓝", Value: "#546e7a"},
	}
	plugin.RegisterTheme(plugin.Theme{
		Name:         "default",
		Version:      "0.2.0",
		Author:       "GoBlog",
		Description:  "Cuckoo 风格的 MDUI 2 默认主题，默认无装饰图，支持 PJAX、主题色和可选图片",
		TemplateList: []string{"index.html", "post.html", "404.html"},
		Templates:    themeFS,
		Static:       static,
		Embedded:     true,
		Funcs: template.FuncMap{
			"themeValue":  themeValue,
			"gravatar":    gravatarURL,
			"avatarURL":   avatarURL,
			"safeHTML":    func(value string) template.HTML { return template.HTML(value) },
			"readingTime": readingTime,
			"daysSince":   daysSince,
			"staleDays":   staleDays,
			"fieldString": func(fields map[string]any, name string) string {
				if fields == nil {
					return ""
				}
				return strings.TrimSpace(fmt.Sprint(fields[name]))
			},
		},
		ConfigSchema: []plugin.FieldSchema{
			{Name: "display_name", Label: "资料卡名称", Type: plugin.FieldText, Default: "GoBlog", Description: "留空时使用站点标题"},
			{Name: "profile_email", Label: "头像邮箱", Type: plugin.FieldText, Description: "用于生成 Gravatar/Cravatar 头像，不在前台明文展示"},
			{Name: "profile_avatar", Label: "头像图片 URL", Type: plugin.FieldImage, Description: "单独指定头像图片；留空时按头像邮箱生成 Gravatar"},
			{Name: "bio", Label: "资料卡描述", Type: plugin.FieldText, Description: "留空时使用站点描述"},
			{Name: "primary_preset", Label: "常用主题色", Type: plugin.FieldSelect, Default: "#ff4081", Options: colorOptions},
			{Name: "custom_primary", Label: "自定义主题色", Type: plugin.FieldColor, Description: "填写 #RRGGBB 后优先于常用主题色", Options: colorOptions},
			{Name: "theme_mode", Label: "明暗模式", Type: plugin.FieldSelect, Default: "auto", Options: []plugin.FieldOption{{Label: "跟随系统", Value: "auto"}, {Label: "浅色", Value: "light"}, {Label: "深色", Value: "dark"}}},
			{Name: "enable_decor", Label: "启用装饰图片", Type: plugin.FieldCheckbox, Default: "1", Description: "关闭后只使用 MDUI 主题色生成简洁外观"},
			{Name: "background_image", Label: "桌面背景图 URL", Type: plugin.FieldImage, Description: "可输入 URL 或上传图片；留空时使用 MDUI 主题色背景"},
			{Name: "mobile_background_image", Label: "移动端背景图 URL", Type: plugin.FieldImage, Description: "留空时沿用桌面背景图"},
			{Name: "sidebar_image", Label: "侧栏封面图 URL", Type: plugin.FieldImage, Description: "资料卡顶部封面；留空时使用主题色"},
			{Name: "default_cover", Label: "默认文章封面 URL", Type: plugin.FieldImage, Description: "文章未设置 cover 字段时使用；留空时使用主题色看板"},
			{Name: "favicon", Label: "Favicon URL", Type: plugin.FieldImage},
			{Name: "show_profile", Label: "显示侧栏资料卡", Type: plugin.FieldCheckbox, Default: "1"},
			{Name: "show_recent_comments", Label: "显示最新回复", Type: plugin.FieldCheckbox, Default: "1"},
			{Name: "show_tag_cloud", Label: "显示标签云", Type: plugin.FieldCheckbox, Default: "1"},
			{Name: "enable_pjax", Label: "启用 PJAX 导航", Type: plugin.FieldCheckbox, Default: "1"},
			{Name: "enable_toc", Label: "文章页显示目录", Type: plugin.FieldCheckbox, Default: "1"},
			{Name: "enable_back_to_top", Label: "显示回到顶部按钮", Type: plugin.FieldCheckbox, Default: "1"},
			{Name: "footer_html", Label: "底部 HTML", Type: plugin.FieldTextarea, Description: "留空时显示 Powered by GoBlog"},
		},
		ContentFields: []plugin.FieldSchema{
			{Name: "cover", Label: "封面图", Type: plugin.FieldImage, Description: "文章或页面封面图 URL", ForTypes: []string{"post", "page"}},
			{Name: "catalog", Label: "显示文章目录", Type: plugin.FieldCheckbox, Default: "1", Description: "配合主题目录开关使用", ForTypes: []string{"post", "page"}},
		},
	})
}

func themeValue(values map[string]string, key string, fallback ...string) string {
	if values != nil {
		if value := strings.TrimSpace(values[key]); value != "" {
			return value
		}
	}
	if len(fallback) > 0 {
		return fallback[0]
	}
	return ""
}

func gravatarURL(email string, size int) string {
	if size <= 0 {
		size = 160
	}
	sum := md5.Sum([]byte(strings.ToLower(strings.TrimSpace(email))))
	query := url.Values{}
	query.Set("s", fmt.Sprint(size))
	query.Set("d", "identicon")
	return "https://www.gravatar.com/avatar/" + fmt.Sprintf("%x", sum[:]) + "?" + query.Encode()
}

func avatarURL(values map[string]string, fallbackEmail string, size int) string {
	if url := themeValue(values, "profile_avatar"); url != "" {
		return url
	}
	return gravatarURL(themeValue(values, "profile_email", fallbackEmail), size)
}

func readingTime(text string) string {
	runes := utf8.RuneCountInString(stripHTMLLike(text))
	if runes == 0 {
		return "1 分钟"
	}
	minutes := (runes + 399) / 400
	if minutes < 1 {
		minutes = 1
	}
	return fmt.Sprintf("%d 分钟", minutes)
}

func daysSince(ts int64) int {
	if ts <= 0 {
		return 0
	}
	days := int(time.Since(time.Unix(ts, 0)).Hours() / 24)
	if days < 0 {
		return 0
	}
	return days
}

func staleDays(ts int64, threshold int) bool {
	return daysSince(ts) > threshold
}

func stripHTMLLike(text string) string {
	var b strings.Builder
	inTag := false
	for _, r := range text {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}
