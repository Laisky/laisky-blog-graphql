package jwt

import (
	gjwt "github.com/Laisky/go-utils/v3/jwt"
	"github.com/pkg/errors"
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
