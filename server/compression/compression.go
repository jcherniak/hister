package compression

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/klauspost/compress/zstd"
)

const (
	Gzip         = "gzip"
	Zstd         = "zstd"
	MaxTextBytes = 256 << 20
)

var Algorithms = []string{Gzip, Zstd}

var ErrSizeLimit = errors.New("decoded data exceeds size limit")

var zstdEncoder = sync.OnceValues(func() (*zstd.Encoder, error) {
	return zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(9)))
})

var zstdDecoder = sync.OnceValues(func() (*zstd.Decoder, error) {
	return zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(MaxTextBytes), zstd.WithDecoderMaxWindow(64<<20), zstd.WithDecodeAllCapLimit(true))
})

func Valid(algorithm string) bool {
	return algorithm == Gzip || algorithm == Zstd
}

func Extension(algorithm string) string {
	switch algorithm {
	case Gzip:
		return ".gz"
	case Zstd:
		return ".zst"
	}
	return ""
}

func FromExtension(ext string) string {
	switch ext {
	case ".gz":
		return Gzip
	case ".zst":
		return Zstd
	}
	return ""
}

func Compress(algorithm string, data []byte) ([]byte, error) {
	switch algorithm {
	case Gzip:
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case Zstd:
		encoder, err := zstdEncoder()
		if err != nil {
			return nil, err
		}
		return encoder.EncodeAll(data, nil), nil
	}
	return nil, fmt.Errorf("unsupported compression algorithm %q", algorithm)
}

func Decompress(algorithm string, data []byte, limit int) ([]byte, error) {
	if limit < 0 || limit > MaxTextBytes {
		return nil, errors.New("invalid decoded size limit")
	}
	switch algorithm {
	case Gzip:
		r, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer func() { _ = r.Close() }()
		decoded, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
		if err != nil {
			return nil, err
		}
		if len(decoded) > limit {
			return nil, ErrSizeLimit
		}
		if err := r.Close(); err != nil {
			return nil, err
		}
		return decoded, nil
	case Zstd:
		var header zstd.Header
		if err := header.Decode(data); err != nil {
			return nil, err
		}
		capacity := limit
		if header.HasFCS {
			if header.FrameContentSize > uint64(limit) {
				return nil, ErrSizeLimit
			}
			capacity = int(header.FrameContentSize)
		}
		decoder, err := zstdDecoder()
		if err != nil {
			return nil, err
		}
		decoded, err := decoder.DecodeAll(data, make([]byte, 0, capacity))
		if err != nil {
			return nil, err
		}
		if len(decoded) > limit {
			return nil, ErrSizeLimit
		}
		return decoded, nil
	}
	return nil, fmt.Errorf("unsupported compression algorithm %q", algorithm)
}
