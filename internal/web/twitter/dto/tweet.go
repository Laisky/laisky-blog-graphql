// Package dto provides data transfer object.
package dto

type LoadTweetArgs struct {
	Page, Size int
	TweetID,
	Topic,
	Regexp,
	Username,
	ViewerID string
	SortBy, SortOrder string
}
