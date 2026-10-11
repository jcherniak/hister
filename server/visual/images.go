package visual

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"math"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

type (
	codecKey struct{}
	codecs   struct{ encoder, decoder string }
)

var ErrUnsafeImage = errors.New("unsafe visual image")

func WithCodecs(ctx context.Context, encoder, decoder string) context.Context {
	if encoder == "" {
		encoder = "avifenc"
	}
	if decoder == "" {
		decoder = "avifdec"
	}
	return context.WithValue(ctx, codecKey{}, codecs{encoder, decoder})
}

func codecPaths(ctx context.Context) codecs {
	if value, ok := ctx.Value(codecKey{}).(codecs); ok {
		return value
	}
	return codecs{"avifenc", "avifdec"}
}

var (
	codecSlots = make(chan struct{}, 2)
	imageSlots = make(chan struct{}, 2)
)

func imageSlot(ctx context.Context) (func(), error) {
	select {
	case imageSlots <- struct{}{}:
		return sync.OnceFunc(func() { <-imageSlots }), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func runCodec(ctx context.Context, executable string, args ...string) error {
	select {
	case codecSlots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-codecSlots }()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, executable, args...).Run()
}

func validDimensions(width, height int) bool {
	return width > 0 && height > 0 && width <= 16384 && height <= 16384 && int64(width)*int64(height) <= 24_000_000
}

func raster(data []byte) (image.Image, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if !validDimensions(config.Width, config.Height) {
		return nil, fmt.Errorf("image exceeds pixel limit")
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	return decoded, err
}

func resized(source image.Image, width, height int) image.Image {
	output := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.ApproxBiLinear.Scale(output, output.Bounds(), source, source.Bounds(), draw.Src, nil)
	return output
}

func screenshot(source image.Image) bool {
	width, height := source.Bounds().Dx(), source.Bounds().Dy()
	if width < 256 || height < 128 {
		return false
	}
	thumb := resized(source, min(width, 256), min(height, 256))
	flat, edges, count := 0, 0, 0
	colors := map[uint32]struct{}{}
	for row := 0; row < thumb.Bounds().Dy(); row++ {
		for col := 1; col < thumb.Bounds().Dx(); col++ {
			red, green, blue, _ := thumb.At(col, row).RGBA()
			prevRed, prevGreen, prevBlue, _ := thumb.At(col-1, row).RGBA()
			difference := math.Abs(float64(red)-float64(prevRed)) + math.Abs(float64(green)-float64(prevGreen)) + math.Abs(float64(blue)-float64(prevBlue))
			if difference < 3*512 {
				flat++
			}
			if difference > 3*8192 {
				edges++
			}
			colors[(red>>12)<<8|(green>>12)<<4|blue>>12] = struct{}{}
			count++
		}
	}
	return float64(flat)/float64(count) > .55 && (len(colors) < 500 || float64(edges)/float64(count) > .035)
}

func animatedWebP(data []byte) (int, int, bool) {
	if len(data) < 30 || string(data[:4]) != "RIFF" || string(data[8:16]) != "WEBPVP8X" || binary.LittleEndian.Uint32(data[16:20]) != 10 || data[20]&2 == 0 {
		return 0, 0, false
	}
	width := 1 + int(data[24]) + int(data[25])<<8 + int(data[26])<<16
	height := 1 + int(data[27]) + int(data[28])<<8 + int(data[29])<<16
	return width, height, true
}

func OptimizeImage(ctx context.Context, asset Asset) (Asset, int, int, error) {
	if err := ctx.Err(); err != nil {
		return asset, asset.Width, asset.Height, err
	}
	if asset.Kind != "image" {
		return asset, asset.Width, asset.Height, nil
	}
	if mediaType, _, err := mime.ParseMediaType(asset.MIME); err == nil {
		asset.MIME = mediaType
	}
	release, err := imageSlot(ctx)
	if err != nil {
		return asset, asset.Width, asset.Height, err
	}
	defer release()
	if asset.Width != 0 || asset.Height != 0 {
		if !validDimensions(asset.Width, asset.Height) {
			return asset, asset.Width, asset.Height, ErrUnsafeImage
		}
	}
	if asset.MIME == "image/svg+xml" {
		var root struct {
			XMLName xml.Name
			Width   string `xml:"width,attr"`
			Height  string `xml:"height,attr"`
		}
		if err := xml.Unmarshal(asset.Data, &root); err != nil || root.XMLName.Local != "svg" || root.XMLName.Space != "http://www.w3.org/2000/svg" {
			return asset, asset.Width, asset.Height, ErrUnsafeImage
		}
		for _, dimension := range []string{root.Width, root.Height} {
			if value, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(dimension), "px"), 64); err == nil && (math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > 16384) {
				return asset, asset.Width, asset.Height, ErrUnsafeImage
			}
		}
		width, widthErr := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(root.Width), "px"), 64)
		height, heightErr := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(root.Height), "px"), 64)
		if widthErr == nil && heightErr == nil && width*height > 24_000_000 {
			return asset, asset.Width, asset.Height, ErrUnsafeImage
		}
		return asset, asset.Width, asset.Height, nil
	}
	if asset.MIME == "image/avif" {
		decoded, err := decodeAVIF(ctx, asset.Data)
		if err != nil {
			if ctx.Err() != nil {
				return asset, asset.Width, asset.Height, ctx.Err()
			}
			return asset, asset.Width, asset.Height, fmt.Errorf("%w: %v", ErrUnsafeImage, err)
		}
		asset.Width, asset.Height = decoded.Bounds().Dx(), decoded.Bounds().Dy()
		return asset, asset.Width, asset.Height, nil
	}
	if width, height, animated := animatedWebP(asset.Data); animated {
		if !validDimensions(width, height) {
			return asset, width, height, ErrUnsafeImage
		}
		asset.Width, asset.Height, asset.MIME = width, height, "image/webp"
		return asset, width, height, nil
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(asset.Data))
	if err != nil || !validDimensions(config.Width, config.Height) {
		return asset, asset.Width, asset.Height, ErrUnsafeImage
	}
	asset.Width, asset.Height = config.Width, config.Height
	asset.MIME = "image/" + format
	if format == "gif" || bytes.Contains(asset.Data, []byte("acTL")) {
		return asset, asset.Width, asset.Height, nil
	}
	paths := codecPaths(ctx)
	if _, err = exec.LookPath(paths.encoder); err != nil {
		return asset, asset.Width, asset.Height, err
	}
	if _, err = exec.LookPath(paths.decoder); err != nil {
		return asset, asset.Width, asset.Height, err
	}
	source, err := raster(asset.Data)
	if err != nil {
		return asset, asset.Width, asset.Height, fmt.Errorf("%w: %v", ErrUnsafeImage, err)
	}
	asset.Width, asset.Height = source.Bounds().Dx(), source.Bounds().Dy()
	width, height, quality := asset.Width, asset.Height, 60
	if screenshot(source) {
		quality = 80
	} else if len(asset.Data) >= 100_000 {
		width = max(1, int(math.Round(float64(width)/math.Sqrt2)))
		height = max(1, int(math.Round(float64(height)/math.Sqrt2)))
		source = resized(source, width, height)
	}
	directory, err := os.MkdirTemp("", "hister-avif-*")
	if err != nil {
		return asset, asset.Width, asset.Height, err
	}
	defer func() { _ = os.RemoveAll(directory) }()
	input, output := filepath.Join(directory, "input.png"), filepath.Join(directory, "output.avif")
	file, err := os.Create(input)
	if err != nil {
		return asset, asset.Width, asset.Height, err
	}
	err = png.Encode(file, source)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return asset, asset.Width, asset.Height, err
	}
	err = runCodec(ctx, paths.encoder, "--jobs", "1", "--speed", "6", "--qcolor", strconv.Itoa(quality), "--qalpha", "100", "--yuv", "444", input, output)
	if err != nil {
		return asset, asset.Width, asset.Height, err
	}
	compressed, err := os.ReadFile(output)
	if err != nil {
		return asset, asset.Width, asset.Height, err
	}
	if len(compressed) >= len(asset.Data) {
		return asset, asset.Width, asset.Height, nil
	}
	asset.Data, asset.MIME = compressed, "image/avif"
	return asset, width, height, nil
}

func decodeAVIF(ctx context.Context, data []byte) (image.Image, error) {
	directory, err := os.MkdirTemp("", "hister-avif-view-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(directory) }()
	input, output := filepath.Join(directory, "input.avif"), filepath.Join(directory, "output.png")
	if err = os.WriteFile(input, data, 0o600); err != nil {
		return nil, err
	}
	if err = runCodec(ctx, codecPaths(ctx).decoder, "--jobs", "1", "--size-limit", "24000000", "--dimension-limit", "16384", input, output); err != nil {
		return nil, err
	}
	decoded, err := os.ReadFile(output)
	if err != nil {
		return nil, err
	}
	return raster(decoded)
}

func restoreImage(ctx context.Context, resource Resource, data []byte) ([]byte, string, error) {
	if resource.MIME == "image/svg+xml" && resource.Width == 0 && resource.Height == 0 {
		return data, resource.MIME, nil
	}
	if resource.Width == 0 && resource.Height == 0 {
		config, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, "", ErrUnsafeImage
		}
		resource.Width, resource.Height = config.Width, config.Height
	}
	if !validDimensions(resource.Width, resource.Height) {
		return nil, "", ErrUnsafeImage
	}
	if resource.StoredWidth == 0 || resource.StoredHeight == 0 || (resource.StoredWidth == resource.Width && resource.StoredHeight == resource.Height) {
		if resource.MIME != "image/avif" && resource.MIME != "image/svg+xml" {
			if width, height, animated := animatedWebP(data); animated {
				if !validDimensions(width, height) {
					return nil, "", ErrUnsafeImage
				}
			} else {
				config, _, err := image.DecodeConfig(bytes.NewReader(data))
				if err != nil || !validDimensions(config.Width, config.Height) {
					return nil, "", ErrUnsafeImage
				}
			}
		}
		return data, resource.MIME, nil
	}
	release, err := imageSlot(ctx)
	if err != nil {
		return nil, "", err
	}
	defer release()
	var source image.Image
	if strings.EqualFold(resource.MIME, "image/avif") {
		source, err = decodeAVIF(ctx, data)
	} else {
		source, err = raster(data)
	}
	if err != nil {
		return nil, "", err
	}
	output := limitedHTML{limit: 32 << 20, ctx: ctx}
	err = png.Encode(&output, resized(source, resource.Width, resource.Height))
	return output.Bytes(), "image/png", err
}
