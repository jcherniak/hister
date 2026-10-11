package visual

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"image"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

var blockedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("fec0::/10"),
}

func publicIP(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	for _, network := range blockedNetworks {
		if network.Contains(address) {
			return false
		}
	}
	return true
}

func safeDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("no addresses")
	}
	for _, address := range addresses {
		if !publicIP(address) {
			return nil, fmt.Errorf("private image address")
		}
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	for _, address := range addresses {
		connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(address.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		err = dialErr
	}
	return nil, err
}

func validLiveURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && parsed.User == nil
}

func newImageClient() *http.Client {
	return &http.Client{
		Timeout:   8 * time.Second,
		Transport: &http.Transport{Proxy: nil, DialContext: safeDial, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxIdleConns: 8, MaxConnsPerHost: 4},
		CheckRedirect: func(request *http.Request, previous []*http.Request) error {
			if len(previous) >= 3 || !validLiveURL(request.URL.String()) {
				return fmt.Errorf("invalid image redirect")
			}
			return nil
		},
	}
}

func fetchImage(ctx context.Context, client *http.Client, raw string) ([]byte, string, error) {
	if !validLiveURL(raw) {
		return nil, "", fmt.Errorf("invalid image URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, "", err
	}
	accept := "image/webp,image/png,image/jpeg,image/gif"
	if _, err := exec.LookPath(codecPaths(ctx).decoder); err == nil {
		accept = "image/avif," + accept
	}
	request.Header.Set("Accept", accept)
	response, err := client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("image returned %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > 8*1024*1024 {
		return nil, "", fmt.Errorf("image exceeds byte limit")
	}
	release, err := imageSlot(ctx)
	if err != nil {
		return nil, "", err
	}
	defer release()
	if strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "image/svg+xml") {
		decoder := xml.NewDecoder(bytes.NewReader(data))
		depth, elements := 0, 0
		for {
			token, err := decoder.Token()
			if err == io.EOF && depth == 0 && elements > 0 {
				return data, "image/svg+xml", nil
			}
			if err != nil || ctx.Err() != nil {
				return nil, "", fmt.Errorf("unreadable SVG")
			}
			switch token := token.(type) {
			case xml.CharData:
				if depth == 0 && strings.TrimSpace(string(token)) != "" {
					return nil, "", fmt.Errorf("invalid SVG content")
				}
			case xml.StartElement:
				if depth == 0 && (elements > 0 || token.Name.Local != "svg" || token.Name.Space != "http://www.w3.org/2000/svg") {
					return nil, "", fmt.Errorf("invalid SVG root")
				}
				depth++
				elements++
				if depth > 512 || elements > 100_000 {
					return nil, "", fmt.Errorf("SVG exceeds element limit")
				}
			case xml.EndElement:
				depth--
			}
		}
	}
	if len(data) >= 16 && string(data[4:8]) == "ftyp" && (bytes.Contains(data[8:min(64, len(data))], []byte("avif")) || bytes.Contains(data[8:min(64, len(data))], []byte("avis"))) {
		if _, err = decodeAVIF(ctx, data); err != nil {
			return nil, "", err
		}
		return data, "image/avif", nil
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || !validDimensions(config.Width, config.Height) {
		return nil, "", fmt.Errorf("unreadable image")
	}
	if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
		return nil, "", err
	}
	media := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp"}[strings.ToLower(format)]
	if media == "" {
		return nil, "", fmt.Errorf("unsupported image")
	}
	return data, media, nil
}
