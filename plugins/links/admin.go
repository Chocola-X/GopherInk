package links

import (
	"bytes"
	"context"
	"encoding/json"
	"embed"
	"fmt"
	"html/template"
	"net/url"
	"strconv"
	"strings"

	"github.com/Chocola-X/GopherInk/core/models"
	"github.com/Chocola-X/GopherInk/core/plugin"
)

//go:embed admin/*
var adminFS embed.FS

const friendAdminPageName = "friends"

type friendAdminPageData struct {
	CSRF         string
	PageTarget   string
	Shuffle      bool
	Links        []FriendLink
	TargetTitle  string
	TargetURL    string
	TargetNotice string
	Lang         string
}

var friendAdminTemplate = template.Must(template.New("friends.html").Funcs(template.FuncMap{
	"T": T,
}).ParseFS(adminFS, "admin/friends.html"))

func renderFriendAdminPage(ctx context.Context, rt *plugin.Runtime, page string, renderContext plugin.AdminPageRenderContext) (template.HTML, error) {
	if page != friendAdminPageName {
		return "", fmt.Errorf("unknown plugin page: %s", page)
	}
	links, err := DecodeFriendLinks(renderContext.Config[friendLinksKey])
	if err != nil {
		return "", fmt.Errorf("read friend link settings: %w", err)
	}
	data := friendAdminPageData{
		CSRF:       renderContext.CSRF,
		PageTarget: strings.TrimSpace(renderContext.Config[friendPageTargetKey]),
		Shuffle:    renderContext.Config[friendShuffleKey] == "1",
		Links:      friendAdminLinks(ctx, rt, links),
	}
	if rt != nil && rt.Language != nil {
		data.Lang = rt.Language(ctx)
	}
	if data.PageTarget != "" {
		content, contentErr := resolveFriendPageTarget(ctx, rt, data.PageTarget)
		if contentErr != nil {
			data.TargetNotice = T(data.Lang, contentErr.Error())
		} else {
			data.TargetTitle = content.Title
			data.TargetURL, _ = rt.ContentURL(ctx, content.CID)
		}
	}
	var output bytes.Buffer
	if err := friendAdminTemplate.ExecuteTemplate(&output, "friends.html", data); err != nil {
		return "", fmt.Errorf("render friend link settings: %w", err)
	}
	return template.HTML(output.String()), nil
}

func friendAdminLinks(ctx context.Context, rt *plugin.Runtime, links []FriendLink) []FriendLink {
	out := make([]FriendLink, len(links))
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
		return plugin.AdminPageActionResult{}, fmt.Errorf("unknown plugin page: %s", page)
	}
	if firstFormValue(form, "action") != "save-friends" {
		return plugin.AdminPageActionResult{}, fmt.Errorf("unsupported friend link action")
	}
	pageTarget := strings.TrimSpace(firstFormValue(form, friendPageTargetKey))
	if _, err := resolveFriendPageTarget(ctx, rt, pageTarget); err != nil {
		return plugin.AdminPageActionResult{}, err
	}
	links, err := friendLinksFromForm(form)
	if err != nil {
		return plugin.AdminPageActionResult{}, err
	}
	raw, err := json.Marshal(links)
	if err != nil {
		return plugin.AdminPageActionResult{}, fmt.Errorf("save friend link settings: %w", err)
	}
	shuffle := "0"
	if firstFormValue(form, friendShuffleKey) == "1" {
		shuffle = "1"
	}
	return plugin.AdminPageActionResult{
		ConfigPatch: map[string]string{
			friendPageTargetKey: pageTarget,
			friendShuffleKey:    shuffle,
			friendLinksKey:      string(raw),
		},
		Notice: plugin.AdminNotice{Type: plugin.NoticeSuccess, Mode: plugin.NoticeSnackbar, Message: "Friend link settings saved."},
	}, nil
}

func resolveFriendPageTarget(ctx context.Context, rt *plugin.Runtime, value string) (plugin.PublicContent, error) {
	target, err := ParseFriendPageTarget(value)
	if err != nil {
		return plugin.PublicContent{}, err
	}
	if rt == nil {
		return plugin.PublicContent{}, plugin.ErrRuntimeUnavailable
	}
	if rt.ListContents == nil {
		return plugin.PublicContent{}, plugin.ErrRuntimeUnavailable
	}
	query := plugin.PublicContentQuery{Type: models.ContentTypePage, Status: "all", IncludeDrafts: true, Limit: 1}
	if target.CID > 0 {
		query.CID = target.CID
	} else {
		if id, parseErr := strconv.ParseInt(target.Slug, 10, 64); parseErr == nil && id > 0 {
			query.SlugID = id
		} else {
			query.Slug = target.Slug
		}
	}
	contents, _, err := rt.ListContents(ctx, query)
	if err != nil {
		return plugin.PublicContent{}, fmt.Errorf("target page does not exist or the permalink is incorrect")
	}
	if len(contents) == 0 {
		return plugin.PublicContent{}, fmt.Errorf("target page does not exist or the permalink is incorrect")
	}
	content := contents[0]
	if content.Type != models.ContentTypePage {
		return plugin.PublicContent{}, fmt.Errorf("target content is not a page")
	}
	if content.Status != models.ContentStatusPost {
		return plugin.PublicContent{}, fmt.Errorf("target page must be published")
	}
	return content, nil
}

func assetURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "{random}") {
		value = strings.ReplaceAll(value, "{random}", "1")
	}
	if strings.HasPrefix(value, "//") || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "#") {
		return value
	}
	if u, err := url.Parse(value); err == nil && u.Scheme != "" {
		return value
	}
	return "/" + value
}
