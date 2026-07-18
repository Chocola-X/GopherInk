package defaulttheme

import (
	"crypto/md5"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Chocola-X/GopherInk/core/plugin"
)

//go:embed templates/* static/*
var themeFS embed.FS

func init() {
	static, _ := fs.Sub(themeFS, "static")
	colorOptions := []plugin.FieldOption{
		{Label: "玫红", Value: "#ff4081"},
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
		DisplayName:  "Default Theme",
		Version:      "0.5.0",
		Author:       "GopherInk",
		Description:  "GopherInk 默认主题，基于 MDUI 2 设计，支持动态主题配色、PJAX 导航与按需装饰图片。",
		TemplateList: []string{"index.html", "post.html", "404.html"},
		Templates:    themeFS,
		Static:       static,
		Embedded:     true,
		Funcs: template.FuncMap{
			"themeValue":   themeValue,
			"themeOpacity": themeOpacity,
			"gravatar":     gravatarURL,
			"avatarURL":    avatarURL,
			"assetURL":     assetURL,
			"safeHTML":     func(value string) template.HTML { return template.HTML(value) },
			"readingTime":  readingTime,
			"daysSince":    daysSince,
			"staleDays":    staleDays,
			"fieldString": func(fields map[string]any, name string) string {
				if fields == nil {
					return ""
				}
				return strings.TrimSpace(fmt.Sprint(fields[name]))
			},
		},
		ConfigSchema: []plugin.FieldSchema{
			{Name: "display_name", Label: "资料卡名称", Group: "资料卡", Type: plugin.FieldText, Default: "GopherInk", Description: "留空时使用站点标题"},
			{Name: "profile_email", Label: "头像邮箱", Group: "资料卡", Type: plugin.FieldText, Description: "用于生成 Gravatar/Cravatar 头像，不在前台明文展示"},
			{Name: "profile_avatar", Label: "头像图片 URL", Group: "资料卡", Type: plugin.FieldImage, Description: "单独指定头像图片；留空时按头像邮箱生成 Gravatar"},
			{Name: "bio", Label: "资料卡描述", Group: "资料卡", Type: plugin.FieldText, Description: "留空时使用站点描述", Wide: true},
			{Name: "primary_preset", Label: "常用主题色", Group: "配色和透明度", Type: plugin.FieldSelect, Default: "#ff4081", Options: colorOptions},
			{Name: "custom_primary", Label: "自定义主题色", Group: "配色和透明度", Type: plugin.FieldColor, Description: "填写 #RRGGBB 后优先于常用主题色", Options: colorOptions},
			{Name: "theme_mode", Label: "明暗模式", Group: "配色和透明度", Type: plugin.FieldSelect, Default: "auto", Options: []plugin.FieldOption{{Label: "跟随系统", Value: "auto"}, {Label: "浅色", Value: "light"}, {Label: "深色", Value: "dark"}}},
			{Name: "card_opacity", Label: "卡片背景透明度", Group: "配色和透明度", Type: plugin.FieldNumber, Default: "0.80", Description: "0 到 1；仅调整卡片背景透明度，不改变 MDUI 主题配色", Min: "0", Max: "1", Step: "0.01"},
			{Name: "input_opacity", Label: "输入框背景透明度", Group: "配色和透明度", Type: plugin.FieldNumber, Default: "0.42", Description: "0 到 1；保留 MDUI 输入框背景色，仅调整透明度", Min: "0", Max: "1", Step: "0.01"},
			{Name: "background_mask_opacity", Label: "背景遮罩透明度", Group: "配色和透明度", Type: plugin.FieldNumber, Default: "0.46", Description: "0 到 1；控制页面背景上方遮罩层透明度", Min: "0", Max: "1", Step: "0.01"},
			{Name: "enable_decor", Label: "启用装饰图片", Group: "背景和装饰图片", Type: plugin.FieldCheckbox, Default: "1", Description: "关闭后隐藏默认封面类装饰；手动配置的背景、评论和文章底部图片仍会显示", Wide: true},
			{Name: "background_image", Label: "桌面背景图 URL", Group: "背景和装饰图片", Type: plugin.FieldImage, Description: "可输入 URL 或上传图片；留空时使用 MDUI 主题色背景"},
			{Name: "mobile_background_image", Label: "移动端背景图 URL", Group: "背景和装饰图片", Type: plugin.FieldImage, Description: "留空时沿用桌面背景图"},
			{Name: "sidebar_image", Label: "侧栏封面图 URL", Group: "背景和装饰图片", Type: plugin.FieldImage, Description: "资料卡顶部封面；留空时使用主题色"},
			{Name: "default_cover", Label: "默认文章封面 URL", Group: "背景和装饰图片", Type: plugin.FieldImage, Description: "文章未设置 cover 字段时使用；留空时使用主题色看板"},
			{Name: "comment_bg_image", Label: "评论框装饰图 URL", Group: "背景和装饰图片", Type: plugin.FieldImage, Description: "显示在评论输入框右侧；留空时不显示"},
			{Name: "post_end_image", Label: "文章底部装饰图 URL", Group: "背景和装饰图片", Type: plugin.FieldImage, Description: "显示在文章内容底部右下角；留空时不显示"},
			{Name: "favicon", Label: "Favicon URL", Group: "背景和装饰图片", Type: plugin.FieldImage, Default: "/theme/default/favicon.svg", Description: "留空时使用 GopherInk 默认 Logo"},
			{Name: "show_profile", Label: "显示侧栏资料卡", Group: "侧栏和导航", Type: plugin.FieldCheckbox, Default: "1"},
			{Name: "show_recent_comments", Label: "显示最新回复", Group: "侧栏和导航", Type: plugin.FieldCheckbox, Default: "1"},
			{Name: "show_tag_cloud", Label: "显示标签云", Group: "侧栏和导航", Type: plugin.FieldCheckbox, Default: "1"},
			{Name: "enable_pjax", Label: "启用 PJAX 导航", Group: "侧栏和导航", Type: plugin.FieldCheckbox, Default: "1"},
			{Name: "enable_toc", Label: "文章页显示目录", Group: "侧栏和导航", Type: plugin.FieldCheckbox, Default: "1"},
			{Name: "enable_back_to_top", Label: "显示回到顶部按钮", Group: "侧栏和导航", Type: plugin.FieldCheckbox, Default: "1"},
			{Name: "footer_html", Label: "底部 HTML", Group: "页脚", Type: plugin.FieldTextarea, Description: "留空时显示 Powered by GopherInk", Wide: true},
		},
		ContentFields: []plugin.FieldSchema{
			{
				Name:    "articleType",
				Label:   "文章类型",
				Group:   "主题显示",
				Type:    plugin.FieldSelect,
				Default: "article",
				Options: []plugin.FieldOption{
					{Label: "文章", Value: "article"},
					{Label: "无封面", Value: "normal"},
					{Label: "日常", Value: "daily"},
				},
				Description: "选择当前内容在默认主题中的展示类型",
				ForTypes:    []string{"post", "page"},
			},
			{
				Name:        "catalog",
				Label:       "文章目录",
				Group:       "主题显示",
				Type:        plugin.FieldSelect,
				Default:     "1",
				Options:     []plugin.FieldOption{{Label: "显示", Value: "1"}, {Label: "隐藏", Value: "0"}},
				Description: "配合主题设置中的目录总开关使用",
				ForTypes:    []string{"post", "page"},
			},
			{Name: "cover", Label: "文章/独立页面封面图", Group: "主题显示", Type: plugin.FieldImage, Description: "填写图片 URL；留空时使用主题设置中的默认封面", ForTypes: []string{"post", "page"}, Wide: true},
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

func themeOpacity(values map[string]string, key, fallback string) string {
	value, err := strconv.ParseFloat(themeValue(values, key, fallback), 64)
	if err != nil {
		value, _ = strconv.ParseFloat(fallback, 64)
	}
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
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
		return assetURL(url)
	}
	return gravatarURL(themeValue(values, "profile_email", fallbackEmail), size)
}

func assetURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "//") || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "#") {
		return value
	}
	if u, err := url.Parse(value); err == nil && u.Scheme != "" {
		return value
	}
	return "/" + value
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
