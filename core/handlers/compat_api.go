package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	neturl "net/url"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Chocola-X/GopherInk/core/models"
	"github.com/Chocola-X/GopherInk/core/plugin"
	"github.com/Chocola-X/GopherInk/core/services"
	compathttp "github.com/Chocola-X/GopherInk/pkg/httpclient"
	"github.com/Chocola-X/GopherInk/pkg/render"
)

type xmlRPCMethodCall struct {
	MethodName string        `xml:"methodName"`
	Params     []xmlRPCParam `xml:"params>param"`
}

type xmlRPCParam struct {
	Value xmlRPCValue `xml:"value"`
}

type xmlRPCValue struct {
	String  *string        `xml:"string"`
	Int     *string        `xml:"int"`
	I4      *string        `xml:"i4"`
	Boolean *string        `xml:"boolean"`
	Base64  *string        `xml:"base64"`
	Date    *string        `xml:"dateTime.iso8601"`
	Struct  []xmlRPCMember `xml:"struct>member"`
	Array   []xmlRPCValue  `xml:"array>data>value"`
	Text    string         `xml:",chardata"`
}

type xmlRPCMember struct {
	Name  string      `xml:"name"`
	Value xmlRPCValue `xml:"value"`
}

func (a *App) xmlRPC(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if recover() != nil {
			a.writeXMLRPCFault(w, 400, "invalid request")
		}
	}()
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if !optionBool(a.option(r.Context(), "enable_xmlrpc", "1")) {
		a.writeXMLRPCFault(w, 403, "xml-rpc is disabled")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		a.writeXMLRPCFault(w, 400, "invalid request")
		return
	}
	var call xmlRPCMethodCall
	if err := xml.Unmarshal(body, &call); err != nil || call.MethodName == "" {
		a.writeXMLRPCFault(w, 400, "invalid request")
		return
	}
	result, fault := a.handleXMLRPCMethod(r.Context(), call.MethodName, call.Params)
	if fault != nil {
		a.writeXMLRPCFault(w, fault.Code, fault.Message)
		return
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_, _ = w.Write([]byte(xmlRPCResponse(result)))
}

type xmlRPCFault struct {
	Code    int
	Message string
}

func (a *App) handleXMLRPCMethod(ctx context.Context, method string, params []xmlRPCParam) (any, *xmlRPCFault) {
	switch method {
	case "mt.supportedMethods":
		return []any{
			"blogger.getUsersBlogs",
			"blogger.deletePost",
			"metaWeblog.getRecentPosts",
			"metaWeblog.getPost",
			"metaWeblog.newPost",
			"metaWeblog.editPost",
			"metaWeblog.getCategories",
			"metaWeblog.newMediaObject",
			"mt.getCategoryList",
			"mt.getRecentPostTitles",
			"mt.getPostCategories",
			"mt.setPostCategories",
			"mt.publishPost",
			"mt.supportedMethods",
			"wp.getPosts",
			"wp.getPost",
			"wp.newPost",
			"wp.editPost",
			"wp.deletePost",
			"wp.getCategories",
			"wp.getTags",
			"wp.uploadFile",
			"pingback.ping",
		}, nil
	case "pingback.ping":
		if len(params) < 2 {
			return nil, &xmlRPCFault{Code: 400, Message: "missing pingback parameters"}
		}
		message, err := a.receivePingback(ctx, params[0].Value.StringValue(), params[1].Value.StringValue())
		if err != nil {
			return nil, &xmlRPCFault{Code: 48, Message: err.Error()}
		}
		return message, nil
	case "blogger.getUsersBlogs":
		user, fault := a.xmlRPCUser(ctx, params, 1, 2)
		if fault != nil {
			return nil, fault
		}
		site := a.siteOptions(ctx)
		return []any{map[string]any{"blogid": "1", "blogName": site["site_title"], "url": site["base_url"], "xmlrpc": strings.TrimRight(site["base_url"], "/") + "/xmlrpc.php", "isAdmin": roleRank(user.Role) >= roleRank("administrator")}}, nil
	case "metaWeblog.getRecentPosts", "wp.getPosts":
		user, fault := a.xmlRPCUser(ctx, params, 1, 2)
		if method == "wp.getPosts" {
			user, fault = a.xmlRPCUser(ctx, params, 1, 2)
		}
		if fault != nil {
			return nil, fault
		}
		if roleRank(user.Role) < roleRank("contributor") {
			return nil, &xmlRPCFault{Code: 403, Message: "permission denied"}
		}
		limit := 10
		if len(params) > 3 {
			limit = params[3].Value.IntValue(10)
		}
		items, err := a.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypePost, Status: "all", Limit: limit})
		if err != nil {
			return nil, &xmlRPCFault{Code: 500, Message: "internal error"}
		}
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, a.xmlRPCPostStruct(ctx, item))
		}
		return out, nil
	case "metaWeblog.getPost", "wp.getPost":
		user, fault := a.xmlRPCUser(ctx, params, 1, 2)
		postIDIndex := 0
		if method == "wp.getPost" {
			user, fault = a.xmlRPCUser(ctx, params, 1, 2)
			postIDIndex = 3
		}
		if fault != nil {
			return nil, fault
		}
		item, err := a.Contents.ByID(ctx, params[postIDIndex].Value.Int64Value())
		if err != nil {
			return nil, &xmlRPCFault{Code: 404, Message: "post not found"}
		}
		if !a.xmlRPCCanEdit(user, item) && roleRank(user.Role) < roleRank("contributor") {
			return nil, &xmlRPCFault{Code: 403, Message: "permission denied"}
		}
		return a.xmlRPCPostStruct(ctx, item), nil
	case "mt.getRecentPostTitles":
		user, fault := a.xmlRPCUser(ctx, params, 1, 2)
		if fault != nil {
			return nil, fault
		}
		if roleRank(user.Role) < roleRank("contributor") {
			return nil, &xmlRPCFault{Code: 403, Message: "permission denied"}
		}
		limit := 10
		if len(params) > 3 {
			limit = params[3].Value.IntValue(10)
		}
		items, err := a.Contents.List(ctx, services.ContentQuery{Type: models.ContentTypePost, Status: "all", Limit: limit})
		if err != nil {
			return nil, &xmlRPCFault{Code: 500, Message: "internal error"}
		}
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, map[string]any{
				"postid":      strconv.FormatInt(item.CID, 10),
				"userid":      strconv.FormatInt(item.AuthorID, 10),
				"title":       item.Title,
				"dateCreated": time.Unix(item.Created, 0).UTC(),
			})
		}
		return out, nil
	case "mt.getPostCategories":
		user, fault := a.xmlRPCUser(ctx, params, 1, 2)
		if fault != nil {
			return nil, fault
		}
		item, err := a.Contents.ByID(ctx, params[0].Value.Int64Value())
		if err != nil {
			return nil, &xmlRPCFault{Code: 404, Message: "post not found"}
		}
		if !a.xmlRPCCanEdit(user, item) && roleRank(user.Role) < roleRank("contributor") {
			return nil, &xmlRPCFault{Code: 403, Message: "permission denied"}
		}
		categories, err := a.Metas.CategoriesForContent(ctx, item.CID)
		if err != nil {
			return nil, &xmlRPCFault{Code: 500, Message: "internal error"}
		}
		out := make([]any, 0, len(categories))
		for i, category := range categories {
			out = append(out, map[string]any{"categoryId": strconv.FormatInt(category.MID, 10), "categoryName": category.Name, "isPrimary": i == 0})
		}
		return out, nil
	case "mt.setPostCategories":
		user, fault := a.xmlRPCUser(ctx, params, 1, 2)
		if fault != nil {
			return nil, fault
		}
		item, err := a.Contents.ByID(ctx, params[0].Value.Int64Value())
		if err != nil {
			return nil, &xmlRPCFault{Code: 404, Message: "post not found"}
		}
		if !a.xmlRPCCanEdit(user, item) {
			return nil, &xmlRPCFault{Code: 403, Message: "permission denied"}
		}
		input := contentToSaveInput(ctx, a, item)
		if len(params) > 3 {
			input.CategoryIDs = a.xmlRPCCategoryIDs(ctx, params[3].Value)
		}
		if err := a.Contents.Update(ctx, item.CID, input); err != nil {
			return nil, &xmlRPCFault{Code: 500, Message: "internal error"}
		}
		return true, nil
	case "mt.publishPost":
		user, fault := a.xmlRPCUser(ctx, params, 1, 2)
		if fault != nil {
			return nil, fault
		}
		item, err := a.Contents.ByID(ctx, params[0].Value.Int64Value())
		if err != nil {
			return nil, &xmlRPCFault{Code: 404, Message: "post not found"}
		}
		if !a.xmlRPCCanEdit(user, item) {
			return nil, &xmlRPCFault{Code: 403, Message: "permission denied"}
		}
		if err := a.markContentStatus(ctx, item.CID, models.ContentStatusPost); err != nil {
			return nil, &xmlRPCFault{Code: 500, Message: "internal error"}
		}
		return true, nil
	case "metaWeblog.newPost", "wp.newPost":
		user, fault := a.xmlRPCUser(ctx, params, 1, 2)
		contentIndex, publishIndex := 3, 4
		if method == "wp.newPost" {
			user, fault = a.xmlRPCUser(ctx, params, 1, 2)
			contentIndex, publishIndex = 3, -1
		}
		if fault != nil {
			return nil, fault
		}
		if roleRank(user.Role) < roleRank("contributor") {
			return nil, &xmlRPCFault{Code: 403, Message: "permission denied"}
		}
		input := a.xmlRPCContentInput(ctx, params[contentIndex].Value, true)
		if publishIndex >= 0 && len(params) > publishIndex && !params[publishIndex].Value.BoolValue() {
			input.Status = models.ContentStatusDraft
		}
		operation := "publish"
		if input.Status != models.ContentStatusPost {
			operation = "draft"
		}
		if roleRank(user.Role) < roleRank("editor") && input.Status == models.ContentStatusPost {
			input.Status = "waiting"
			input.Password = ""
		}
		id, err := a.saveContentWithHooks(ctx, 0, input, user.UID, operation)
		if err != nil {
			return nil, &xmlRPCFault{Code: 500, Message: "internal error"}
		}
		a.sendOutgoingPings(ctx, id, input)
		return strconv.FormatInt(id, 10), nil
	case "metaWeblog.editPost", "wp.editPost":
		user, fault := a.xmlRPCUser(ctx, params, 1, 2)
		postIDIndex, contentIndex, publishIndex := 0, 3, 4
		if method == "wp.editPost" {
			user, fault = a.xmlRPCUser(ctx, params, 1, 2)
			postIDIndex, contentIndex, publishIndex = 3, 4, -1
		}
		if fault != nil {
			return nil, fault
		}
		item, err := a.Contents.ByID(ctx, params[postIDIndex].Value.Int64Value())
		if err != nil {
			return nil, &xmlRPCFault{Code: 404, Message: "post not found"}
		}
		if !a.xmlRPCCanEdit(user, item) {
			return nil, &xmlRPCFault{Code: 403, Message: "permission denied"}
		}
		input := a.xmlRPCContentInput(ctx, params[contentIndex].Value, item.Type == "")
		if input.Type == "" {
			input.Type = item.Type
		}
		if publishIndex >= 0 && len(params) > publishIndex && !params[publishIndex].Value.BoolValue() {
			input.Status = models.ContentStatusDraft
		}
		operation := "publish"
		if input.Status != models.ContentStatusPost {
			operation = "draft"
		}
		if roleRank(user.Role) < roleRank("editor") {
			input.Password = item.Password
			if item.Status != models.ContentStatusPost && input.Status == models.ContentStatusPost {
				input.Status = "waiting"
			}
		}
		if _, err := a.saveContentWithHooks(ctx, item.CID, input, user.UID, operation); err != nil {
			return nil, &xmlRPCFault{Code: 500, Message: "internal error"}
		}
		a.sendOutgoingPings(ctx, item.CID, input)
		return true, nil
	case "blogger.deletePost", "wp.deletePost":
		postIDIndex, userIndex, passIndex := 1, 2, 3
		if method == "wp.deletePost" {
			postIDIndex, userIndex, passIndex = 3, 1, 2
		}
		user, fault := a.xmlRPCUser(ctx, params, userIndex, passIndex)
		if fault != nil {
			return nil, fault
		}
		item, err := a.Contents.ByID(ctx, params[postIDIndex].Value.Int64Value())
		if err != nil {
			return nil, &xmlRPCFault{Code: 404, Message: "post not found"}
		}
		if !a.xmlRPCCanEdit(user, item) {
			return nil, &xmlRPCFault{Code: 403, Message: "permission denied"}
		}
		if err := a.deleteContentWithAttachmentPolicy(ctx, item.CID); err != nil {
			return nil, &xmlRPCFault{Code: 500, Message: "internal error"}
		}
		return true, nil
	case "metaWeblog.getCategories", "mt.getCategoryList", "wp.getCategories":
		if _, fault := a.xmlRPCUser(ctx, params, 1, 2); fault != nil {
			return nil, fault
		}
		categories, err := a.Metas.List(ctx, "category")
		if err != nil {
			return nil, &xmlRPCFault{Code: 500, Message: "internal error"}
		}
		out := make([]any, 0, len(categories))
		for _, c := range categories {
			out = append(out, map[string]any{"categoryId": strconv.FormatInt(c.MID, 10), "categoryName": c.Name, "description": c.Description, "htmlUrl": a.metaURL(ctx, c), "rssUrl": a.metaURL(ctx, c) + "/feed.xml"})
		}
		return out, nil
	case "wp.getTags":
		if _, fault := a.xmlRPCUser(ctx, params, 1, 2); fault != nil {
			return nil, fault
		}
		tags, err := a.Metas.List(ctx, "tag")
		if err != nil {
			return nil, &xmlRPCFault{Code: 500, Message: "internal error"}
		}
		out := make([]any, 0, len(tags))
		for _, t := range tags {
			out = append(out, map[string]any{"tag_id": strconv.FormatInt(t.MID, 10), "name": t.Name, "slug": t.Slug, "count": t.Count})
		}
		return out, nil
	case "metaWeblog.newMediaObject", "wp.uploadFile":
		user, fault := a.xmlRPCUser(ctx, params, 1, 2)
		if fault != nil {
			return nil, fault
		}
		if roleRank(user.Role) < roleRank("contributor") {
			return nil, &xmlRPCFault{Code: 403, Message: "permission denied"}
		}
		if len(params) < 4 {
			return nil, &xmlRPCFault{Code: 400, Message: "missing media object"}
		}
		media := params[3].Value.StructMap()
		name := media["name"].StringValue()
		if name == "" {
			name = "upload.bin"
		}
		data := media["bits"].BytesValue()
		uploadPayload := plugin.XMLRPCUploadPayload{Name: name, Data: data}
		if out, err := a.Plugins.ApplyActive(ctx, plugin.HookXMLRPCUpload, uploadPayload); err != nil {
			return nil, &xmlRPCFault{Code: 500, Message: err.Error()}
		} else if next, ok := out.(plugin.XMLRPCUploadPayload); ok {
			if next.Handled {
				return next.Result, nil
			}
			name = firstNonEmpty(next.Name, name)
			if next.Data != nil {
				data = next.Data
			}
		}
		saved, err := a.saveUpload(ctx, bytes.NewReader(data), name, 0)
		if err != nil {
			return nil, &xmlRPCFault{Code: 400, Message: "invalid upload"}
		}
		meta := saved.Meta
		text, _ := json.Marshal(meta)
		id, err := a.Contents.CreateAttachmentMeta(ctx, meta.Name, strings.TrimSuffix(filepath.Base(meta.Name), filepath.Ext(meta.Name)), string(text), user.UID, 0)
		if err != nil {
			return nil, &xmlRPCFault{Code: 500, Message: "internal error"}
		}
		if item, itemErr := a.Contents.ByID(ctx, id); itemErr == nil {
			meta = a.attachmentMeta(ctx, item)
		}
		site := a.siteOptions(ctx)
		return map[string]any{"id": strconv.FormatInt(id, 10), "file": meta.Path, "url": absolutePublicURL(strings.TrimRight(site["base_url"], "/"), meta.URL), "type": meta.MIME}, nil
	default:
		return nil, &xmlRPCFault{Code: 404, Message: "method not found"}
	}
}

func (a *App) xmlRPCUser(ctx context.Context, params []xmlRPCParam, userIndex, passIndex int) (models.User, *xmlRPCFault) {
	if len(params) <= userIndex || len(params) <= passIndex {
		return models.User{}, &xmlRPCFault{Code: 401, Message: "authentication failed"}
	}
	user, err := a.authenticateUserWithHooks(ctx, params[userIndex].Value.StringValue(), params[passIndex].Value.StringValue())
	if err != nil {
		return models.User{}, &xmlRPCFault{Code: 401, Message: "authentication failed"}
	}
	return user, nil
}

func (a *App) xmlRPCCanEdit(user models.User, content models.Content) bool {
	if content.Type == models.ContentTypePage && roleRank(user.Role) < roleRank("editor") {
		return false
	}
	return roleRank(user.Role) >= roleRank("editor") || content.AuthorID == user.UID
}

func (a *App) xmlRPCContentInput(ctx context.Context, value xmlRPCValue, publish bool) services.SaveContentInput {
	m := value.StructMap()
	status := models.ContentStatusDraft
	if publish {
		status = models.ContentStatusPost
	}
	if rawStatus := m["post_status"].StringValue(); rawStatus != "" {
		if rawStatus == "publish" || rawStatus == "published" {
			status = models.ContentStatusPost
		} else {
			status = models.ContentStatusDraft
		}
	}
	text := m["description"].StringValue()
	if more := m["mt_text_more"].StringValue(); more != "" {
		text += "\n\n<!--more-->\n\n" + more
	}
	if out, err := a.Plugins.ApplyActive(ctx, plugin.HookXMLRPCTextFilter, plugin.XMLRPCTextPayload{Method: "content", Text: text}); err == nil {
		if next, ok := out.(plugin.XMLRPCTextPayload); ok {
			text = next.Text
		}
	}
	input := services.SaveContentInput{
		Title:        firstNonEmpty(m["title"].StringValue(), "Untitled"),
		Slug:         firstNonEmpty(m["wp_slug"].StringValue(), m["post_name"].StringValue()),
		Text:         text,
		Type:         models.ContentTypePost,
		Status:       status,
		Password:     m["wp_password"].StringValue(),
		AllowComment: true,
		AllowFeed:    true,
		AllowPing:    optionBool(a.option(ctx, "enable_pingback", "1")),
		Tags:         splitTags(firstNonEmpty(m["mt_keywords"].StringValue(), m["terms_names"].StructMap()["post_tag"].StringValue())),
	}
	if created := m["dateCreated"].TimeValue(); created > 0 {
		input.Created = created
	}
	categories := m["categories"].ArrayStrings()
	if len(categories) > 0 {
		input.CategoryIDs = a.categoryIDsByName(ctx, categories)
	}
	return input
}

func (a *App) xmlRPCPostStruct(ctx context.Context, c models.Content) map[string]any {
	site := a.siteOptions(ctx)
	link := absolutePublicURL(strings.TrimRight(site["base_url"], "/"), a.contentURL(ctx, c))
	categories, _ := a.Metas.CategoriesForContent(ctx, c.CID)
	tags, _ := a.Metas.TagsForContent(ctx, c.CID)
	categoryNames := make([]any, 0, len(categories))
	for _, cat := range categories {
		categoryNames = append(categoryNames, cat.Name)
	}
	tagNames := make([]string, 0, len(tags))
	for _, tag := range tags {
		tagNames = append(tagNames, tag.Name)
	}
	return map[string]any{
		"postid":      strconv.FormatInt(c.CID, 10),
		"post_id":     strconv.FormatInt(c.CID, 10),
		"title":       c.Title,
		"description": c.Text,
		"link":        link,
		"permaLink":   link,
		"userid":      strconv.FormatInt(c.AuthorID, 10),
		"dateCreated": time.Unix(c.Created, 0).UTC(),
		"post_status": c.Status,
		"wp_slug":     c.Slug,
		"mt_keywords": strings.Join(tagNames, ","),
		"categories":  categoryNames,
	}
}

func (a *App) xmlRPCCategoryIDs(ctx context.Context, value xmlRPCValue) []int64 {
	var ids []int64
	for _, item := range value.Array {
		m := item.StructMap()
		id := m["categoryId"].Int64Value()
		if id == 0 {
			id = m["categoryID"].Int64Value()
		}
		if id > 0 {
			ids = append(ids, id)
			continue
		}
		name := firstNonEmpty(m["categoryName"].StringValue(), item.StringValue())
		if name != "" {
			ids = append(ids, a.categoryIDsByName(ctx, []string{name})...)
		}
	}
	return ids
}

func contentToSaveInput(ctx context.Context, a *App, item models.Content) services.SaveContentInput {
	categories, _ := a.Metas.CategoriesForContent(ctx, item.CID)
	tags, _ := a.Metas.TagsForContent(ctx, item.CID)
	input := services.SaveContentInput{
		Title:        item.Title,
		Slug:         item.Slug,
		Text:         item.Text,
		Type:         item.Type,
		Status:       item.Status,
		Password:     item.Password,
		Created:      item.Created,
		SortOrder:    item.SortOrder,
		Template:     item.Template,
		Parent:       item.Parent,
		AllowComment: item.AllowComment == "1",
		AllowPing:    item.AllowPing == "1",
		AllowFeed:    item.AllowFeed == "1",
	}
	for _, category := range categories {
		input.CategoryIDs = append(input.CategoryIDs, category.MID)
	}
	for _, tag := range tags {
		input.Tags = append(input.Tags, tag.Name)
	}
	return input
}

func (a *App) receivePingback(ctx context.Context, sourceURI, targetURI string) (string, error) {
	pingPayload := plugin.XMLRPCPingbackPayload{SourceURI: sourceURI, TargetURI: targetURI}
	if out, err := a.Plugins.ApplyActive(ctx, plugin.HookXMLRPCPingback, pingPayload); err != nil {
		return "", err
	} else if next, ok := out.(plugin.XMLRPCPingbackPayload); ok {
		if next.Handled {
			if next.Message != "" {
				return next.Message, nil
			}
			return "Pingback registered.", nil
		}
		sourceURI = next.SourceURI
		targetURI = next.TargetURI
	}
	if !optionBool(a.option(ctx, "enable_pingback", "1")) {
		return "", errors.New("pingback is disabled")
	}
	if sourceURI == "" || targetURI == "" || sourceURI == targetURI {
		return "", errors.New("invalid pingback")
	}
	if a.sameSiteURL(ctx, sourceURI) {
		return "", errors.New("self pingback is not allowed")
	}
	content, err := a.contentByPublicURL(ctx, targetURI)
	if err != nil {
		return "", errors.New("target not found")
	}
	if content.AllowPing != "1" {
		return "", errors.New("pingback is not allowed")
	}
	exists, err := a.Comments.ExistsByURLType(ctx, content.CID, sourceURI, "pingback")
	if err != nil {
		return "", errors.New("internal error")
	}
	if exists {
		return "", errors.New("duplicate pingback")
	}
	page, err := a.fetchExternalText(ctx, sourceURI)
	if err != nil {
		return "", errors.New("source cannot be fetched")
	}
	if !strings.Contains(page, targetURI) && !strings.Contains(page, html.EscapeString(targetURI)) {
		return "", errors.New("source does not link to target")
	}
	author := sourceHost(sourceURI)
	text := render.Excerpt(stripHTML(page), 240)
	if text == "" {
		text = sourceURI
	}
	commentPayload, err := a.saveCommentWithHooks(ctx, services.SaveCommentInput{CID: content.CID, Author: author, URL: sourceURI, Text: text, Type: "pingback", Status: "approved"}, 0, "pingback", content)
	if err != nil {
		return "", errors.New("internal error")
	}
	finishPayload := plugin.XMLRPCPingbackPayload{SourceURI: sourceURI, TargetURI: targetURI, Content: a.contentToPublic(content), Message: "Pingback registered."}
	if comment, ok := commentPayload.Comment.(models.Comment); ok {
		finishPayload.Comment = a.commentToPublic(comment)
	}
	_, _ = a.Plugins.ApplyActive(ctx, plugin.HookXMLRPCFinishPingback, finishPayload)
	return "Pingback registered.", nil
}

func (a *App) trackback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if !optionBool(a.option(r.Context(), "enable_trackback", "1")) {
		writeTrackbackResponse(w, 1, "trackback is disabled")
		return
	}
	cid, err := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/trackback/"), "/"), 10, 64)
	if err != nil || cid <= 0 {
		writeTrackbackResponse(w, 1, "invalid target")
		return
	}
	content, err := a.Contents.ByID(r.Context(), cid)
	if err != nil || content.AllowPing != "1" {
		writeTrackbackResponse(w, 1, "trackback is not allowed")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeTrackbackResponse(w, 1, "invalid request")
		return
	}
	source := strings.TrimSpace(r.FormValue("url"))
	if source == "" {
		writeTrackbackResponse(w, 1, "url is required")
		return
	}
	exists, err := a.Comments.ExistsByURLType(r.Context(), cid, source, "trackback")
	if err != nil {
		writeTrackbackResponse(w, 1, "internal error")
		return
	}
	if exists {
		writeTrackbackResponse(w, 1, "duplicate trackback")
		return
	}
	author := firstNonEmpty(strings.TrimSpace(r.FormValue("blog_name")), strings.TrimSpace(r.FormValue("title")), sourceHost(source))
	text := strings.TrimSpace(r.FormValue("excerpt"))
	if title := strings.TrimSpace(r.FormValue("title")); title != "" && text != "" {
		text = title + "\n\n" + text
	} else if title != "" {
		text = title
	}
	input := services.SaveCommentInput{CID: cid, Author: author, URL: source, Text: text, Type: "trackback", Status: "approved", IP: a.clientIP(r), Agent: r.UserAgent()}
	trackPayload := plugin.TrackbackPayload{Content: a.contentToPublic(content), Input: input}
	if out, err := a.Plugins.ApplyActive(r.Context(), plugin.HookTrackback, trackPayload); err != nil {
		writeTrackbackResponse(w, 1, err.Error())
		return
	} else if next, ok := out.(plugin.TrackbackPayload); ok {
		if next.Handled {
			writeTrackbackResponse(w, 0, "")
			return
		}
		if nextInput, ok := next.Input.(services.SaveCommentInput); ok {
			input = nextInput
		}
	}
	commentPayload, err := a.saveCommentWithHooks(r.Context(), input, 0, "trackback", content)
	if err != nil {
		writeTrackbackResponse(w, 1, "internal error")
		return
	}
	finishPayload := plugin.TrackbackPayload{Content: a.contentToPublic(content), Input: input}
	if comment, ok := commentPayload.Comment.(models.Comment); ok {
		finishPayload.Comment = a.commentToPublic(comment)
	}
	_, _ = a.Plugins.ApplyActive(r.Context(), plugin.HookFinishTrackback, finishPayload)
	writeTrackbackResponse(w, 0, "")
}

func (a *App) sendOutgoingPings(ctx context.Context, cid int64, input services.SaveContentInput) {
	if input.Status != models.ContentStatusPost || !input.AllowPing {
		return
	}
	urls := extractHTTPLinks(input.Text)
	if len(urls) == 0 {
		return
	}
	site := a.siteOptions(ctx)
	content, err := a.Contents.ByID(ctx, cid)
	if err != nil {
		return
	}
	source := strings.TrimRight(site["base_url"], "/") + a.contentURL(ctx, content)
	for _, target := range urls {
		target := target
		go a.sendSinglePing(context.Background(), source, target)
	}
}

func (a *App) sendSinglePing(ctx context.Context, source, target string) {
	if source == "" || target == "" || source == target {
		return
	}
	client := a.compatHTTPClient(ctx)
	page, err := client.GetText(ctx, target)
	status := "failed"
	if err == nil {
		if endpoint := discoverPingbackEndpoint(page); endpoint != "" {
			body := xmlRPCRequestBody("pingback.ping", source, target)
			if err = client.PostXML(ctx, endpoint, body); err == nil {
				status = "pingback sent"
			}
		} else if endpoint := discoverTrackbackEndpoint(page); endpoint != "" {
			form := neturl.Values{"url": {source}, "title": {"GopherInk"}, "blog_name": {"GopherInk"}}
			if err = client.PostForm(ctx, endpoint, form.Encode()); err == nil {
				status = "trackback sent"
			}
		} else {
			status = "no endpoint"
		}
	}
	_ = a.Options.Set(ctx, "last_ping_target", target)
	_ = a.Options.Set(ctx, "last_ping_status", status)
	if err != nil {
		_ = a.Options.Set(ctx, "last_ping_error", err.Error())
	}
	_ = a.Options.Set(ctx, "last_ping_time", strconv.FormatInt(time.Now().Unix(), 10))
}

func (a *App) rsdXML(w http.ResponseWriter, r *http.Request) {
	if !optionBool(a.option(r.Context(), "enable_xmlrpc", "1")) {
		http.NotFound(w, r)
		return
	}
	site := a.siteOptions(r.Context())
	endpoint := strings.TrimRight(site["base_url"], "/") + "/xmlrpc.php"
	w.Header().Set("Content-Type", "application/rsd+xml; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><rsd version="1.0" xmlns="http://archipelago.phrasewise.com/rsd"><service><engineName>GopherInk</engineName><engineLink>%s</engineLink><homePageLink>%s</homePageLink><apis><api name="MetaWeblog" preferred="true" apiLink="%s" blogID="1"/><api name="Blogger" preferred="false" apiLink="%s" blogID="1"/><api name="WordPress" preferred="false" apiLink="%s" blogID="1"/></apis></service></rsd>`, xmlEscape(site["base_url"]), xmlEscape(site["base_url"]), xmlEscape(endpoint), xmlEscape(endpoint), xmlEscape(endpoint))
}

func (a *App) wlwManifest(w http.ResponseWriter, r *http.Request) {
	if !optionBool(a.option(r.Context(), "enable_xmlrpc", "1")) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/wlwmanifest+xml; charset=utf-8")
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><manifest xmlns="http://schemas.microsoft.com/wlw/manifest/weblog"><options><clientType>Metaweblog</clientType><supportsNewCategories>Yes</supportsNewCategories><supportsFileUpload>Yes</supportsFileUpload><supportsSlug>Yes</supportsSlug></options></manifest>`))
}

func (a *App) writeXMLRPCFault(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><fault><value><struct><member><name>faultCode</name><value><int>%d</int></value></member><member><name>faultString</name><value><string>%s</string></value></member></struct></value></fault></methodResponse>`, code, xmlEscape(message))
}

func xmlRPCResponse(value any) string {
	return `<?xml version="1.0"?><methodResponse><params><param><value>` + xmlRPCEncode(value) + `</value></param></params></methodResponse>`
}

func xmlRPCEncode(value any) string {
	switch v := value.(type) {
	case nil:
		return `<string></string>`
	case bool:
		if v {
			return `<boolean>1</boolean>`
		}
		return `<boolean>0</boolean>`
	case int:
		return `<int>` + strconv.Itoa(v) + `</int>`
	case int64:
		return `<int>` + strconv.FormatInt(v, 10) + `</int>`
	case string:
		return `<string>` + xmlEscape(v) + `</string>`
	case time.Time:
		return `<dateTime.iso8601>` + v.UTC().Format("20060102T15:04:05") + `</dateTime.iso8601>`
	case []any:
		var b strings.Builder
		b.WriteString(`<array><data>`)
		for _, item := range v {
			b.WriteString(`<value>`)
			b.WriteString(xmlRPCEncode(item))
			b.WriteString(`</value>`)
		}
		b.WriteString(`</data></array>`)
		return b.String()
	case map[string]any:
		var b strings.Builder
		b.WriteString(`<struct>`)
		for key, item := range v {
			b.WriteString(`<member><name>`)
			b.WriteString(xmlEscape(key))
			b.WriteString(`</name><value>`)
			b.WriteString(xmlRPCEncode(item))
			b.WriteString(`</value></member>`)
		}
		b.WriteString(`</struct>`)
		return b.String()
	default:
		return `<string>` + xmlEscape(fmt.Sprint(v)) + `</string>`
	}
}

func (v xmlRPCValue) StringValue() string {
	if v.String != nil {
		return *v.String
	}
	if v.Int != nil {
		return *v.Int
	}
	if v.I4 != nil {
		return *v.I4
	}
	if v.Boolean != nil {
		return *v.Boolean
	}
	return strings.TrimSpace(v.Text)
}

func (v xmlRPCValue) IntValue(fallback int) int {
	n, err := strconv.Atoi(v.StringValue())
	if err != nil {
		return fallback
	}
	return n
}

func (v xmlRPCValue) Int64Value() int64 {
	n, _ := strconv.ParseInt(v.StringValue(), 10, 64)
	return n
}

func (v xmlRPCValue) BoolValue() bool {
	raw := strings.TrimSpace(v.StringValue())
	return raw == "1" || strings.EqualFold(raw, "true")
}

func (v xmlRPCValue) BytesValue() []byte {
	raw := strings.TrimSpace(v.StringValue())
	if v.Base64 != nil {
		raw = strings.TrimSpace(*v.Base64)
	}
	data, _ := base64.StdEncoding.DecodeString(raw)
	return data
}

func (v xmlRPCValue) StructMap() map[string]xmlRPCValue {
	out := map[string]xmlRPCValue{}
	for _, member := range v.Struct {
		out[member.Name] = member.Value
	}
	return out
}

func (v xmlRPCValue) ArrayStrings() []string {
	out := make([]string, 0, len(v.Array))
	for _, item := range v.Array {
		if s := strings.TrimSpace(item.StringValue()); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (v xmlRPCValue) TimeValue() int64 {
	raw := strings.TrimSpace(v.StringValue())
	if v.Date != nil {
		raw = strings.TrimSpace(*v.Date)
	}
	for _, layout := range []string{"20060102T15:04:05", time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func (a *App) categoryIDsByName(ctx context.Context, names []string) []int64 {
	categories, err := a.Metas.List(ctx, "category")
	if err != nil {
		return nil
	}
	var ids []int64
	for _, name := range names {
		for _, category := range categories {
			if strings.EqualFold(category.Name, name) || strings.EqualFold(category.Slug, name) {
				ids = append(ids, category.MID)
				break
			}
		}
	}
	return ids
}

func (a *App) contentByPublicURL(ctx context.Context, rawURL string) (models.Content, error) {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return models.Content{}, err
	}
	targetPath := cleanPublicPath(u.Path)
	for _, typ := range []string{models.ContentTypePost, models.ContentTypePage} {
		items, err := a.Contents.List(ctx, services.ContentQuery{Type: typ, Status: models.ContentStatusPost, Limit: 10000})
		if err != nil {
			return models.Content{}, err
		}
		for _, item := range items {
			if cleanPublicPath(a.contentURL(ctx, item)) == targetPath {
				return item, nil
			}
		}
	}
	if slug := path.Base(targetPath); slug != "." && slug != "/" {
		if c, err := a.Contents.BySlug(ctx, slug); err == nil {
			return c, nil
		}
		if c, err := a.Contents.PageBySlug(ctx, slug); err == nil {
			return c, nil
		}
	}
	return models.Content{}, sql.ErrNoRows
}

func (a *App) sameSiteURL(ctx context.Context, rawURL string) bool {
	site := a.siteOptions(ctx)
	base, err := neturl.Parse(site["base_url"])
	if err != nil || base.Hostname() == "" {
		return false
	}
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(base.Hostname(), u.Hostname())
}

func (a *App) compatHTTPClient(ctx context.Context) *compathttp.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	client, _ := compathttp.New(compathttp.Config{
		Timeout:   compathttp.ParseTimeoutSeconds(a.option(ctx, "http_client_timeout", "5"), 5*time.Second),
		UserAgent: firstNonEmpty(a.option(ctx, "http_client_user_agent", ""), "GopherInk/0.5.0"),
		Proxy:     a.option(ctx, "http_client_proxy", ""),
		Retries:   optionInt(a.option(ctx, "http_client_retries", "1"), 1),
	})
	return client
}

func (a *App) fetchExternalText(ctx context.Context, rawURL string) (string, error) {
	if a.HTTPFetch != nil {
		return a.HTTPFetch(ctx, rawURL)
	}
	return a.compatHTTPClient(ctx).GetText(ctx, rawURL)
}

func (a *App) siteOptions(ctx context.Context) map[string]string {
	site, err := a.Options.All(ctx)
	if err != nil {
		return map[string]string{"site_title": "GopherInk", "base_url": "http://localhost:8086"}
	}
	return site
}

func writeTrackbackResponse(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	if code == 0 {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><response><error>0</error></response>`))
		return
	}
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><response><error>1</error><message>%s</message></response>`, xmlEscape(message))
}

func xmlEscape(value string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

func stripHTML(value string) string {
	var b strings.Builder
	inTag := false
	for _, r := range value {
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
	return html.UnescapeString(b.String())
}

func extractHTTPLinks(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == '"' || r == '\'' || r == '<' || r == '>' || r == '(' || r == ')' || r == '[' || r == ']' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	seen := map[string]bool{}
	var out []string
	for _, field := range fields {
		field = strings.TrimRight(field, ".,;")
		if (strings.HasPrefix(field, "http://") || strings.HasPrefix(field, "https://")) && !seen[field] {
			seen[field] = true
			out = append(out, field)
		}
	}
	return out
}

func discoverPingbackEndpoint(page string) string {
	return discoverEndpoint(page, `(?is)<link[^>]+rel=["'][^"']*pingback[^"']*["'][^>]+href=["']([^"']+)["']`)
}

func discoverTrackbackEndpoint(page string) string {
	return discoverEndpoint(page, `(?is)trackback:ping=["']([^"']+)["']`)
}

func discoverEndpoint(page, pattern string) string {
	matches := regexp.MustCompile(pattern).FindStringSubmatch(page)
	if len(matches) < 2 {
		return ""
	}
	return html.UnescapeString(matches[1])
}

func xmlRPCRequestBody(method string, values ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><methodCall><methodName>`)
	b.WriteString(xmlEscape(method))
	b.WriteString(`</methodName><params>`)
	for _, value := range values {
		b.WriteString(`<param><value><string>`)
		b.WriteString(xmlEscape(value))
		b.WriteString(`</string></value></param>`)
	}
	b.WriteString(`</params></methodCall>`)
	return b.String()
}

func sourceHost(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return raw
	}
	return u.Hostname()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
