package arweave

import (
	"bytes"
	"compress/gzip"
)

var DataPrefixEnabledGz = []byte("gz::")

// CompressData compress data if it's larger than 1KB
func CompressData(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}

	return append(DataPrefixEnabledGz, buf.Bytes()...), nil
}

// DecompressData decompress data if it's compressed
func DecompressData(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, DataPrefixEnabledGz) {
		return data, nil
	}

	r, err := gzip.NewReader(bytes.NewReader(data[len(DataPrefixEnabledGz):]))
	if err != nil {
		return nil, err
	}
	defer r.Close()

	var buf bytes.Buffer
	if _, err = buf.ReadFrom(r); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
