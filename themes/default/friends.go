package defaulttheme

import (
	"context"
	"fmt"
	"math/rand"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/Chocola-X/GopherInk/core/models"
	"github.com/Chocola-X/GopherInk/core/plugin"
	"github.com/Chocola-X/GopherInk/plugins/links"
)

type friendLinkView struct {
	Name        string
	Description string
	URL         string
	AvatarURL   string
}

func adjustDefaultThemeData(ctx context.Context, data map[string]any) error {
	content, ok := data["Post"].(models.Content)
	config, _ := data["ThemeConfig"].(map[string]string)
	if !ok || content.Type != models.ContentTypePage {
		return nil
	}
	pageTarget := strings.TrimSpace(config["friend_page_target"])
	if pageTarget == "" || !friendPageTargetMatches(content, pageTarget) {
		return nil
	}
	runtime, _ := plugin.RuntimeFromContext(ctx)
	if runtime == nil || runtime.ServiceAvailable == nil || !runtime.ServiceAvailable("links.list") {
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
	if config["friend_links_shuffle"] == "1" {
		rand.Shuffle(len(views), func(i, j int) { views[i], views[j] = views[j], views[i] })
	}
	data["IsFriendPage"] = true
	data["FriendLinks"] = views
	return nil
}

func friendEnrichComments(ctx context.Context, rt *plugin.Runtime, config map[string]string, comments []plugin.PublicComment) map[int64]plugin.CommentEnrichment {
	lang := "en-US"
	if rt != nil && rt.Language != nil {
		lang = rt.Language(ctx)
	}
	var emails map[string]bool
	if rt != nil && rt.ServiceAvailable != nil && rt.ServiceAvailable("links.emails") {
		if emailsValue, err := rt.CallService(ctx, "links.emails"); err == nil {
			emails, _ = emailsValue.(map[string]bool)
		}
	}
	if emails == nil {
		emails = make(map[string]bool)
	}
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

type friendPageTarget struct {
	CID  int64
	Slug string
}

func parseFriendPageTarget(value string) (friendPageTarget, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return friendPageTarget{}, fmt.Errorf("enter a target page CID or permalink")
	}
	if id, err := strconv.ParseInt(value, 10, 64); err == nil && id > 0 {
		return friendPageTarget{CID: id}, nil
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return friendPageTarget{}, fmt.Errorf("target page permalink is invalid")
	}
	candidate := parsed.Path
	if candidate == "" {
		candidate = value
	}
	candidate = strings.TrimSpace(candidate)
	if strings.HasPrefix(candidate, "/") || strings.Contains(candidate, "/") {
		cleaned := strings.Trim(path.Clean("/"+candidate), "/")
		if !strings.HasPrefix(cleaned, "page/") {
			return friendPageTarget{}, fmt.Errorf("permalink must point to a page under /page/")
		}
		candidate = strings.TrimPrefix(cleaned, "page/")
	}
	candidate = strings.TrimSuffix(candidate, ".html")
	candidate, err = url.PathUnescape(candidate)
	if err != nil || candidate == "" || strings.Contains(candidate, "/") {
		return friendPageTarget{}, fmt.Errorf("target page permalink is invalid")
	}
	return friendPageTarget{Slug: candidate}, nil
}
