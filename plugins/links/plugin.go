package links

import (
	"context"
	"html/template"
	"math/rand"
	"strconv"
	"strings"

	"github.com/Chocola-X/GopherInk/core/models"
	"github.com/Chocola-X/GopherInk/core/plugin"
)

const pluginName = "links"

type linksPlugin struct{}

func init() { plugin.Register(linksPlugin{}) }

func (linksPlugin) Name() string    { return pluginName }
func (linksPlugin) Version() string { return "0.1.0" }
func (linksPlugin) Description() string {
	return "Manage friend links and choose the standalone page that uses the friend-link template."
}

func (linksPlugin) Info() plugin.PluginInfo {
	return plugin.PluginInfo{
		Name:             pluginName,
		Version:          "0.1.0",
		Author:           "GopherInk",
		Description:      "Manage friend links and choose the standalone page that uses the friend-link template.",
		Homepage:         "https://gopherink.nekopara.uk",
		RequireGopherInk: "0.5.0",
	}
}

func (linksPlugin) Translate(lang, key string) string { return T(lang, key) }

func (linksPlugin) Init(m *plugin.Manager) {
	m.RegisterAdminMenu(plugin.AdminMenuItem{
		Label: "Friend Links",
		URL:   "/admin/plugins/" + pluginName + "/config?tab=" + friendAdminPageName,
		Icon:  "link",
	})
	m.RegisterService("links.list", listLinksService)
	m.RegisterService("links.config", linksConfigService)
	m.RegisterService("links.enrich", linksEnrichService)
}

func (linksPlugin) AdminPages() []plugin.AdminPage {
	return []plugin.AdminPage{{
		Name:        friendAdminPageName,
		Label:       "Friend Links",
		Icon:        "link",
		Title:       "Friend Link Settings",
		Description: "Manage friend links and choose the standalone page that uses the friend-link template.",
	}}
}

func (linksPlugin) RenderAdminPage(ctx context.Context, rt *plugin.Runtime, page string, rc plugin.AdminPageRenderContext) (template.HTML, error) {
	return renderFriendAdminPage(ctx, rt, page, rc)
}

func (linksPlugin) HandleAdminPageAction(ctx context.Context, rt *plugin.Runtime, page string, form map[string][]string) (plugin.AdminPageActionResult, error) {
	return handleFriendAdminPageAction(ctx, rt, page, form)
}

func listLinksService(ctx context.Context, rt *plugin.Runtime, args ...any) (any, error) {
	if rt == nil || rt.Config == nil {
		return nil, plugin.ErrRuntimeUnavailable
	}
	cfg, err := rt.Config(ctx, pluginName)
	if err != nil {
		return nil, err
	}
	links, err := DecodeFriendLinks(cfg[friendLinksKey])
	if err != nil {
		return nil, err
	}
	views := make([]FriendLinkView, 0, len(links))
	for _, link := range links {
		view := FriendLinkView{Name: link.Name, Description: link.Description, URL: link.URL}
		if link.IconURL != "" {
			view.AvatarURL = assetURL(link.IconURL)
		} else if rt.AvatarURL != nil {
			view.AvatarURL = rt.AvatarURL(ctx, link.Email, 160)
		}
		views = append(views, view)
	}
	if cfg[friendShuffleKey] == "1" {
		rand.Shuffle(len(views), func(i, j int) { views[i], views[j] = views[j], views[i] })
	}
	return views, nil
}

func linksConfigService(ctx context.Context, rt *plugin.Runtime, args ...any) (any, error) {
	if rt == nil || rt.Config == nil {
		return nil, plugin.ErrRuntimeUnavailable
	}
	cfg, err := rt.Config(ctx, pluginName)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		friendPageTargetKey: cfg[friendPageTargetKey],
		friendShuffleKey:    cfg[friendShuffleKey],
		friendLinksKey:      cfg[friendLinksKey],
	}, nil
}

func linksEnrichService(ctx context.Context, rt *plugin.Runtime, args ...any) (any, error) {
	if rt == nil || rt.Config == nil {
		return nil, plugin.ErrRuntimeUnavailable
	}
	cfg, err := rt.Config(ctx, pluginName)
	if err != nil {
		return nil, err
	}
	lang := "en-US"
	if rt.Language != nil {
		lang = rt.Language(ctx)
	}
	links, _ := DecodeFriendLinks(cfg[friendLinksKey])
	emails := make(map[string]bool, len(links))
	for _, link := range links {
		if email := strings.ToLower(strings.TrimSpace(link.Email)); email != "" {
			emails[email] = true
		}
	}
	return LinksEnrichResult{
		Emails: emails,
		Lang:   lang,
	}, nil
}

type LinksEnrichResult struct {
	Emails map[string]bool
	Lang   string
}

func FriendPageTargetMatches(content models.Content, value string) bool {
	target, err := ParseFriendPageTarget(value)
	if err != nil {
		return false
	}
	if target.CID > 0 {
		return content.CID == target.CID
	}
	return content.Slug == target.Slug || (content.Slug == "" && strconv.FormatInt(content.SlugID, 10) == target.Slug)
}

var _ plugin.AdminPageProvider = linksPlugin{}
var _ plugin.AdminPageActionProvider = linksPlugin{}
var _ plugin.Translator = linksPlugin{}
