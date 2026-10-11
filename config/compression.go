package config

import "fmt"

const DefaultCompressionAlgorithm = "gzip"

type Compression struct {
	Algorithm          string `yaml:"algorithm" mapstructure:"algorithm"`
	RecompressExisting bool   `yaml:"recompress_existing" mapstructure:"recompress_existing"`
}

func (c Compression) Validate() error {
	switch c.Algorithm {
	case "", "gzip", "zstd":
		return nil
	}
	return fmt.Errorf("compression.algorithm must be gzip or zstd, got %q", c.Algorithm)
}

func (c Compression) HTMLAlgorithm() string {
	if c.Algorithm == "" {
		return DefaultCompressionAlgorithm
	}
	return c.Algorithm
}
