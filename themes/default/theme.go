package defaulttheme

import (
	"embed"
	"io/fs"

	"goblog/core/plugin"
)

//go:embed templates/* static/*
var themeFS embed.FS

func init() {
	static, _ := fs.Sub(themeFS, "static")
	plugin.RegisterTheme(plugin.Theme{
		Name:         "default",
		Version:      "0.1.0",
		Author:       "GoBlog",
		Description:  "Typecho 默认主题启发的极简 MDUI 2 主题",
		Screenshot:   "/theme/default/cuckoo-sidebar.jpg",
		TemplateList: []string{"index.html", "post.html", "404.html"},
		Templates:    themeFS,
		Static:       static,
		Embedded:     true,
		ConfigSchema: []plugin.FieldSchema{
			{Name: "accent", Label: "强调色", Type: plugin.FieldText, Default: "#6750a4", Description: "用于主题自定义样式的颜色值"},
			{Name: "show_profile", Label: "显示侧栏资料卡", Type: plugin.FieldCheckbox, Default: "1"},
		},
		ContentFields: []plugin.FieldSchema{
			{Name: "cover", Label: "封面图", Type: plugin.FieldText, Description: "文章或页面封面图 URL", ForTypes: []string{"post", "page"}},
		},
	})
}
