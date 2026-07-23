package defaulttheme

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Chocola-X/GopherInk/core/plugin"
)

//go:embed templates/* static/* admin/*
var themeFS embed.FS

var assetRandomSequence atomic.Uint64

func init() {
	assetRandomSequence.Store(uint64(time.Now().UnixNano()))
	static, _ := fs.Sub(themeFS, "static")
	colorOptions := []plugin.FieldOption{
		{Label: "Rose", Value: "#ff4081"},
		{Label: "Purple", Value: "#6750a4"},
		{Label: "Pink", Value: "#c2185b"},
		{Label: "Indigo", Value: "#3f51b5"},
		{Label: "Blue", Value: "#1976d2"},
		{Label: "Cyan", Value: "#00838f"},
		{Label: "Green", Value: "#2e7d32"},
		{Label: "Amber", Value: "#ff8f00"},
		{Label: "Deep Orange", Value: "#e64a19"},
		{Label: "Blue Grey", Value: "#546e7a"},
	}
	plugin.RegisterTheme(plugin.Theme{
		Name:         "default",
		DisplayName:  "Default Theme",
		Version:      "0.5.0",
		Author:       "GopherInk",
		Description:  "GopherInk default theme built with MDUI 2, featuring dynamic colors, PJAX navigation, and optional decorative images.",
		TemplateList: []string{"index.html", "post.html", "404.html"},
		Templates:    themeFS,
		Static:       static,
		Embedded:     true,
		Translate:    defaultThemeT,
		Capabilities: plugin.ThemeCapabilities{CommentGuard: true},
		AdminPages: []plugin.AdminPage{{
			Name:        friendAdminPageName,
			Label:       "Friend Links",
			Icon:        "link",
			Title:       "Friend Link Settings",
			Description: "Manage friend links and choose the standalone page that uses the friend-link template.",
		}},
		RenderAdminPage:       renderFriendAdminPage,
		HandleAdminPageAction: handleFriendAdminPageAction,
		EnrichComments:        friendEnrichComments,
		AdjustData:            adjustDefaultThemeData,
		Funcs: template.FuncMap{
			"themeValue":      themeValue,
			"themeInt":        themeInt,
			"themeOpacity":    themeOpacity,
			"assetURL":        assetURL,
			"safeHTML":        func(value string) template.HTML { return template.HTML(value) },
			"readingTimeI18n": readingTimeI18n,
			"daysSince":       daysSince,
			"staleDays":       staleDays,
			"fieldString": func(fields map[string]any, name string) string {
				if fields == nil {
					return ""
				}
				value, ok := fields[name]
				if !ok || value == nil {
					return ""
				}
				return strings.TrimSpace(fmt.Sprint(value))
			},
		},
		ConfigSchema: []plugin.FieldSchema{
			{Name: "display_name", Label: "Profile name", Group: "Profile Card", Type: plugin.FieldText, Default: "GopherInk", Description: "Use site title when blank"},
			{Name: "profile_email", Label: "Avatar email", Group: "Profile Card", Type: plugin.FieldText, Description: "Generate avatar with the unified admin avatar URL; email is not exposed on the frontend"},
			{Name: "profile_avatar", Label: "Avatar image URL", Group: "Profile Card", Type: plugin.FieldImage, Description: "Specify avatar image; when blank, use CMS avatar URL settings; supports {random}"},
			{Name: "bio", Label: "Profile description", Group: "Profile Card", Type: plugin.FieldText, Description: "Use site description when blank", Wide: true},
			{Name: "primary_preset", Label: "Preset color", Group: "Colors and Opacity", Type: plugin.FieldSelect, Default: "#ff4081", Options: colorOptions},
			{Name: "custom_primary", Label: "Custom color", Group: "Colors and Opacity", Type: plugin.FieldColor, Description: "Overrides preset color when #RRGGBB is set", Options: colorOptions},
			{Name: "theme_mode", Label: "Color mode", Group: "Colors and Opacity", Type: plugin.FieldSelect, Default: "auto", Options: []plugin.FieldOption{{Label: "Follow system", Value: "auto"}, {Label: "Light", Value: "light"}, {Label: "Dark", Value: "dark"}}},
			{Name: "card_opacity", Label: "Card background opacity", Group: "Colors and Opacity", Type: plugin.FieldNumber, Default: "0.80", Description: "0 to 1; only changes card background opacity, not MDUI theme colors", Min: "0", Max: "1", Step: "0.01"},
			{Name: "input_opacity", Label: "Input background opacity", Group: "Colors and Opacity", Type: plugin.FieldNumber, Default: "0.42", Description: "0 to 1; keeps MDUI input color and only changes opacity", Min: "0", Max: "1", Step: "0.01"},
			{Name: "background_mask_opacity", Label: "Background mask opacity", Group: "Colors and Opacity", Type: plugin.FieldNumber, Default: "0.46", Description: "0 to 1; controls the mask over page background", Min: "0", Max: "1", Step: "0.01"},
			{Name: "background_image", Label: "Desktop background URL", Group: "Background and Decorative Images", Type: plugin.FieldImage, Description: "Enter URL or upload image; blank uses MDUI theme-color background; supports {random}"},
			{Name: "mobile_background_image", Label: "Mobile background URL", Group: "Background and Decorative Images", Type: plugin.FieldImage, Description: "Blank uses desktop background; supports {random}"},
			{Name: "sidebar_image", Label: "Sidebar cover URL", Group: "Background and Decorative Images", Type: plugin.FieldImage, Description: "Cover image for profile card; blank uses theme color; supports {random}"},
			{Name: "fallback_no_cover", Label: "Fallback to no-cover style when post has no cover", Group: "Background and Decorative Images", Type: plugin.FieldCheckbox, Default: "1", Description: "Only affects article-style posts without their own cover; explicit no-cover or daily styles are preserved", Wide: true},
			{Name: "default_cover", Label: "Default post cover URL", Group: "Background and Decorative Images", Type: plugin.FieldImage, Description: "Used for article-style posts without cover when fallback is disabled; supports {random}", Required: true, ShowWhenField: "fallback_no_cover", ShowWhenValue: "0"},
			{Name: "comment_bg_image", Label: "Comment form decorative image URL", Group: "Background and Decorative Images", Type: plugin.FieldImage, Description: "Shown on the right side of comment form; blank hides it; supports {random}"},
			{Name: "post_end_image", Label: "Post footer decorative image URL", Group: "Background and Decorative Images", Type: plugin.FieldImage, Description: "Shown at bottom-right of post content; blank hides it; supports {random}"},
			{Name: "favicon", Label: "Favicon URL", Group: "Background and Decorative Images", Type: plugin.FieldImage, Default: "/theme/default/favicon.svg", Description: "Blank uses GopherInk default logo; supports {random}"},
			{Name: "show_profile", Label: "Show sidebar profile card", Group: "Sidebar and Navigation", Type: plugin.FieldCheckbox, Default: "1"},
			{Name: "show_recent_comments", Label: "Show recent comments", Group: "Sidebar and Navigation", Type: plugin.FieldCheckbox, Default: "1"},
			{Name: "show_tag_cloud", Label: "Show tag cloud", Group: "Sidebar and Navigation", Type: plugin.FieldCheckbox, Default: "1"},
			{Name: "enable_pjax", Label: "Enable PJAX navigation", Group: "Sidebar and Navigation", Type: plugin.FieldCheckbox, Default: "1"},
			{Name: "enable_infinite_scroll", Label: "Enable infinite scroll for post list", Group: "Post Display", Type: plugin.FieldCheckbox, Default: "0", Description: "Load next page near list bottom; disabled keeps normal pagination and manual load", Wide: true},
			{Name: "enable_toc", Label: "Show table of contents on post pages", Group: "Sidebar and Navigation", Type: plugin.FieldCheckbox, Default: "1"},
			{Name: "enable_back_to_top", Label: "Show back-to-top button", Group: "Sidebar and Navigation", Type: plugin.FieldCheckbox, Default: "1"},
			{Name: "show_stale_notice", Label: "Show stale post notice", Group: "Post Display", Type: plugin.FieldCheckbox, Default: "1", Description: "Show notice when post modified time exceeds configured days", Wide: true},
			{Name: "stale_notice_days", Label: "Stale notice days", Group: "Post Display", Type: plugin.FieldNumber, Default: "30", Description: "Calculated from post modified time", Min: "1", Max: "3650", Step: "1", Required: true, ShowWhenField: "show_stale_notice", ShowWhenValue: "1", Wide: true},
			{Name: "footer_html", Label: "Footer HTML", Group: "Footer", Type: plugin.FieldTextarea, Description: "Blank shows Powered by GopherInk", Wide: true},
		},
		ContentFields: []plugin.FieldSchema{
			{
				Name:    "articleType",
				Label:   "Article type",
				Group:   "Theme Display",
				Type:    plugin.FieldSelect,
				Default: "article",
				Options: []plugin.FieldOption{
					{Label: "Posts", Value: "article"},
					{Label: "No cover", Value: "normal"},
					{Label: "Daily", Value: "daily"},
				},
				Description: "Choose how this content is displayed in the default theme",
				ForTypes:    []string{"post", "page"},
			},
			{
				Name:        "catalog",
				Label:       "Table of Contents",
				Group:       "Theme Display",
				Type:        plugin.FieldSelect,
				Default:     "1",
				Options:     []plugin.FieldOption{{Label: "Show", Value: "1"}, {Label: "Hidden", Value: "0"}},
				Description: "Used together with the theme-wide TOC switch",
				ForTypes:    []string{"post", "page"},
			},
			{Name: "cover", Label: "Post/Page cover image", Group: "Theme Display", Type: plugin.FieldImage, Description: "Enter image URL; blank follows theme fallback; supports {random}", ForTypes: []string{"post", "page"}, Wide: true},
			{Name: "remark", Label: "No-cover card note", Group: "Theme Display", Type: plugin.FieldText, Description: "Only shown in no-cover post cards", ForTypes: []string{"post", "page"}, Wide: true},
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

func themeInt(values map[string]string, key string, fallback int) int {
	value, err := strconv.Atoi(themeValue(values, key, strconv.Itoa(fallback)))
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

func assetURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "{random}") {
		value = strings.ReplaceAll(value, "{random}", randomAssetToken())
	}
	if strings.HasPrefix(value, "//") || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "#") {
		return value
	}
	if u, err := url.Parse(value); err == nil && u.Scheme != "" {
		return value
	}
	return "/" + value
}

func randomAssetToken() string {
	value := assetRandomSequence.Add(0x9e3779b97f4a7c15)
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	value ^= value >> 31
	return strconv.FormatUint(value, 10)
}

func readingTimeI18n(lang, text string) string {
	runes := utf8.RuneCountInString(stripHTMLLike(text))
	minutes := (runes + 399) / 400
	if minutes < 1 {
		minutes = 1
	}
	if minutes == 1 {
		return fmt.Sprintf("1 %s", defaultThemeT(lang, "min"))
	}
	return fmt.Sprintf("%d %s", minutes, defaultThemeT(lang, "mins"))
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
