package models

type Meta struct {
	MID         int64
	Name        string
	Slug        string
	Type        string
	Description string
	Count       int64
	SortOrder   int64
	Parent      int64
}

type Comment struct {
	COID     int64
	CID      int64
	Created  int64
	Author   string
	AuthorID int64
	OwnerID  int64
	Mail     string
	URL      string
	IP       string
	Agent    string
	Text     string
	Type     string
	Status   string
	Parent   int64
	Title    string
	Slug     string
}

type Stats struct {
	Posts      int64
	Pages      int64
	Comments   int64
	Categories int64
	Tags       int64
	Users      int64
	Waiting    int64
}
