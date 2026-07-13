package defaulttheme

import "testing"

func TestGravatarURLUsesNormalizedEmailHash(t *testing.T) {
	got := gravatarURL(" Chocola@Nekopara.UK ", 128)
	want := "https://www.gravatar.com/avatar/ab19c18dc265207b197f1080b493241a?d=identicon&s=128"
	if got != want {
		t.Fatalf("gravatarURL() = %q, want %q", got, want)
	}
}

func TestAvatarURLPrefersConfiguredImage(t *testing.T) {
	values := map[string]string{"profile_avatar": "/uploads/avatar.png", "profile_email": "chocola@nekopara.uk"}
	if got := avatarURL(values, "", 128); got != "/uploads/avatar.png" {
		t.Fatalf("avatarURL() = %q, want configured URL", got)
	}
}

func TestAssetURLNormalizesUploadRelativePath(t *testing.T) {
	tests := map[string]string{
		"uploads/posts/1/cover.png":        "/uploads/posts/1/cover.png",
		"/uploads/posts/1/cover.png":       "/uploads/posts/1/cover.png",
		"https://example.com/cover.png":    "https://example.com/cover.png",
		"//cdn.example.com/cover.png":      "//cdn.example.com/cover.png",
		"../assets/cover.png":              "../assets/cover.png",
		"data:image/gif;base64,R0lGODlhAQ": "data:image/gif;base64,R0lGODlhAQ",
	}
	for input, want := range tests {
		if got := assetURL(input); got != want {
			t.Fatalf("assetURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestReadingTimeUsesAtLeastOneMinute(t *testing.T) {
	if got := readingTime("<p>短文</p>"); got != "1 分钟" {
		t.Fatalf("readingTime() = %q, want one minute", got)
	}
}

func TestStripHTMLLikeRemovesTags(t *testing.T) {
	if got := stripHTMLLike("<h1>标题</h1><p>正文</p>"); got != "标题正文" {
		t.Fatalf("stripHTMLLike() = %q", got)
	}
}
