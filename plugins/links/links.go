package links

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
)

const (
	friendLinksKey = "friend_links"
	maxFriendLinks = 200
)

type FriendLink struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Email       string `json:"email"`
	IconURL     string `json:"icon_url,omitempty"`
	AvatarURL   string `json:"-"`
}

type FriendLinkView struct {
	Name        string
	Description string
	URL         string
	AvatarURL   string
}

func DecodeFriendLinks(raw string) ([]FriendLink, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var links []FriendLink
	if err := json.Unmarshal([]byte(raw), &links); err != nil {
		return nil, err
	}
	if len(links) > maxFriendLinks {
		return nil, fmt.Errorf("friend links exceed %d entries", maxFriendLinks)
	}
	return links, nil
}

func friendLinksFromForm(form map[string][]string) ([]FriendLink, error) {
	names := form["friend_name"]
	descriptions := form["friend_description"]
	urls := form["friend_url"]
	emails := form["friend_email"]
	iconURLs := form["friend_icon_url"]
	if len(names) != len(descriptions) || len(names) != len(urls) || len(names) != len(emails) || len(names) != len(iconURLs) {
		return nil, fmt.Errorf("friend link form data is incomplete; refresh the page and try again")
	}
	if len(names) > maxFriendLinks {
		return nil, fmt.Errorf("friend links cannot exceed %d entries", maxFriendLinks)
	}
	links := make([]FriendLink, 0, len(names))
	for i := range names {
		link := FriendLink{
			Name:        strings.TrimSpace(names[i]),
			Description: strings.TrimSpace(descriptions[i]),
			URL:         strings.TrimSpace(urls[i]),
			Email:       strings.ToLower(strings.TrimSpace(emails[i])),
			IconURL:     strings.TrimSpace(iconURLs[i]),
		}
		position := i + 1
		switch {
		case link.Name == "":
			return nil, fmt.Errorf("friend link %d is missing a name", position)
		case len([]rune(link.Name)) > 100:
			return nil, fmt.Errorf("friend link %d name cannot exceed 100 characters", position)
		case link.Description == "":
			return nil, fmt.Errorf("friend link %d is missing a description", position)
		case len([]rune(link.Description)) > 250:
			return nil, fmt.Errorf("friend link %d description cannot exceed 250 characters", position)
		case !validFriendURL(link.URL):
			return nil, fmt.Errorf("friend link %d URL must be a valid HTTP or HTTPS URL", position)
		case link.Email == "" && link.IconURL == "":
			return nil, fmt.Errorf("friend link %d must include either an email or icon URL", position)
		case link.Email != "" && !validFriendEmail(link.Email):
			return nil, fmt.Errorf("friend link %d email is invalid", position)
		case link.IconURL != "" && !validFriendIconURL(link.IconURL):
			return nil, fmt.Errorf("friend link %d icon URL is invalid", position)
		}
		links = append(links, link)
	}
	return links, nil
}

func validFriendURL(value string) bool {
	if len(value) > 2048 {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func validFriendEmail(value string) bool {
	if value == "" || len(value) > 254 {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil && strings.EqualFold(address.Address, value)
}

func validFriendIconURL(value string) bool {
	if len(value) > 2048 {
		return false
	}
	value = strings.ReplaceAll(strings.TrimSpace(value), "{random}", "1")
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		parsed, err := url.ParseRequestURI(value)
		return err == nil && parsed.Path != ""
	}
	return validFriendURL(value)
}

func firstFormValue(form map[string][]string, name string) string {
	values := form[name]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
