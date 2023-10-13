// package Auth provides JWT
package auth

import (
	ginMw "github.com/Laisky/gin-middlewares/v5"
)

var Instance *ginMw.Auth

func Initialize(secret []byte) (err error) {
	Instance, err = ginMw.NewAuth(secret)
	return err
}
