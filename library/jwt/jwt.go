package jwt

import (
	"github.com/Laisky/errors"
	gjwt "github.com/Laisky/go-utils/v4/jwt"
)

var Instance gjwt.JWT

func Initialize(secret []byte) (err error) {
	if Instance, err = gjwt.New(
		gjwt.WithSecretByte(secret),
		gjwt.WithSignMethod(gjwt.SignMethodHS256),
	); err != nil {
		return errors.Wrap(err, "new jwt")
	}

	return nil
}
