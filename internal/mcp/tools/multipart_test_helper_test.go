package tools

import (
	"io"
	"mime/multipart"
	"net/textproto"
)

// multipartBuilder is a thin test-only wrapper around mime/multipart that
// exposes a prebuilt "images" file part so individual test files do not have
// to duplicate the textproto boilerplate.
type multipartBuilder struct {
	writer *multipart.Writer
}

func newMultipartWriter(w io.Writer) *multipartBuilder {
	return &multipartBuilder{writer: multipart.NewWriter(w)}
}

func (m *multipartBuilder) WriteField(name, value string) error {
	return m.writer.WriteField(name, value)
}

func (m *multipartBuilder) CreateImagePart(filename, mime string) (io.Writer, error) {
	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", `form-data; name="images"; filename="`+filename+`"`)
	hdr.Set("Content-Type", mime)
	return m.writer.CreatePart(hdr)
}

func (m *multipartBuilder) Close() error {
	return m.writer.Close()
}

func (m *multipartBuilder) ContentType() string {
	return m.writer.FormDataContentType()
}
