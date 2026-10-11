package visual

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os/exec"
	"strings"
	"testing"
)

func TestPublicImageAddresses(t *testing.T) {
	for _, value := range []string{"0.1.2.3", "192.88.99.1", "2001:db8::1", "3fff::1", "fec0::1"} {
		if publicIP(netip.MustParseAddr(value)) {
			t.Fatalf("reserved address accepted: %s", value)
		}
	}
	for _, value := range []string{"127.0.0.1", "::1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.100.100.200", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "2002:7f00:1::", "198.18.0.1", "192.0.2.1", "fc00::1"} {
		if publicIP(netip.MustParseAddr(value)) {
			t.Errorf("accepted %s", value)
		}
	}
	if !publicIP(netip.MustParseAddr("8.8.8.8")) || !publicIP(netip.MustParseAddr("2606:4700:4700::1111")) {
		t.Fatal("public addresses rejected")
	}
	if _, err := safeDial(context.Background(), "tcp", "127.0.0.1:1234"); err == nil {
		t.Fatal("private server dialed")
	}
}

func pngImage(width, height int) []byte {
	var data bytes.Buffer
	source := image.NewNRGBA(image.Rect(0, 0, width, height))
	for row := 0; row < height; row++ {
		for col := 0; col < width; col++ {
			source.Set(col, row, color.NRGBA{uint8(col), uint8(row), uint8(col + row), 255})
		}
	}
	_ = png.Encode(&data, source)
	return data.Bytes()
}

func TestLiveImageFetchValidatesAndFetchesOnce(t *testing.T) {
	requests := 0
	data := pngImage(16, 16)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write(data)
	}))
	defer server.Close()
	client := server.Client()
	result, media, err := fetchImage(context.Background(), client, server.URL)
	if err != nil || !bytes.Equal(data, result) || media != "image/png" || requests != 1 {
		t.Fatalf("fetch: %s %v %d", media, err, requests)
	}
	for _, value := range []string{"file:///tmp/image.png", "http://user:pass@example.com/a", "data:image/png,aaa"} {
		if validLiveURL(value) {
			t.Errorf("accepted %s", value)
		}
	}
}

func TestImageClientBlocksPrivateRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { t.Error("private server accessed") }))
	defer server.Close()
	client := newImageClient()
	defer client.CloseIdleConnections()
	if _, _, err := fetchImage(context.Background(), client, server.URL); err == nil {
		t.Fatal("private URL accepted")
	}
	if err := client.CheckRedirect(&http.Request{URL: mustURL("file:///tmp/a")}, nil); err == nil {
		t.Fatal("redirect accepted")
	}
}

func TestLiveSVGValidation(t *testing.T) {
	for _, test := range []struct {
		data     string
		readable bool
	}{
		{`<svg xmlns="http://www.w3.org/2000/svg" width="20" height="10"><rect width="20" height="10"/></svg>`, true},
		{`<html><body>not an image</body></html>`, false},
		{`<svg xmlns="http://www.w3.org/2000/svg"><rect></svg>`, false},
		{`<svg xmlns="http://www.w3.org/2000/svg"/><svg xmlns="http://www.w3.org/2000/svg"/>`, false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "image/svg+xml")
			_, _ = writer.Write([]byte(test.data))
		}))
		data, mime, err := fetchImage(context.Background(), server.Client(), server.URL)
		server.Close()
		if (err == nil) != test.readable || (test.readable && (mime != "image/svg+xml" || string(data) != test.data)) {
			t.Fatalf("readable=%v mime=%s err=%v", test.readable, mime, err)
		}
	}
}

func TestReplayRewritesResourcesAndRemovesActiveContent(t *testing.T) {
	markup := []byte(`<head><meta http-equiv="refresh" content="0;url=https://bad.example"><link rel="stylesheet" href="/style.css"></head><body onload="alert(1)"><script>alert(1)</script><iframe src="https://bad.example"></iframe><form action="https://bad.example"><input autofocus></form><img src="/image.png" srcset="https://bad.example/x 2x"><a href="javascript:alert(1)">link</a><div style="background:url('/missing.png')"></div></body>`)
	manifest := &Manifest{Resources: []Resource{{URL: "https://example.com/style.css", Kind: "css", MIME: "text/css", Key: "css"}, {URL: "https://example.com/image.png", Kind: "image", MIME: "image/png", Key: "image"}, {URL: "https://example.com/font.woff2", Kind: "font", MIME: "font/woff2", Key: "font"}}}
	data := map[string][]byte{"css": []byte(`@media(max-width:800px){body{color:red}} @font-face{font-family:test;src:url('/font.woff2')} div{background:url('/image.png')}`), "image": pngImage(16, 16), "font": []byte("font")}
	completed := 0
	result, err := Render(context.Background(), "https://example.com/page", manifest, markup, func(resource Resource) ([]byte, error) { return data[resource.Key], nil }, func(done, total, failed int) { completed = done })
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"<script", "<iframe", "<form", "onload", "srcset", "autofocus", "https://bad.example", "https://example.com", "javascript:"} {
		if strings.Contains(result, forbidden) {
			t.Errorf("contains %s", forbidden)
		}
	}
	for _, required := range []string{"@media(max-width:800px)", "data:image/png;base64,", "data:font/woff2;base64,", "Content-Security-Policy", "default-src"} {
		if !strings.Contains(result, required) {
			t.Errorf("missing %s", required)
		}
	}
	if completed != 3 {
		t.Errorf("completed %d", completed)
	}
}

func TestReplayPreservesSafeLinks(t *testing.T) {
	for _, test := range []struct {
		href string
		want string
	}{
		{"/next?x=1", "https://example.com/next?x=1"},
		{"../next", "https://example.com/next"},
		{"//other.example/next", "https://other.example/next"},
		{"https://other.example/next#section", "https://other.example/next#section"},
		{"#section", "#section"},
		{"javascript:alert(1)", ""},
		{"data:text/html,unsafe", ""},
		{"file:///tmp/page", ""},
		{"https://user:pass@example.com/", ""},
	} {
		t.Run(test.href, func(t *testing.T) {
			markup := []byte(`<a href="` + test.href + `" target="_top" rel="opener" onclick="alert(1)" ping="https://tracker.example">link</a>`)
			result, err := Render(context.Background(), "https://example.com/docs/page", &Manifest{}, markup, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"_top", "onclick", "ping=", `rel="opener"`} {
				if strings.Contains(result, forbidden) {
					t.Fatalf("active content remains: %s", result)
				}
			}
			if test.want == "" {
				if strings.Contains(result, "href=") || strings.Contains(result, "target=") {
					t.Fatal(result)
				}
			} else if !strings.Contains(result, `href="`+test.want+`"`) {
				t.Fatal(result)
			} else if strings.HasPrefix(test.want, "#") {
				if strings.Contains(result, "target=") {
					t.Fatal(result)
				}
			} else if !strings.Contains(result, `target="_blank" rel="noopener noreferrer"`) {
				t.Fatal(result)
			}
		})
	}
	result, err := Render(context.Background(), "https://example.com", &Manifest{}, []byte(`<svg><a href="https://other.example">link</a><use href="https://other.example/image.svg#icon"></use><use href="#icon"></use></svg>`), nil, nil)
	if err != nil || strings.Contains(result, "https://other.example") || !strings.Contains(result, `href="#icon"`) {
		t.Fatal(result, err)
	}
}

func TestRestoreDimensions(t *testing.T) {
	resource := Resource{MIME: "image/png", Width: 80, Height: 40, StoredWidth: 40, StoredHeight: 20}
	data, media, err := restoreImage(context.Background(), resource, pngImage(40, 20))
	if err != nil || media != "image/png" {
		t.Fatal(media, err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width != 80 || config.Height != 40 {
		t.Fatal(config, err)
	}
	resource.Width = 24_000_001
	if _, _, err := restoreImage(context.Background(), resource, data); err == nil {
		t.Fatal("unsafe dimensions accepted")
	}
}

func TestImageOptimizationAndMissingCodecFallback(t *testing.T) {
	asset := Asset{Kind: "image", MIME: "image/png", Data: pngImage(512, 256)}
	ctx := WithCodecs(context.Background(), "hister-nonexistent-avifenc", "hister-nonexistent-avifdec")
	retained, width, height, err := OptimizeImage(ctx, asset)
	if err == nil || !bytes.Equal(retained.Data, asset.Data) || width != 512 || height != 256 {
		t.Fatal("original not retained")
	}
	if _, err := exec.LookPath("avifenc"); err != nil {
		t.Skip("libavif is not installed")
	}
	if _, err := exec.LookPath("avifdec"); err != nil {
		t.Skip("libavif is not installed")
	}
	compressed, width, height, err := OptimizeImage(context.Background(), asset)
	if err != nil {
		t.Fatal(err)
	}
	if len(compressed.Data) > len(asset.Data) || width != 512 || height != 256 {
		t.Fatal("small image policy failed")
	}
	if compressed.MIME == "image/avif" {
		decoded, err := decodeAVIF(context.Background(), compressed.Data)
		if err != nil || decoded.Bounds().Dx() != width {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runCodec(ctx, "avifenc", "--version"); err == nil {
		t.Fatal("canceled conversion succeeded")
	}
}

func TestUnsafeImagesDoNotUseOriginalFallback(t *testing.T) {
	ctx := WithCodecs(context.Background(), "hister-nonexistent-avifenc", "hister-nonexistent-avifdec")
	oversized := append([]byte(nil), pngImage(1, 1)...)
	binary.BigEndian.PutUint32(oversized[16:20], 60000)
	binary.BigEndian.PutUint32(oversized[20:24], 60000)
	binary.BigEndian.PutUint32(oversized[29:33], crc32.ChecksumIEEE(oversized[12:29]))
	for _, asset := range []Asset{
		{Kind: "image", MIME: "image/png", Data: oversized},
		{Kind: "image", MIME: "image/png", Data: []byte("not an image")},
		{Kind: "image", MIME: "image/gif", Data: []byte("GIF89a\xff\xff\xff\xff\x00\x00\x00")},
		{Kind: "image", MIME: "image/svg+xml", Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), Width: 60000, Height: 60000},
		{Kind: "image", MIME: "image/svg+xml", Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="60000px" height="60000px"/>`)},
		{Kind: "image", MIME: "image/svg+xml", Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="6000" height="6000"/>`)},
		{Kind: "image", MIME: "image/svg+xml", Data: oversized},
		{Kind: "image", MIME: "image/avif", Data: []byte("not an AVIF")},
	} {
		if _, _, _, err := OptimizeImage(ctx, asset); !errors.Is(err, ErrUnsafeImage) {
			t.Fatalf("mime=%s err=%v", asset.MIME, err)
		}
	}
	if _, _, err := restoreImage(context.Background(), Resource{MIME: "image/png", Width: 60000, Height: 60000, StoredWidth: 60000, StoredHeight: 60000}, oversized); !errors.Is(err, ErrUnsafeImage) {
		t.Fatalf("unsafe full-size replay: %v", err)
	}
	if _, _, err := restoreImage(context.Background(), Resource{MIME: "image/png", Width: 1, Height: 1}, oversized); !errors.Is(err, ErrUnsafeImage) {
		t.Fatalf("unsafe source replay: %v", err)
	}
	_, _, _, err := OptimizeImage(ctx, Asset{Kind: "image", MIME: "image/png", Data: pngImage(16, 16)})
	if err == nil || errors.Is(err, ErrUnsafeImage) {
		t.Fatalf("missing codec classified as unsafe: %v", err)
	}
}

func TestPassthroughImageDimensions(t *testing.T) {
	gifData := []byte("GIF89a\x02\x00\x03\x00\x00\x00\x00")
	webpData := make([]byte, 30)
	copy(webpData, "RIFF")
	copy(webpData[8:], "WEBPVP8X")
	binary.LittleEndian.PutUint32(webpData[16:20], 10)
	webpData[20], webpData[24], webpData[27] = 2, 7, 3
	for _, test := range []struct {
		asset         Asset
		width, height int
	}{
		{Asset{Kind: "image", MIME: "image/gif", Data: gifData}, 2, 3},
		{Asset{Kind: "image", MIME: "image/png; charset=binary", Data: gifData}, 2, 3},
		{Asset{Kind: "image", MIME: "image/webp", Data: webpData}, 8, 4},
		{Asset{Kind: "image", MIME: "image/svg+xml", Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)}, 0, 0},
	} {
		asset, width, height, err := OptimizeImage(context.Background(), test.asset)
		if err != nil || width != test.width || height != test.height || !bytes.Equal(asset.Data, test.asset.Data) {
			t.Fatalf("size=%dx%d err=%v", width, height, err)
		}
	}
	webpData[24], webpData[25], webpData[26] = 255, 255, 255
	if _, _, _, err := OptimizeImage(context.Background(), Asset{Kind: "image", MIME: "image/webp", Data: webpData}); !errors.Is(err, ErrUnsafeImage) {
		t.Fatalf("oversize animation: %v", err)
	}
}

func TestLargeImagePolicyRestoresOriginalDimensions(t *testing.T) {
	for _, executable := range []string{"avifenc", "avifdec"} {
		if _, err := exec.LookPath(executable); err != nil {
			t.Skip("libavif is not installed")
		}
	}
	source := image.NewNRGBA(image.Rect(0, 0, 512, 256))
	random := rand.New(rand.NewPCG(1, 2))
	for offset := 0; offset < len(source.Pix); offset += 4 {
		source.Pix[offset] = uint8(random.Uint32())
		source.Pix[offset+1] = uint8(random.Uint32())
		source.Pix[offset+2] = uint8(random.Uint32())
		source.Pix[offset+3] = 255
	}
	var original bytes.Buffer
	if err := png.Encode(&original, source); err != nil {
		t.Fatal(err)
	}
	if original.Len() < 100_000 || screenshot(source) {
		t.Fatal("invalid large-image fixture")
	}
	compressed, width, height, err := OptimizeImage(context.Background(), Asset{Kind: "image", MIME: "image/png", Data: original.Bytes()})
	if err != nil || compressed.MIME != "image/avif" || width != int(math.Round(512/math.Sqrt2)) || height != int(math.Round(256/math.Sqrt2)) {
		t.Fatalf("size=%dx%d mime=%s err=%v", width, height, compressed.MIME, err)
	}
	data, mime, err := restoreImage(context.Background(), Resource{MIME: compressed.MIME, Width: compressed.Width, Height: compressed.Height, StoredWidth: width, StoredHeight: height}, compressed.Data)
	if err != nil || mime != "image/png" {
		t.Fatal(mime, err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width != 512 || config.Height != 256 {
		t.Fatalf("dimensions=%+v err=%v", config, err)
	}
}

func TestCSSImportsPreserveConditions(t *testing.T) {
	resources := map[string]replayResource{"https://example.com/css/nested.css": {resource: Resource{Kind: "css"}, data: []byte(`p{color:red}`)}}
	result := rewriteCSS(`@import "nested.css" screen and (min-width: 500px);`, "https://example.com/css/main.css", resources, 0)
	if result != "@media screen and (min-width: 500px){p{color:red}}" {
		t.Fatal(result)
	}
	result = rewriteCSS(`@import "nested.css" layer(theme) supports(display:grid) print;`, "https://example.com/css/main.css", resources, 0)
	if result != "@layer theme{@supports (display:grid){@media print{p{color:red}}}}" {
		t.Fatal(result)
	}
	if strings.Contains(cssText(`</style><a href="https://bad.example">`), "<") {
		t.Fatal("CSS can escape its style element")
	}
}

func TestNestedCSSResourcesAndSharedBudget(t *testing.T) {
	fontURI := dataURI("font/woff2", []byte("font"))
	imageURI := dataURI("image/png", pngImage(1, 1))
	resources := map[string]replayResource{
		"https://example.com/css/nested.css": {resource: Resource{Kind: "css"}, data: []byte(`@font-face{src:url('../font.woff2')}p{background:url('../image.png')}`)},
		"https://example.com/font.woff2":     {uri: fontURI},
		"https://example.com/image.png":      {uri: imageURI},
	}
	result := rewriteCSS(`@import url("nested.css") screen;`, "https://example.com/css/main.css", resources, 0)
	if !strings.Contains(result, fontURI) || !strings.Contains(result, imageURI) || !strings.HasPrefix(result, "@media screen{") {
		t.Fatal(result)
	}
	resources["https://example.com/css/cycle.css"] = replayResource{resource: Resource{Kind: "css"}, data: []byte(`@import "cycle.css";p{color:red}`)}
	budget := 100
	result = rewriteCSSBudget(`@import "cycle.css";@import "cycle.css";`, "https://example.com/css/main.css", resources, 0, &budget)
	if len(result) > 120 || budget < 0 || strings.Contains(result, "@import") {
		t.Fatalf("unbounded import expansion: %d bytes, %d budget", len(result), budget)
	}
	budget = 200
	resources["https://example.com/image.png"] = replayResource{uri: strings.Repeat("x", 1000)}
	result = rewriteCSSBudget(`p{background:url('../image.png')}`, "https://example.com/css/main.css", resources, 0, &budget)
	if result != `p{background:url("")}` {
		t.Fatal(result)
	}
}

func TestReplayDeduplicatesStoredResources(t *testing.T) {
	first := Resource{URL: "data:first", Kind: "image", MIME: "image/png", Key: "shared", Width: 16, Height: 16, StoredWidth: 8, StoredHeight: 8}
	second := first
	second.URL = "data:second"
	manifest := &Manifest{Resources: []Resource{first, second, first}}
	reads, completed, total := 0, 0, 0
	result, err := Render(context.Background(), "https://example.com", manifest, []byte(`<img src="data:first"><img src="data:second">`), func(resource Resource) ([]byte, error) {
		reads++
		return pngImage(8, 8), nil
	}, func(done, count, failed int) { completed, total = done, count })
	if err != nil || reads != 1 || completed != 2 || total != 2 || strings.Count(result, "data:image/png;base64,") != 2 {
		t.Fatalf("reads=%d progress=%d/%d err=%v", reads, completed, total, err)
	}
}

func TestReplayLimitsAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Render(ctx, "https://example.com", &Manifest{}, nil, nil, nil); err == nil {
		t.Fatal("canceled render succeeded")
	}
	if _, err := Render(context.Background(), "https://example.com", nil, nil, nil, nil); err == nil {
		t.Fatal("missing manifest accepted")
	}
	output := limitedHTML{limit: 10, ctx: context.Background()}
	if _, err := output.WriteString(strings.Repeat("x", 11)); err == nil || output.Len() != 0 {
		t.Fatal("output limit exceeded")
	}
	resource := Resource{URL: "data:oversize", Kind: "font", Key: "oversize"}
	result, err := Render(context.Background(), "https://example.com", &Manifest{Resources: []Resource{resource}}, nil, func(Resource) ([]byte, error) {
		return make([]byte, MaxResourceBytes+1), nil
	}, nil)
	if err != nil || strings.Contains(result, "base64") {
		t.Fatal("oversized resource was embedded")
	}
}

func mustURL(raw string) *url.URL {
	value, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return value
}
