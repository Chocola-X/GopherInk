package orchestration

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Chocola-X/GopherInk/core/models"
	"github.com/Chocola-X/GopherInk/core/plugin"
	"github.com/Chocola-X/GopherInk/core/services"
)

var ErrRecursiveWrite = errors.New("recursive runtime write is not allowed")

type writeContextKey struct{}

type PluginManager interface {
	ApplyActive(context.Context, string, any) (any, error)
}

type ContentSaveRequest struct {
	ID          int64
	PublishedID int64
	AuthorID    int64
	Operation   string
	Input       services.SaveContentInput
}

func ContentInputFromPlugin(input plugin.ContentWriteInput) ContentSaveRequest {
	fields := make([]services.SaveFieldInput, 0, len(input.Fields))
	for _, field := range input.Fields {
		fields = append(fields, services.SaveFieldInput{
			Name: field.Name, Type: field.Type, StrValue: field.StrValue,
			IntValue: field.IntValue, FloatValue: field.FloatValue,
		})
	}
	return ContentSaveRequest{
		ID: input.ID, PublishedID: input.PublishedID, AuthorID: input.AuthorID,
		Operation: input.Operation,
		Input: services.SaveContentInput{
			Title: input.Title, Slug: input.Slug, SlugID: input.SlugID, Text: input.Text,
			Type: input.Type, Status: input.Status, Password: input.Password, Created: input.Created,
			SortOrder: input.SortOrder, Template: input.Template, Parent: input.Parent,
			AllowComment: input.AllowComment, AllowPing: input.AllowPing, AllowFeed: input.AllowFeed,
			CategoryIDs: input.CategoryIDs, Tags: input.Tags, Fields: fields, DraftOf: input.DraftOf,
		},
	}
}

func CommentInputFromPlugin(input plugin.CommentWriteInput) CommentSaveRequest {
	return CommentSaveRequest{
		ID: input.ID, Operation: input.Operation,
		Input: services.SaveCommentInput{
			CID: input.CID, Author: input.Author, AuthorID: input.AuthorID, OwnerID: input.OwnerID,
			Mail: input.Mail, URL: input.URL, Text: input.Text, Type: input.Type, Status: input.Status,
			Parent: input.Parent, IP: input.IP, Agent: input.Agent,
		},
	}
}

type CommentSaveRequest struct {
	ID        int64
	Operation string
	Input     services.SaveCommentInput
	Content   any
}

type AutosaveRequest struct {
	ContentID int64
	AuthorID  int64
	Input     services.SaveContentInput
}

type AutosaveResult struct {
	ContentID int64
	PreviewID int64
	Content   models.Content
}

type Writer struct {
	Contents          *services.ContentService
	Comments          *services.CommentService
	Plugins           PluginManager
	DeleteContentData func(context.Context, int64) error
}

func enterWrite(ctx context.Context) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if active, _ := ctx.Value(writeContextKey{}).(bool); active {
		return ctx, ErrRecursiveWrite
	}
	return context.WithValue(ctx, writeContextKey{}, true), nil
}

func (w *Writer) SaveContent(ctx context.Context, req ContentSaveRequest) (plugin.ContentSavePayload, error) {
	ctx, err := enterWrite(ctx)
	if err != nil {
		return plugin.ContentSavePayload{ID: req.ID, PublishedID: req.PublishedID, AuthorID: req.AuthorID, Operation: req.Operation, Input: req.Input}, err
	}
	return w.saveContent(ctx, req)
}

func (w *Writer) saveContent(ctx context.Context, req ContentSaveRequest) (plugin.ContentSavePayload, error) {
	payload := plugin.ContentSavePayload{ID: req.ID, PublishedID: req.PublishedID, AuthorID: req.AuthorID, Operation: req.Operation, Input: req.Input}
	if out, err := w.Plugins.ApplyActive(ctx, plugin.HookContentBeforeSave, payload); err != nil {
		return payload, err
	} else if next, ok := out.(plugin.ContentSavePayload); ok {
		payload = next
		if nextInput, ok := next.Input.(services.SaveContentInput); ok {
			req.Input = nextInput
		}
	}

	id := req.ID
	var err error
	switch {
	case req.PublishedID > 0:
		draftID, err := w.Contents.SaveEditingDraft(ctx, req.PublishedID, req.Input, req.AuthorID)
		if err != nil {
			return payload, err
		}
		id = draftID
		if req.Input.Status == models.ContentStatusPost {
			if err := w.Contents.PublishDraft(ctx, draftID); err != nil {
				return payload, err
			}
			id = req.PublishedID
		}
	case id == 0:
		id, err = w.Contents.Create(ctx, req.Input, req.AuthorID)
		if err != nil {
			return payload, err
		}
	default:
		if err := w.Contents.Update(ctx, id, req.Input); err != nil {
			return payload, err
		}
	}

	payload.ID = id
	payload.Input = req.Input
	if content, err := w.Contents.ByID(ctx, id); err == nil {
		payload.Content = content
	} else if !errors.Is(err, sql.ErrNoRows) {
		return payload, err
	}
	_, err = w.Plugins.ApplyActive(ctx, plugin.HookContentAfterSave, payload)
	return payload, err
}

func (w *Writer) SaveAutosave(ctx context.Context, req AutosaveRequest) (AutosaveResult, error) {
	ctx, err := enterWrite(ctx)
	if err != nil {
		return AutosaveResult{ContentID: req.ContentID, PreviewID: req.ContentID}, err
	}
	autosavePayload := plugin.AutosavePayload{ContentID: req.ContentID, Input: req.Input}
	if out, err := w.Plugins.ApplyActive(ctx, plugin.HookAutosaveBeforeSave, autosavePayload); err != nil {
		return AutosaveResult{ContentID: req.ContentID, PreviewID: req.ContentID}, err
	} else if next, ok := out.(plugin.AutosavePayload); ok {
		autosavePayload = next
		if nextInput, ok := next.Input.(services.SaveContentInput); ok {
			req.Input = nextInput
		}
	}

	responseID := req.ContentID
	saveReq := ContentSaveRequest{ID: req.ContentID, AuthorID: req.AuthorID, Operation: "autosave", Input: req.Input}
	if req.ContentID > 0 {
		existing, err := w.Contents.ByID(ctx, req.ContentID)
		switch {
		case err == nil && existing.DraftOf > 0:
			saveReq.PublishedID = existing.DraftOf
			responseID = existing.DraftOf
		case err == nil && existing.Status == models.ContentStatusPost && existing.DraftOf == 0:
			saveReq.PublishedID = existing.CID
			responseID = existing.CID
		case err == nil:
			responseID = existing.CID
		default:
			return AutosaveResult{ContentID: req.ContentID, PreviewID: req.ContentID}, err
		}
	}
	savePayload, err := w.saveContent(ctx, saveReq)
	if err != nil {
		return AutosaveResult{ContentID: responseID, PreviewID: savePayload.ID}, err
	}
	previewID := savePayload.ID
	if responseID <= 0 {
		responseID = previewID
	}
	item, err := w.Contents.ByID(ctx, previewID)
	if err != nil {
		return AutosaveResult{ContentID: responseID, PreviewID: previewID}, err
	}
	autosavePayload.ContentID = responseID
	autosavePayload.Result = item
	_, _ = w.Plugins.ApplyActive(ctx, plugin.HookAutosaveAfterSave, autosavePayload)
	return AutosaveResult{ContentID: responseID, PreviewID: previewID, Content: item}, nil
}

func (w *Writer) DeleteContent(ctx context.Context, id int64) error {
	ctx, err := enterWrite(ctx)
	if err != nil {
		return err
	}
	item, err := w.Contents.ByID(ctx, id)
	if err != nil {
		return err
	}
	payload := plugin.ContentDeletePayload{ID: id, Content: item}
	if _, err := w.Plugins.ApplyActive(ctx, plugin.HookContentBeforeDelete, payload); err != nil {
		return err
	}
	if w.DeleteContentData != nil {
		err = w.DeleteContentData(ctx, id)
	} else {
		err = w.Contents.Delete(ctx, id)
	}
	if err != nil {
		return err
	}
	_, err = w.Plugins.ApplyActive(ctx, plugin.HookContentAfterDelete, payload)
	return err
}

func (w *Writer) SaveComment(ctx context.Context, req CommentSaveRequest) (plugin.CommentSavePayload, error) {
	ctx, err := enterWrite(ctx)
	if err != nil {
		return plugin.CommentSavePayload{ID: req.ID, Operation: req.Operation, Input: req.Input, Content: req.Content}, err
	}
	payload := plugin.CommentSavePayload{ID: req.ID, Operation: req.Operation, Input: req.Input, Content: req.Content}
	if payload.Content == nil && req.Input.CID > 0 {
		if parentContent, err := w.Contents.ByID(ctx, req.Input.CID); err == nil {
			payload.Content = parentContent
		}
	}
	if out, err := w.Plugins.ApplyActive(ctx, plugin.HookCommentBeforeSave, payload); err != nil {
		return payload, err
	} else if next, ok := out.(plugin.CommentSavePayload); ok {
		payload = next
		if nextInput, ok := next.Input.(services.SaveCommentInput); ok {
			req.Input = nextInput
		}
	}
	commentID, err := w.Comments.SaveReturningID(ctx, req.Input, req.ID)
	if err != nil {
		return payload, err
	}
	payload.ID = commentID
	payload.Input = req.Input
	if comment, err := w.Comments.ByID(ctx, commentID); err == nil {
		payload.Comment = comment
	}
	_, err = w.Plugins.ApplyActive(ctx, plugin.HookCommentAfterSave, payload)
	return payload, err
}

func (w *Writer) DeleteComment(ctx context.Context, id int64) error {
	ctx, err := enterWrite(ctx)
	if err != nil {
		return err
	}
	comment, err := w.Comments.ByID(ctx, id)
	if err != nil {
		return err
	}
	payload := plugin.CommentActionPayload{ID: id, PreviousStatus: comment.Status, Comment: comment}
	if content, err := w.Contents.ByID(ctx, comment.CID); err == nil {
		payload.Content = content
	}
	if _, err := w.Plugins.ApplyActive(ctx, plugin.HookCommentBeforeDelete, payload); err != nil {
		return err
	}
	if err := w.Comments.Delete(ctx, id); err != nil {
		return err
	}
	_, err = w.Plugins.ApplyActive(ctx, plugin.HookCommentAfterDelete, payload)
	return err
}
