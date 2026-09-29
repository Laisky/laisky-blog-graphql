package service

// tweetIDFilter preserves string IDs, including leading zeros, while preventing
// caller input from selecting BSON keys or operators. Empty IDs stay explicit.
type tweetIDFilter struct {
	ID string `bson:"id_str"`
}
