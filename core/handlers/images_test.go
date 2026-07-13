package handlers

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goblog/pkg/imageproc"
)

func handlerTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 180, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImageUploadProcessingConvertsAttachmentToWebP(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	app.UploadDir = t.TempDir()
	ctx := context.Background()
	if err := app.Options.Set(ctx, "upload_image_processing", imageproc.UploadWebPQuality); err != nil {
		t.Fatal(err)
	}
	if err := app.Options.Set(ctx, "upload_webp_quality", "74"); err != nil {
		t.Fatal(err)
	}
	postID := createPublishedPost(t, app, adminID, "processed-image")
	_, meta := uploadMedia(t, app, secret, adminID, postID, "cover.png", handlerTestPNG(t, 80, 48))

	if meta.Name != "cover.webp" || meta.Type != "webp" || meta.MIME != "image/webp" {
		t.Fatalf("converted attachment metadata = %#v", meta)
	}
	if meta.Width != 80 || meta.Height != 48 || !strings.HasSuffix(meta.Path, "cover.webp") {
		t.Fatalf("converted attachment dimensions/path = %#v", meta)
	}
	data, err := os.ReadFile(filepath.Join(app.UploadDir, filepath.FromSlash(meta.Path)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("RIFF")) {
		t.Fatalf("converted attachment is not WebP")
	}
}

func TestAdminThumbnailUsesConfiguredFormatAndCachesResult(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	app.UploadDir = t.TempDir()
	ctx := context.Background()
	if err := app.Options.Set(ctx, "thumbnail_format", imageproc.ThumbnailWebP); err != nil {
		t.Fatal(err)
	}
	if err := app.Options.Set(ctx, "thumbnail_quality", "78"); err != nil {
		t.Fatal(err)
	}
	postID := createPublishedPost(t, app, adminID, "thumbnail-image")
	_, meta := uploadMedia(t, app, secret, adminID, postID, "large.png", handlerTestPNG(t, 640, 360))

	req := httptest.NewRequest(http.MethodGet, adminThumbnailURL(meta.URL), nil)
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("thumbnail status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/webp" {
		t.Fatalf("thumbnail content type = %q", got)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 320 || cfg.Height != 180 {
		t.Fatalf("thumbnail size = %dx%d, want 320x180", cfg.Width, cfg.Height)
	}
	sourcePath := filepath.Join(app.UploadDir, filepath.FromSlash(meta.Path))
	cachePath := thumbnailCachePath(sourcePath, imageproc.ThumbnailWebP, 78)
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("thumbnail cache missing: %v", err)
	}
	if err := app.Options.Set(ctx, "thumbnail_format", imageproc.ThumbnailDisabled); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, adminThumbnailURL(meta.URL), nil)
	setSession(t, req, secret, adminID)
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != meta.URL {
		t.Fatalf("disabled thumbnail response = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestImageProcessingFailureFallsBackToOriginal(t *testing.T) {
	app, _, _ := newSecurityTestApp(t)
	app.UploadDir = t.TempDir()
	ctx := context.Background()
	if err := app.Options.Set(ctx, "upload_image_processing", imageproc.UploadWebPQuality); err != nil {
		t.Fatal(err)
	}
	if err := app.Options.Set(ctx, "image_processing_memory_mb", "64"); err != nil {
		t.Fatal(err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 4000, 3000))
	var source bytes.Buffer
	if err := jpeg.Encode(&source, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	saved, err := app.saveUpload(ctx, bytes.NewReader(source.Bytes()), "over-budget.jpg", 0)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Warning != imageProcessingFallbackWarning || saved.Meta.Type != "jpg" || !strings.HasSuffix(saved.Meta.Path, "over-budget.jpg") {
		t.Fatalf("fallback upload = %#v", saved)
	}
	data, err := os.ReadFile(filepath.Join(app.UploadDir, filepath.FromSlash(saved.Meta.Path)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, source.Bytes()) {
		t.Fatal("fallback upload did not preserve the original bytes")
	}
}

func TestImageProcessingSettingsRejectInvalidQuality(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	form := url.Values{
		"_csrf":                   {adminToken(secret, adminID)},
		"upload_image_processing": {imageproc.UploadWebPQuality},
		"upload_webp_quality":     {"101"},
		"thumbnail_format":        {imageproc.ThumbnailJPEG},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/options/general", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "WebP 质量必须是 1 到 100 的整数") {
		t.Fatalf("invalid quality response = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUploadExtensionComparisonUsesStoredFormat(t *testing.T) {
	if !sameUploadExtension("jpeg", "jpg") || !sameUploadExtension("webp", "webp") {
		t.Fatal("equivalent stored extensions should match")
	}
	if sameUploadExtension("png", "webp") {
		t.Fatal("different stored extensions should not match")
	}
}

func TestMediaPageUsesDirectUploadAndCompactTable(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	app.UploadDir = t.TempDir()
	uploadMedia(t, app, secret, adminID, 0, "compact.png", tinyPNG(t))
	req := httptest.NewRequest(http.MethodGet, "/admin/medias?kind=all&author=all", nil)
	setSession(t, req, secret, adminID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("media page status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"media-upload-form", "data-auto-submit", "media-table", "media-copy-button", "上传附件", `name="kind" label="类型" value="all"`, `name="author" label="作者" value="all"`, `<mdui-menu-item value="all">全部</mdui-menu-item>`} {
		if !strings.Contains(body, want) {
			t.Fatalf("media page missing %q", want)
		}
	}
	if strings.Contains(body, "<th>地址</th>") || strings.Contains(body, "未选择文件") {
		t.Fatalf("media page still contains the old upload or address layout")
	}
}

func TestSettingsAssetCardsCopyRelativeURL(t *testing.T) {
	app, secret, adminID := newSecurityTestApp(t)
	app.UploadDir = t.TempDir()
	ctx := context.Background()
	adminAsset, err := app.saveAdminSettingUpload(ctx, bytes.NewReader(tinyPNG(t)), "admin-copy.png")
	if err != nil {
		t.Fatal(err)
	}
	themeAsset, err := app.saveThemeSettingUpload(ctx, bytes.NewReader(tinyPNG(t)), "theme-copy.png")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		path string
		url  string
	}{
		{path: "/admin/management", url: adminAsset.URL},
		{path: "/admin/themes/default/config", url: themeAsset.URL},
	} {
		req := httptest.NewRequest(http.MethodGet, check.path, nil)
		setSession(t, req, secret, adminID)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", check.path, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "copy-notice-button") || !strings.Contains(body, `data-copy="`+check.url+`"`) || !strings.Contains(body, "复制相对 URL") {
			t.Fatalf("GET %s missing relative URL copy action for %s", check.path, check.url)
		}
	}
}
