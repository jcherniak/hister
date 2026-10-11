package compression

import (
	"bytes"
	"errors"
	"testing"
)

func sample(size int) []byte {
	data := make([]byte, size)
	for index := range data {
		data[index] = byte('a' + index%17)
	}
	return data
}

func TestRoundTrip(t *testing.T) {
	for _, algorithm := range Algorithms {
		for _, data := range [][]byte{[]byte("<p>hello</p>"), sample(1 << 20)} {
			encoded, err := Compress(algorithm, data)
			if err != nil {
				t.Fatalf("%s compress: %v", algorithm, err)
			}
			if bytes.Equal(encoded, data) {
				t.Fatalf("%s did not encode the data", algorithm)
			}
			decoded, err := Decompress(algorithm, encoded, MaxTextBytes)
			if err != nil {
				t.Fatalf("%s decompress: %v", algorithm, err)
			}
			if !bytes.Equal(decoded, data) {
				t.Fatalf("%s round trip changed the data", algorithm)
			}
		}
	}
}

func TestMalformedAndTruncatedInput(t *testing.T) {
	data := sample(64 << 10)
	for _, algorithm := range Algorithms {
		encoded, err := Compress(algorithm, data)
		if err != nil {
			t.Fatal(err)
		}
		for name, input := range map[string][]byte{
			"garbage":   []byte("not compressed at all"),
			"empty":     {},
			"truncated": encoded[:len(encoded)/2],
		} {
			if _, err := Decompress(algorithm, input, MaxTextBytes); err == nil {
				t.Fatalf("%s accepted %s input", algorithm, name)
			}
		}
	}
	gz, _ := Compress(Gzip, data)
	if _, err := Decompress(Zstd, gz, MaxTextBytes); err == nil {
		t.Fatal("zstd accepted gzip data")
	}
	zst, _ := Compress(Zstd, data)
	if _, err := Decompress(Gzip, zst, MaxTextBytes); err == nil {
		t.Fatal("gzip accepted zstd data")
	}
}

func TestSizeLimits(t *testing.T) {
	data := sample(1000)
	for _, algorithm := range Algorithms {
		encoded, err := Compress(algorithm, data)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Decompress(algorithm, encoded, len(data)); err != nil {
			t.Fatalf("%s rejected data at the limit: %v", algorithm, err)
		}
		if _, err := Decompress(algorithm, encoded, len(data)-1); !errors.Is(err, ErrSizeLimit) {
			t.Fatalf("%s limit error = %v, want ErrSizeLimit", algorithm, err)
		}
		if _, err := Decompress(algorithm, encoded, MaxTextBytes+1); err == nil {
			t.Fatalf("%s accepted a limit above MaxTextBytes", algorithm)
		}
	}
}

func TestUnsupportedAlgorithmAndNames(t *testing.T) {
	if _, err := Compress("lz4", []byte("x")); err == nil {
		t.Fatal("compress accepted an unsupported algorithm")
	}
	if _, err := Decompress("lz4", []byte("x"), MaxTextBytes); err == nil {
		t.Fatal("decompress accepted an unsupported algorithm")
	}
	if !Valid(Gzip) || !Valid(Zstd) || Valid("") || Valid("lz4") {
		t.Fatal("unexpected algorithm validity")
	}
	for _, algorithm := range Algorithms {
		if FromExtension(Extension(algorithm)) != algorithm {
			t.Fatalf("extension round trip failed for %s", algorithm)
		}
	}
	if Extension("lz4") != "" || FromExtension(".lz4") != "" || FromExtension("") != "" {
		t.Fatal("unknown names must map to empty strings")
	}
}
