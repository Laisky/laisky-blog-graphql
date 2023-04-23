// Code generated by github.com/99designs/gqlgen, DO NOT EDIT.

package global

import (
	"fmt"
	"io"
	"strconv"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

type BlogLoginResponse struct {
	User  *model.User `json:"user"`
	Token string      `json:"token"`
}

type GeneralUser struct {
	Name         string   `json:"name"`
	LockPrefixes []string `json:"lock_prefixes"`
}

type NewBlogPost struct {
	Name     string        `json:"name"`
	Title    *string       `json:"title,omitempty"`
	Markdown *string       `json:"markdown,omitempty"`
	Type     *BlogPostType `json:"type,omitempty"`
	Category *string       `json:"category,omitempty"`
}

type Pagination struct {
	Page int `json:"page"`
	Size int `json:"size"`
}

type Sort struct {
	SortBy string    `json:"sort_by"`
	Order  SortOrder `json:"order"`
}

type BlogPostType string

const (
	BlogPostTypeMarkdown BlogPostType = "markdown"
	BlogPostTypeSlide    BlogPostType = "slide"
	BlogPostTypeHTML     BlogPostType = "html"
)

var AllBlogPostType = []BlogPostType{
	BlogPostTypeMarkdown,
	BlogPostTypeSlide,
	BlogPostTypeHTML,
}

func (e BlogPostType) IsValid() bool {
	switch e {
	case BlogPostTypeMarkdown, BlogPostTypeSlide, BlogPostTypeHTML:
		return true
	}
	return false
}

func (e BlogPostType) String() string {
	return string(e)
}

func (e *BlogPostType) UnmarshalGQL(v interface{}) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}

	*e = BlogPostType(str)
	if !e.IsValid() {
		return fmt.Errorf("%s is not a valid BlogPostType", str)
	}
	return nil
}

func (e BlogPostType) MarshalGQL(w io.Writer) {
	fmt.Fprint(w, strconv.Quote(e.String()))
}

type SortOrder string

const (
	SortOrderAsc  SortOrder = "ASC"
	SortOrderDesc SortOrder = "DESC"
)

var AllSortOrder = []SortOrder{
	SortOrderAsc,
	SortOrderDesc,
}

func (e SortOrder) IsValid() bool {
	switch e {
	case SortOrderAsc, SortOrderDesc:
		return true
	}
	return false
}

func (e SortOrder) String() string {
	return string(e)
}

func (e *SortOrder) UnmarshalGQL(v interface{}) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}

	*e = SortOrder(str)
	if !e.IsValid() {
		return fmt.Errorf("%s is not a valid SortOrder", str)
	}
	return nil
}

func (e SortOrder) MarshalGQL(w io.Writer) {
	fmt.Fprint(w, strconv.Quote(e.String()))
}
