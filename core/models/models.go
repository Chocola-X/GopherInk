package models

import "time"

const (
	ContentTypePost    = "post"
	ContentTypePage    = "page"
	ContentStatusPost  = "publish"
	ContentStatusDraft = "draft"
)

type Content struct {
	CID          int64
	Title        string
	Slug         string
	Created      int64
	Modified     int64
	Text         string
	SortOrder    int64
	AuthorID     int64
	Template     string
	Type         string
	Status       string
	Password     string
	CommentsNum  int64
	AllowComment string
	AllowPing    string
	AllowFeed    string
	Parent       int64
}

func (c Content) CreatedAt() time.Time {
	return time.Unix(c.Created, 0)
}

func (c Content) ModifiedAt() time.Time {
	return time.Unix(c.Modified, 0)
}

type User struct {
	UID        int64
	Name       string
	Password   string
	Mail       string
	URL        string
	ScreenName string
	Created    int64
	Activated  int64
	Logged     int64
	Role       string
	AuthCode   string
}

type Option struct {
	Name  string
	User  int64
	Value string
}
