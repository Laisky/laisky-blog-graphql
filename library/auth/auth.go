package auth

import (
	ginMw "github.com/Laisky/gin-middlewares"
)

var Instance *ginMw.Auth

func Initialize(secret []byte) (err error) {
	Instance, err = ginMw.NewAuth(secret)
	return err
}
