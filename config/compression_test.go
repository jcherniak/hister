package config

import "testing"

func TestCompressionDefaultsAndValidation(t *testing.T) {
	cfg := CreateDefaultConfig()
	if cfg.Compression.Algorithm != "gzip" || cfg.Compression.HTMLAlgorithm() != "gzip" || cfg.Compression.RecompressExisting {
		t.Fatalf("unexpected default compression: %+v", cfg.Compression)
	}
	if (Compression{}).HTMLAlgorithm() != "gzip" {
		t.Fatal("empty algorithm must default to gzip")
	}
	parsed, err := parseConfig([]byte("compression:\n  algorithm: zstd\n  recompress_existing: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Compression.HTMLAlgorithm() != "zstd" || !parsed.Compression.RecompressExisting {
		t.Fatalf("unexpected parsed compression: %+v", parsed.Compression)
	}
	if _, err := parseConfig([]byte("compression:\n  algorithm: lz4\n")); err == nil {
		t.Fatal("unsupported compression algorithm accepted")
	}
	if err := (Compression{Algorithm: "zstd"}).Validate(); err != nil {
		t.Fatal(err)
	}
}
