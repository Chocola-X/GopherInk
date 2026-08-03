package defaulttheme

import (
	"context"
	"math/rand"
	"strings"

	"github.com/Chocola-X/GopherInk/core/models"
	"github.com/Chocola-X/GopherInk/core/plugin"
	"github.com/Chocola-X/GopherInk/plugins/links"
)

const (
	friendPageTargetKey = "friend_page_target"
	friendShuffleKey    = "friend_links_shuffle"
	friendLinksKey      = "friend_links"
)

type friendLinkView struct {
	Name        string
	Description string
	URL         string
	AvatarURL   string
}

func adjustDefaultThemeData(ctx context.Context, data map[string]any) error {
	content, ok := data["Post"].(models.Content)
	if !ok || content.Type != models.ContentTypePage {
		return nil
	}
	runtime, _ := plugin.RuntimeFromContext(ctx)
	if runtime == nil || runtime.ServiceAvailable == nil || !runtime.ServiceAvailable("links.config") {
		return nil
	}
	configValue, err := runtime.CallService(ctx, "links.config")
	if err != nil {
		return nil
	}
	cfg, ok := configValue.(map[string]string)
	if !ok {
		return nil
	}
	pageTarget := strings.TrimSpace(cfg[friendPageTargetKey])
	if pageTarget == "" || !links.FriendPageTargetMatches(content, pageTarget) {
		return nil
	}
	listValue, err := runtime.CallService(ctx, "links.list")
	if err != nil {
		return nil
	}
	linkViews, ok := listValue.([]links.FriendLinkView)
	if !ok {
		return nil
	}
	views := make([]friendLinkView, 0, len(linkViews))
	for _, lv := range linkViews {
		views = append(views, friendLinkView{
			Name:        lv.Name,
			Description: lv.Description,
			URL:         lv.URL,
			AvatarURL:   lv.AvatarURL,
		})
	}
	if cfg[friendShuffleKey] == "1" {
		rand.Shuffle(len(views), func(i, j int) { views[i], views[j] = views[j], views[i] })
	}
	data["IsFriendPage"] = true
	data["FriendLinks"] = views
	return nil
}

func friendEnrichComments(ctx context.Context, rt *plugin.Runtime, config map[string]string, comments []plugin.PublicComment) map[int64]plugin.CommentEnrichment {
	if rt == nil || rt.ServiceAvailable == nil || !rt.ServiceAvailable("links.enrich") {
		return nil
	}
	enrichValue, err := rt.CallService(ctx, "links.enrich")
	if err != nil {
		return nil
	}
	enrichResult, ok := enrichValue.(links.LinksEnrichResult)
	if !ok {
		return nil
	}
	lang := enrichResult.Lang
	emails := enrichResult.Emails
	enrichments := make(map[int64]plugin.CommentEnrichment)
	for _, comment := range comments {
		switch {
		case comment.AuthorID > 0 && comment.AuthorID == comment.OwnerID:
			enrichments[comment.COID] = plugin.CommentEnrichment{
				Badges: []plugin.CommentBadge{{Label: defaultThemeT(lang, "Owner"), Icon: "bolt", Tone: "owner"}},
			}
		case emails[strings.ToLower(strings.TrimSpace(comment.Mail))]:
			enrichments[comment.COID] = plugin.CommentEnrichment{
				Badges: []plugin.CommentBadge{{Label: defaultThemeT(lang, "Friend"), Icon: "bolt", Tone: "friend"}},
			}
		}
	}
	return enrichments
}
