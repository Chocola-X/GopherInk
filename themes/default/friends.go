package defaulttheme

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"math/rand"
	"net/mail"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/Chocola-X/GopherInk/core/models"
	"github.com/Chocola-X/GopherInk/core/plugin"
)

const (
	friendAdminPageName = "friends"
	friendPageTargetKey = "friend_page_target"
	friendShuffleKey    = "friend_links_shuffle"
	friendLinksKey      = "friend_links"
	maxFriendLinks      = 200
)

type friendLink struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Email       string `json:"email"`
	IconURL     string `json:"icon_url,omitempty"`
	AvatarURL   string `json:"-"`
}

type friendLinkView struct {
	Name        string
	Description string
	URL         string
	AvatarURL   string
}

type friendAdminPageData struct {
	CSRF         string
	PageTarget   string
	Shuffle      bool
	Links        []friendLink
	TargetTitle  string
	TargetURL    string
	TargetNotice string
}

type friendPageTarget struct {
	CID  int64
	Slug string
}

var friendAdminTemplate = template.Must(template.New("friends.html").ParseFS(themeFS, "admin/friends.html"))

func renderFriendAdminPage(ctx context.Context, rt *plugin.Runtime, page string, renderContext plugin.AdminPageRenderContext) (template.HTML, error) {
	if page != friendAdminPageName {
		return "", fmt.Errorf("未知的主题页面：%s", page)
	}
	links, err := decodeFriendLinks(renderContext.Config[friendLinksKey])
	if err != nil {
		return "", fmt.Errorf("读取友链配置：%w", err)
	}
	data := friendAdminPageData{
		CSRF:       renderContext.CSRF,
		PageTarget: strings.TrimSpace(renderContext.Config[friendPageTargetKey]),
		Shuffle:    renderContext.Config[friendShuffleKey] == "1",
		Links:      friendAdminLinks(ctx, rt, links),
	}
	if data.PageTarget != "" {
		content, contentErr := resolveFriendPageTarget(ctx, rt, data.PageTarget)
		if contentErr != nil {
			data.TargetNotice = contentErr.Error()
		} else {
			data.TargetTitle = content.Title
			if rt.ContentURL != nil {
				data.TargetURL, _ = rt.ContentURL(ctx, content.CID)
			}
		}
	}
	var output bytes.Buffer
	if err := friendAdminTemplate.ExecuteTemplate(&output, "friends.html", data); err != nil {
		return "", fmt.Errorf("渲染友链设置：%w", err)
	}
	return template.HTML(output.String()), nil
}

func friendAdminLinks(ctx context.Context, rt *plugin.Runtime, links []friendLink) []friendLink {
	out := make([]friendLink, len(links))
	copy(out, links)
	for i := range out {
		switch {
		case out[i].IconURL != "":
			out[i].AvatarURL = assetURL(out[i].IconURL)
		case out[i].Email != "" && rt != nil && rt.AvatarURL != nil:
			out[i].AvatarURL = rt.AvatarURL(ctx, out[i].Email, 80)
		}
	}
	return out
}

func handleFriendAdminPageAction(ctx context.Context, rt *plugin.Runtime, page string, form map[string][]string) (plugin.AdminPageActionResult, error) {
	if page != friendAdminPageName {
		return plugin.AdminPageActionResult{}, fmt.Errorf("未知的主题页面：%s", page)
	}
	if firstFriendFormValue(form, "action") != "save-friends" {
		return plugin.AdminPageActionResult{}, fmt.Errorf("不支持的友链操作")
	}
	pageTarget := strings.TrimSpace(firstFriendFormValue(form, friendPageTargetKey))
	if _, err := resolveFriendPageTarget(ctx, rt, pageTarget); err != nil {
		return plugin.AdminPageActionResult{}, err
	}
	links, err := friendLinksFromForm(form)
	if err != nil {
		return plugin.AdminPageActionResult{}, err
	}
	raw, err := json.Marshal(links)
	if err != nil {
		return plugin.AdminPageActionResult{}, fmt.Errorf("保存友链配置：%w", err)
	}
	shuffle := "0"
	if firstFriendFormValue(form, friendShuffleKey) == "1" {
		shuffle = "1"
	}
	return plugin.AdminPageActionResult{
		ConfigPatch: map[string]string{
			friendPageTargetKey: pageTarget,
			friendShuffleKey:    shuffle,
			friendLinksKey:      string(raw),
		},
		Notice: plugin.AdminNotice{Type: plugin.NoticeSuccess, Mode: plugin.NoticeSnackbar, Message: "友链设置已保存。"},
	}, nil
}

func friendLinksFromForm(form map[string][]string) ([]friendLink, error) {
	names := form["friend_name"]
	descriptions := form["friend_description"]
	urls := form["friend_url"]
	emails := form["friend_email"]
	iconURLs := form["friend_icon_url"]
	if len(names) != len(descriptions) || len(names) != len(urls) || len(names) != len(emails) || len(names) != len(iconURLs) {
		return nil, fmt.Errorf("友链表单数据不完整，请刷新页面后重试")
	}
	if len(names) > maxFriendLinks {
		return nil, fmt.Errorf("友链数量不能超过 %d 条", maxFriendLinks)
	}
	links := make([]friendLink, 0, len(names))
	for i := range names {
		link := friendLink{
			Name:        strings.TrimSpace(names[i]),
			Description: strings.TrimSpace(descriptions[i]),
			URL:         strings.TrimSpace(urls[i]),
			Email:       strings.ToLower(strings.TrimSpace(emails[i])),
			IconURL:     strings.TrimSpace(iconURLs[i]),
		}
		position := i + 1
		switch {
		case link.Name == "":
			return nil, fmt.Errorf("第 %d 条友链缺少链接名称", position)
		case len([]rune(link.Name)) > 100:
			return nil, fmt.Errorf("第 %d 条友链的名称不能超过 100 个字符", position)
		case link.Description == "":
			return nil, fmt.Errorf("第 %d 条友链缺少链接描述", position)
		case len([]rune(link.Description)) > 250:
			return nil, fmt.Errorf("第 %d 条友链的描述不能超过 250 个字符", position)
		case !validFriendURL(link.URL):
			return nil, fmt.Errorf("第 %d 条友链的 URL 必须是有效的 HTTP 或 HTTPS 地址", position)
		case link.Email == "" && link.IconURL == "":
			return nil, fmt.Errorf("第 %d 条友链必须填写邮箱或友链图标 URL", position)
		case link.Email != "" && !validFriendEmail(link.Email):
			return nil, fmt.Errorf("第 %d 条友链的邮箱格式不正确", position)
		case link.IconURL != "" && !validFriendIconURL(link.IconURL):
			return nil, fmt.Errorf("第 %d 条友链的图标 URL 格式不正确", position)
		}
		links = append(links, link)
	}
	return links, nil
}

func validFriendURL(value string) bool {
	if len(value) > 2048 {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func validFriendEmail(value string) bool {
	if value == "" || len(value) > 254 {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil && strings.EqualFold(address.Address, value)
}

func validFriendIconURL(value string) bool {
	if len(value) > 2048 {
		return false
	}
	value = strings.ReplaceAll(strings.TrimSpace(value), "{random}", "1")
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		parsed, err := url.ParseRequestURI(value)
		return err == nil && parsed.Path != ""
	}
	return validFriendURL(value)
}

func decodeFriendLinks(raw string) ([]friendLink, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var links []friendLink
	if err := json.Unmarshal([]byte(raw), &links); err != nil {
		return nil, err
	}
	if len(links) > maxFriendLinks {
		return nil, fmt.Errorf("友链数量超过 %d 条", maxFriendLinks)
	}
	return links, nil
}

func resolveFriendPageTarget(ctx context.Context, rt *plugin.Runtime, value string) (plugin.PublicContent, error) {
	target, err := parseFriendPageTarget(value)
	if err != nil {
		return plugin.PublicContent{}, err
	}
	if rt == nil {
		return plugin.PublicContent{}, plugin.ErrRuntimeUnavailable
	}
	var content plugin.PublicContent
	if target.CID > 0 {
		if rt.ContentByID == nil {
			return plugin.PublicContent{}, plugin.ErrRuntimeUnavailable
		}
		content, err = rt.ContentByID(ctx, target.CID)
	} else {
		if rt.PageBySlug == nil {
			return plugin.PublicContent{}, plugin.ErrRuntimeUnavailable
		}
		content, err = rt.PageBySlug(ctx, target.Slug)
	}
	if err != nil {
		return plugin.PublicContent{}, fmt.Errorf("目标独立页面不存在或固定链接不正确")
	}
	if content.Type != models.ContentTypePage {
		return plugin.PublicContent{}, fmt.Errorf("目标内容不是独立页面")
	}
	if content.Status != models.ContentStatusPost {
		return plugin.PublicContent{}, fmt.Errorf("目标独立页面必须处于已发布状态")
	}
	return content, nil
}

func parseFriendPageTarget(value string) (friendPageTarget, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return friendPageTarget{}, fmt.Errorf("请填写目标独立页面 CID 或固定链接")
	}
	if id, err := strconv.ParseInt(value, 10, 64); err == nil && id > 0 {
		return friendPageTarget{CID: id}, nil
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return friendPageTarget{}, fmt.Errorf("目标独立页面固定链接格式不正确")
	}
	candidate := parsed.Path
	if candidate == "" {
		candidate = value
	}
	candidate = strings.TrimSpace(candidate)
	if strings.HasPrefix(candidate, "/") || strings.Contains(candidate, "/") {
		cleaned := strings.Trim(path.Clean("/"+candidate), "/")
		if !strings.HasPrefix(cleaned, "page/") {
			return friendPageTarget{}, fmt.Errorf("固定链接必须指向 /page/ 下的独立页面")
		}
		candidate = strings.TrimPrefix(cleaned, "page/")
	}
	candidate = strings.TrimSuffix(candidate, ".html")
	candidate, err = url.PathUnescape(candidate)
	if err != nil || candidate == "" || strings.Contains(candidate, "/") {
		return friendPageTarget{}, fmt.Errorf("目标独立页面固定链接格式不正确")
	}
	return friendPageTarget{Slug: candidate}, nil
}

func friendPageTargetMatches(content models.Content, value string) bool {
	target, err := parseFriendPageTarget(value)
	if err != nil {
		return false
	}
	if target.CID > 0 {
		return content.CID == target.CID
	}
	return content.Slug == target.Slug || (content.Slug == "" && strconv.FormatInt(content.SlugID, 10) == target.Slug)
}

func adjustDefaultThemeData(ctx context.Context, data map[string]any) error {
	config, _ := data["ThemeConfig"].(map[string]string)
	links, _ := decodeFriendLinks(config[friendLinksKey])
	content, ok := data["Post"].(models.Content)
	if !ok || content.Type != models.ContentTypePage || !friendPageTargetMatches(content, config[friendPageTargetKey]) {
		return nil
	}
	views := make([]friendLinkView, 0, len(links))
	runtime, _ := plugin.RuntimeFromContext(ctx)
	for _, link := range links {
		view := friendLinkView{Name: link.Name, Description: link.Description, URL: link.URL}
		if link.IconURL != "" {
			view.AvatarURL = assetURL(link.IconURL)
		} else if runtime != nil && runtime.AvatarURL != nil {
			view.AvatarURL = runtime.AvatarURL(ctx, link.Email, 160)
		}
		views = append(views, view)
	}
	if config[friendShuffleKey] == "1" {
		rand.Shuffle(len(views), func(i, j int) { views[i], views[j] = views[j], views[i] })
	}
	data["IsFriendPage"] = true
	data["FriendLinks"] = views
	return nil
}

func friendCommentBadges(_ context.Context, _ *plugin.Runtime, config map[string]string, comments []plugin.PublicComment) map[int64]plugin.CommentBadge {
	links, _ := decodeFriendLinks(config[friendLinksKey])
	emails := make(map[string]bool, len(links))
	for _, link := range links {
		if email := strings.ToLower(strings.TrimSpace(link.Email)); email != "" {
			emails[email] = true
		}
	}
	badges := make(map[int64]plugin.CommentBadge)
	for _, comment := range comments {
		switch {
		case comment.AuthorID > 0 && comment.AuthorID == comment.OwnerID:
			badges[comment.COID] = plugin.CommentBadge{Label: "博主", Icon: "bolt", Tone: "owner"}
		case emails[strings.ToLower(strings.TrimSpace(comment.Mail))]:
			badges[comment.COID] = plugin.CommentBadge{Label: "好朋友", Icon: "bolt", Tone: "friend"}
		}
	}
	return badges
}

func firstFriendFormValue(form map[string][]string, name string) string {
	values := form[name]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
