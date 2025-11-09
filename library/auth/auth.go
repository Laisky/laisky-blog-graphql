// package Auth provides JWT
package auth

import (
	"net/http"

	ginMw "github.com/Laisky/gin-middlewares/v7"
	"github.com/gin-gonic/gin"

	"github.com/Laisky/laisky-blog-graphql/library/jwt"
)

// Instance global auth instance
var Instance *ginMw.Auth

const (
	// CtxKeyAuthUser key for auth user in gin.Context
	CtxKeyAuthUser string = "authenticated_user"
)

// Initialize initialize auth
func Initialize(secret []byte) (err error) {
	Instance, err = ginMw.NewAuth(secret)
	return err
}

// AuthMw gin middleware for auth
func AuthMw(ctx *gin.Context) {
	user := new(jwt.UserClaims)
	if err := Instance.GetUserClaims(ctx, user); err != nil {
		ctx.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	ctx.Set(CtxKeyAuthUser, user)
	ctx.Next()
}

// GetUserFromCtx get user from gin.Context
func GetUserFromCtx(ctx *gin.Context) *jwt.UserClaims {
	if user, ok := ctx.Get(CtxKeyAuthUser); ok {
		return user.(*jwt.UserClaims)
	}

	return nil
}
