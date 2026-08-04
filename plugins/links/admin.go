package links

import (
	"bytes"
	"context"
	"encoding/json"
	"embed"
	"fmt"
	"html/template"
	"net/url"
	"strings"

	"github.com/Chocola-X/GopherInk/core/plugin"
)

//go:embed admin/*
var adminFS embed.FS

const friendAdminPageName = "friends"

type friendAdminPageData struct {
	CSRF  string
	Links []FriendLink
	Lang  string
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
		CSRF:  renderContext.CSRF,
		Links: friendAdminLinks(ctx, rt, links),
	}
	if rt != nil && rt.Language != nil {
		data.Lang = rt.Language(ctx)
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
	links, err := friendLinksFromForm(form)
	if err != nil {
		return plugin.AdminPageActionResult{}, err
	}
	raw, err := json.Marshal(links)
	if err != nil {
		return plugin.AdminPageActionResult{}, fmt.Errorf("save friend link settings: %w", err)
	}
	return plugin.AdminPageActionResult{
		ConfigPatch: map[string]string{
			friendLinksKey: string(raw),
		},
		Notice: plugin.AdminNotice{Type: plugin.NoticeSuccess, Mode: plugin.NoticeSnackbar, Message: "Friend link settings saved."},
	}, nil
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
