package sitemap

import (
	"encoding/xml"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Chocola-X/GopherInk/core/plugin"
)

type sitemapPlugin struct{}

func init() {
	plugin.Register(sitemapPlugin{})
}

func (sitemapPlugin) Name() string {
	return "sitemap"
}

func (sitemapPlugin) Version() string {
	return "0.5.0"
}

func (sitemapPlugin) Description() string {
	return "Generate /sitemap.xml for published content."
}

func (sitemapPlugin) Translate(lang, key string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(lang)), "zh") {
		switch key {
		case "Generate /sitemap.xml for published content.":
			return "生成 /sitemap.xml，展示已发布内容。"
		}
	}
	return key
}

func (sitemapPlugin) Init(m *plugin.Manager) {
	m.RegisterRoute(http.MethodGet, "/sitemap.xml", handleSitemap)
}

func handleSitemap(rt *plugin.Runtime, w http.ResponseWriter, r *http.Request) {
	posts, _, err := rt.ListContents(r.Context(), plugin.PublicContentQuery{
		Type:          "post",
		Status:        "publish",
		ExcludeFuture: true,
		Limit:         1000,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	baseURL, _ := rt.Option(r.Context(), "base_url")
	baseURL = sitemapBaseURL(baseURL, r)

	doc := urlSet{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	doc.URLs = append(doc.URLs, urlEntry{Loc: baseURL + "/", LastMod: sitemapDate(time.Now().Unix())})
	for _, post := range posts {
		lastMod := post.Modified
		if lastMod == 0 {
			lastMod = post.Created
		}
		path := postPath(post)
		if path == "" {
			continue
		}
		doc.URLs = append(doc.URLs, urlEntry{
			Loc:     baseURL + path,
			LastMod: sitemapDate(lastMod),
		})
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(doc)
}

func sitemapBaseURL(value string, r *http.Request) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value != "" {
		return value
	}
	scheme := "http"
	if r != nil && r.TLS != nil {
		scheme = "https"
	}
	host := ""
	if r != nil {
		host = r.Host
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func postPath(post plugin.PublicContent) string {
	slug := strings.TrimSpace(post.Slug)
	if slug == "" && post.SlugID > 0 {
		slug = strconv.FormatInt(post.SlugID, 10)
	}
	if slug == "" && post.CID > 0 {
		slug = strconv.FormatInt(post.CID, 10)
	}
	if slug == "" {
		return ""
	}
	return "/post/" + slug + ".html"
}

func sitemapDate(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).Format("2006-01-02")
}

type urlSet struct {
	XMLName xml.Name   `xml:"urlset"`
	XMLNS   string     `xml:"xmlns,attr"`
	URLs    []urlEntry `xml:"url"`
}

type urlEntry struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}
