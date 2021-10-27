package model

type User struct {
	ID         string `bson:"id_str" json:"id"`
	ScreenName string `bson:"screen_name" json:"screen_name"`
	Name       string `bson:"name" json:"name"`
	Dscription string `bson:"dscription" json:"dscription"`
}
