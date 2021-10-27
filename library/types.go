package library

import (
	"fmt"
	"io"
	"net/url"
	"time"

	"laisky-blog-graphql/library/log"

	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

type Datetime struct {
	t time.Time
}

const TimeLayout = "2006-01-02T15:04:05.000Z"

func NewDatetimeFromTime(t time.Time) *Datetime {
	return &Datetime{
		t: t,
	}
}

func (d *Datetime) GetTime() time.Time {
	return d.t
}

func (d *Datetime) UnmarshalGQL(vi interface{}) (err error) {
	v, ok := vi.(string)
	if !ok {
		return fmt.Errorf("unknown type of Datetime: `%+v`", vi)
	}
	if d.t, err = time.Parse(TimeLayout, v); err != nil {
		return err
	}

	return nil
}

func (d Datetime) MarshalGQL(w io.Writer) {
	if _, err := w.Write(appendQuote([]byte(d.t.Format(TimeLayout)))); err != nil {
		log.Logger.Error("write datetime bytes", zap.Error(err))
	}
}

type QuotedString string

func (qs *QuotedString) UnmarshalGQL(vi interface{}) (err error) {
	switch v := vi.(type) {
	case string:
		if v, err = url.QueryUnescape(v); err != nil {
			log.Logger.Debug("unquote string", zap.String("quoted", v), zap.Error(err))
			return err
		}
		*qs = QuotedString(v)
		return nil
	}

	log.Logger.Debug("unknown type of QuotedString", zap.String("quoted", fmt.Sprint(vi)))
	return fmt.Errorf("unknown type of QuotedString: `%+v`", vi)
}

func (qs QuotedString) MarshalGQL(w io.Writer) {
	if _, err := w.Write(appendQuote([]byte(qs))); err != nil {
		log.Logger.Error("write bytes", zap.Error(err))
	}
}

type JSONString string

func (qs *JSONString) UnmarshalGQL(vi interface{}) (err error) {
	v, ok := vi.(string)
	if !ok {
		log.Logger.Debug("unknown type of JSONString", zap.String("val", fmt.Sprint(vi)))
	}
	// var v string
	if err = gutils.JSON.UnmarshalFromString(v, &v); err != nil {
		log.Logger.Debug("decode string", zap.String("quoted", v), zap.Error(err))
		return err
	}

	*qs = JSONString(v)
	return nil
}

func (qs JSONString) MarshalGQL(w io.Writer) {
	if vb, err := gutils.JSON.Marshal(qs); err != nil {
		log.Logger.Error("marshal json", zap.Error(err))
	} else {
		if _, err = w.Write(vb); err != nil {
			log.Logger.Error("write bytes", zap.Error(err))
		}
	}
}
