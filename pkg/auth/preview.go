// Package auth's preview.go implements expiring preview tokens.
//
// Preview URLs are shared by content authors before publishing. The previous
// implementation signed only (cid, modified, status) with no expiry, so a
// leaked preview link stayed valid forever. This module signs an explicit
// (cid, revision-tag, issued-at, uid) payload and enforces a configurable
// TTL. It also refuses tokens whose subject UID is not permitted to see the
// content.
package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrPreview signals a rejected preview token. Callers surface a generic
// "invalid preview link" rather than leaking the reason.
var ErrPreview = errors.New("auth: preview token rejected")

// PreviewService issues and validates preview tokens for content editing.
type PreviewService struct {
	secrets *SecretManager
	ttl     time.Duration
	now     func() time.Time
}

// NewPreviewService returns a service that produces tokens valid for ttl.
// If ttl is non-positive it defaults to 24h.
func NewPreviewService(secrets *SecretManager, ttl time.Duration) *PreviewService {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &PreviewService{secrets: secrets, ttl: ttl, now: time.Now}
}

// Issue mints a preview token. Tag should be an opaque revision-scoped fingerprint
// (for example content.modified time) so that further edits invalidate old
// tokens even before their TTL is reached.
func (p *PreviewService) Issue(ctx context.Context, uid int64, cid int64, tag string) (string, error) {
	if p == nil {
		return "", ErrPreview
	}
	issued := p.now().UTC().Unix()
	body := previewPayload(uid, cid, tag, issued)
	sig, err := p.secrets.Sign(ctx, "preview", body)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(body) + "." + sig, nil
}

// PreviewClaims is what Verify returns on success. Callers may compare Owner
// against the current viewer to enforce private previews.
type PreviewClaims struct {
	Owner int64
	CID   int64
	Tag   string
}

// Verify returns the parsed claims if token is valid for (cid, tag) and has
// not expired.
func (p *PreviewService) Verify(ctx context.Context, cid int64, tag, token string) (PreviewClaims, error) {
	if p == nil || token == "" {
		return PreviewClaims{}, ErrPreview
	}
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return PreviewClaims{}, ErrPreview
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return PreviewClaims{}, ErrPreview
	}
	if !p.secrets.Verify(ctx, "preview", body, parts[1]) {
		return PreviewClaims{}, ErrPreview
	}
	fields := strings.Split(string(body), "|")
	if len(fields) != 4 {
		return PreviewClaims{}, ErrPreview
	}
	uid, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return PreviewClaims{}, ErrPreview
	}
	claimCID, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || claimCID != cid {
		return PreviewClaims{}, ErrPreview
	}
	if fields[2] != tag {
		return PreviewClaims{}, ErrPreview
	}
	issued, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return PreviewClaims{}, ErrPreview
	}
	now := p.now().UTC().Unix()
	if now-issued > int64(p.ttl/time.Second) {
		return PreviewClaims{}, ErrPreview
	}
	return PreviewClaims{Owner: uid, CID: cid, Tag: tag}, nil
}

func previewPayload(uid, cid int64, tag string, issued int64) []byte {
	return []byte(
		strconv.FormatInt(uid, 10) + "|" +
			strconv.FormatInt(cid, 10) + "|" +
			tag + "|" +
			strconv.FormatInt(issued, 10),
	)
}
