package library

import (
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/Laisky/errors/v2"
	"github.com/Laisky/go-utils/v6/json"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// Datetime datetime
type Datetime struct {
	t time.Time
}

// TimeLayout time layout
const TimeLayout = "2006-01-02T15:04:05.000Z"

// NewDatetimeFromTime new datetime from time
func NewDatetimeFromTime(t time.Time) *Datetime {
	return &Datetime{
		t: t,
	}
}

// GetTime get time
func (d *Datetime) GetTime() time.Time {
	return d.t
}

// UnmarshalGQL implements the graphql.Unmarshaler interface
func (d *Datetime) UnmarshalGQL(vi interface{}) (err error) {
	v, ok := vi.(string)
	if !ok {
		return errors.Errorf("unknown type of Datetime: `%+v`", vi)
	}
	if d.t, err = time.Parse(TimeLayout, v); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// MarshalGQL implements the graphql.Marshaler interface
func (d Datetime) MarshalGQL(w io.Writer) {
	if _, err := w.Write(appendQuote([]byte(d.t.Format(TimeLayout)))); err != nil {
		log.Logger.Error("write datetime bytes", zap.Error(err))
	}
}

// QuotedString is a string which will be unquoted when marshal to json
type QuotedString string

// UnmarshalGQL implements the graphql.Unmarshaler interface
func (qs *QuotedString) UnmarshalGQL(vi interface{}) (err error) {
	switch v := vi.(type) {
	case string:
		if v, err = url.QueryUnescape(v); err != nil {
			log.Logger.Debug("unquote string", zap.String("quoted", v), zap.Error(err))
			return errors.WithStack(err)
		}
		*qs = QuotedString(v)
		return nil
	}

	log.Logger.Debug("unknown type of QuotedString", zap.String("quoted", fmt.Sprint(vi)))
	return errors.Errorf("unknown type of QuotedString: `%+v`", vi)
}

func (qs QuotedString) MarshalGQL(w io.Writer) {
	if _, err := w.Write(appendQuote([]byte(qs))); err != nil {
		log.Logger.Error("write bytes", zap.Error(err))
	}
}

// JSONString is a string which will be unquoted when marshal to json
type JSONString string

// UnmarshalGQL implements the graphql.Unmarshaler interface
func (qs *JSONString) UnmarshalGQL(vi interface{}) (err error) {
	v, ok := vi.(string)
	if !ok {
		log.Logger.Debug("unknown type of JSONString", zap.String("val", fmt.Sprint(vi)))
	}

	// var v string
	if err = json.UnmarshalFromString(v, &v); err != nil {
		log.Logger.Debug("decode string", zap.String("quoted", v), zap.Error(err))
		return errors.WithStack(err)
	}

	*qs = JSONString(v)
	return nil
}

func (qs JSONString) MarshalGQL(w io.Writer) {
	if vb, err := json.Marshal(qs); err != nil {
		log.Logger.Error("marshal json", zap.Error(err))
	} else {
		if _, err = w.Write(vb); err != nil {
			log.Logger.Error("write bytes", zap.Error(err))
		}
	}
}
