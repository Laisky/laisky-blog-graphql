package jwt

import (
	"github.com/Laisky/go-utils"
	"github.com/pkg/errors"
)

var Instance *utils.JWT

func Initialize(secret []byte) (err error) {
	if Instance, err = utils.NewJWT(
		utils.WithJWTSecretByte(secret),
		utils.WithJWTSignMethod(utils.SignMethodHS256),
	); err != nil {
		return errors.Wrap(err, "new jwt")
	}

	return nil
}
