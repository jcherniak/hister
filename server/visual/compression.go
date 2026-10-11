package visual

import (
	"errors"
	"sync"

	"github.com/klauspost/compress/zstd"
)

const MaxStoredTextBytes = 256 << 20

var textEncoder = sync.OnceValues(func() (*zstd.Encoder, error) {
	return zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(9)))
})

var textDecoder = sync.OnceValues(func() (*zstd.Decoder, error) {
	return zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(MaxStoredTextBytes), zstd.WithDecoderMaxWindow(64<<20), zstd.WithDecodeAllCapLimit(true))
})

func CompressText(data []byte) ([]byte, error) {
	encoder, err := textEncoder()
	if err != nil {
		return nil, err
	}
	return encoder.EncodeAll(data, nil), nil
}

func DecompressText(data []byte, limit int) ([]byte, error) {
	if limit < 0 || limit > MaxStoredTextBytes {
		return nil, errors.New("invalid stored text size limit")
	}
	var header zstd.Header
	if err := header.Decode(data); err != nil {
		return nil, err
	}
	capacity := limit
	if header.HasFCS {
		if header.FrameContentSize > uint64(limit) {
			return nil, errors.New("stored text exceeds size limit")
		}
		capacity = int(header.FrameContentSize)
	}
	decoder, err := textDecoder()
	if err != nil {
		return nil, err
	}
	decoded, err := decoder.DecodeAll(data, make([]byte, 0, capacity))
	if err != nil {
		return nil, err
	}
	if len(decoded) > limit {
		return nil, errors.New("stored text exceeds size limit")
	}
	return decoded, nil
}
