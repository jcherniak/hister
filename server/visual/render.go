package visual

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

const ReplayCSP = "default-src 'none'; img-src data:; font-src data:; style-src 'unsafe-inline'; script-src 'none'; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"

var cssURL = regexp.MustCompile(`(?i)url\(\s*(?:"([^"]*)"|'([^']*)'|([^\s)]*))\s*\)`)
var cssImport = regexp.MustCompile(`(?i)@import\s+(?:url\(\s*(?:"([^"]*)"|'([^']*)'|([^\s)]*))\s*\)|"([^"]*)"|'([^']*)')\s*([^;]*);`)
var cssReference = regexp.MustCompile(cssImport.String() + "|" + cssURL.String())

func resolveURL(base, reference string) string {
	parsed, err := url.Parse(reference)
	if err != nil {
		return ""
	}
	parent, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return parent.ResolveReference(parsed).String()
}

func firstValue(values []string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func dataURI(mime string, data []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func importCondition(condition, keyword string) (string, string, bool) {
	if !strings.HasPrefix(condition, keyword) {
		return "", condition, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(condition, keyword))
	if !strings.HasPrefix(rest, "(") {
		return "", rest, true
	}
	depth := 0
	for index, char := range rest {
		if char == '(' {
			depth++
		}
		if char == ')' {
			depth--
			if depth == 0 {
				return rest[1:index], strings.TrimSpace(rest[index+1:]), true
			}
		}
	}
	return "", "", true
}

func cssText(value string) string { return strings.ReplaceAll(value, "<", `\3c `) }

type replayResource struct {
	resource Resource
	data     []byte
	uri      string
}

var replaySlots = make(chan struct{}, 8)
var renderSlots = make(chan struct{}, 2)

type limitedHTML struct {
	bytes.Buffer
	limit int
	ctx   context.Context
}

func (output *limitedHTML) Write(data []byte) (int, error) {
	if output.ctx != nil && output.ctx.Err() != nil {
		return 0, output.ctx.Err()
	}
	if output.Len()+len(data) > output.limit {
		return 0, fmt.Errorf("visual preview exceeds output limit")
	}
	return output.Buffer.Write(data)
}

func (output *limitedHTML) WriteString(value string) (int, error) { return output.Write([]byte(value)) }

func rewriteCSS(css, base string, resources map[string]replayResource, depth int) string {
	budget := 16 * 1024 * 1024
	return rewriteCSSBudget(css, base, resources, depth, &budget)
}

func rewriteCSSBudget(css, base string, resources map[string]replayResource, depth int, budget *int) string {
	if len(css) > *budget {
		return ""
	}
	*budget -= len(css)
	return cssReference.ReplaceAllStringFunc(css, func(statement string) string {
		if !strings.HasPrefix(strings.ToLower(statement), "@import") {
			parts := cssURL.FindStringSubmatch(statement)
			reference := firstValue(parts[1:])
			if strings.HasPrefix(reference, "#") {
				return statement
			}
			asset, ok := resources[resolveURL(base, reference)]
			if !ok || asset.uri == "" {
				return `url("")`
			}
			cost := max(0, len(asset.uri)+7-len(statement))
			if cost > *budget {
				return `url("")`
			}
			*budget -= cost
			return `url("` + asset.uri + `")`
		}
		if depth >= 8 {
			return ""
		}
		parts := cssImport.FindStringSubmatch(statement)
		key := resolveURL(base, firstValue(parts[1:6]))
		asset, ok := resources[key]
		if !ok || asset.resource.Kind != "css" {
			return ""
		}
		if len(asset.data) > *budget {
			return ""
		}
		nested := rewriteCSSBudget(string(asset.data), key, resources, depth+1, budget)
		condition := strings.TrimSpace(parts[6])
		layer, condition, hasLayer := importCondition(condition, "layer")
		supports, condition, hasSupports := importCondition(condition, "supports")
		if condition != "" {
			nested = "@media " + condition + "{" + nested + "}"
		}
		if hasSupports {
			nested = "@supports (" + supports + "){" + nested + "}"
		}
		if hasLayer {
			nested = "@layer " + layer + "{" + nested + "}"
		}
		return nested
	})
}

func Render(ctx context.Context, pageURL string, manifest *Manifest, markup []byte, read func(Resource) ([]byte, error), progress func(int, int, int)) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	select {
	case renderSlots <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-renderSlots }()
	if manifest == nil || len(markup) > 16*1024*1024 || len(manifest.Resources) > 256 {
		return "", fmt.Errorf("visual preview exceeds limits")
	}
	resources := make(map[string]replayResource, len(manifest.Resources))
	unique := make([]Resource, 0, len(manifest.Resources))
	seen := make(map[string]bool)
	for _, resource := range manifest.Resources {
		if !seen[resource.URL] {
			seen[resource.URL] = true
			unique = append(unique, resource)
		}
	}
	client := newImageClient()
	defer client.CloseIdleConnections()
	liveContext, cancelLive := context.WithTimeout(ctx, 20*time.Second)
	defer cancelLive()
	var lock sync.Mutex
	var stored sync.Map
	type loadedResource struct {
		once sync.Once
		data []byte
		mime string
		err  error
	}
	loadStored := func(resource Resource) ([]byte, string, error) {
		key := fmt.Sprintf("%s:%s:%d:%d:%d:%d", resource.Kind, resource.Key, resource.Width, resource.Height, resource.StoredWidth, resource.StoredHeight)
		entry, _ := stored.LoadOrStore(key, &loadedResource{})
		loaded := entry.(*loadedResource)
		loaded.once.Do(func() {
			loaded.mime = resource.MIME
			loaded.data, loaded.err = read(resource)
			if loaded.err == nil && len(loaded.data) > MaxResourceBytes {
				loaded.err = fmt.Errorf("resource exceeds byte limit")
			}
			if loaded.err == nil && resource.Kind == "image" {
				loaded.data, loaded.mime, loaded.err = restoreImage(ctx, resource, loaded.data)
			}
		})
		return loaded.data, loaded.mime, loaded.err
	}
	completed, failed, totalBytes := 0, 0, 0
	if progress != nil {
		progress(0, len(unique), 0)
	}
	jobs := make(chan Resource)
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for resource := range jobs {
				select {
				case replaySlots <- struct{}{}:
				case <-ctx.Done():
					return
				}
				var data []byte
				var err error
				media := resource.MIME
				if resource.Kind == "image" {
					data, media, err = fetchImage(liveContext, client, resource.URL)
				}
				if resource.Kind != "image" || err != nil {
					data, media, err = loadStored(resource)
				}
				lock.Lock()
				totalBytes += len(data)
				if err != nil || totalBytes > 96*1024*1024 {
					failed++
				} else {
					asset := replayResource{resource: resource, data: data}
					if resource.Kind != "css" {
						asset.uri = dataURI(media, data)
					}
					resources[resource.URL] = asset
				}
				completed++
				if progress != nil {
					progress(completed, len(unique), failed)
				}
				lock.Unlock()
				<-replaySlots
			}
		}()
	}
	for _, resource := range unique {
		select {
		case jobs <- resource:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return "", ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if totalBytes > 96*1024*1024 {
		return "", fmt.Errorf("visual preview exceeds memory limit")
	}
	root, err := html.Parse(bytes.NewReader(markup))
	if err != nil {
		return "", err
	}
	var head *html.Node
	cssBudget := 96 * 1024 * 1024
	css := func(value, base string) string {
		value = rewriteCSSBudget(value, base, resources, 0, &cssBudget)
		escaping := strings.Count(value, "<") * 3
		if escaping > cssBudget {
			return ""
		}
		cssBudget -= escaping
		return cssText(value)
	}
	nodes := 0
	var cleanErr error
	var clean func(*html.Node, int)
	clean = func(node *html.Node, depth int) {
		nodes++
		if depth > 512 || nodes > 100_000 {
			cleanErr = fmt.Errorf("visual preview exceeds DOM limit")
		}
		if ctx.Err() != nil || cleanErr != nil {
			return
		}
		for child := node.FirstChild; child != nil; {
			next := child.NextSibling
			if child.Type == html.CommentNode {
				node.RemoveChild(child)
				child = next
				continue
			}
			if child.Type == html.ElementNode {
				tag := strings.ToLower(child.Data)
				switch tag {
				case "script", "iframe", "object", "embed", "base", "meta", "noscript", "source":
					node.RemoveChild(child)
					child = next
					continue
				case "head":
					head = child
				case "form":
					child.Data = "div"
				case "link":
					href, relation, media := "", "", ""
					for _, attribute := range child.Attr {
						switch attribute.Key {
						case "href":
							href = attribute.Val
						case "rel":
							relation = attribute.Val
						case "media":
							media = attribute.Val
						}
					}
					asset, ok := resources[resolveURL(pageURL, href)]
					if strings.EqualFold(relation, "stylesheet") && ok && asset.resource.Kind == "css" {
						child.Data = "style"
						child.Attr = nil
						if media != "" {
							child.Attr = []html.Attribute{{Key: "media", Val: media}}
						}
						child.AppendChild(&html.Node{Type: html.TextNode, Data: css(string(asset.data), asset.resource.URL)})
					} else {
						node.RemoveChild(child)
						child = next
						continue
					}
				}
				attributes := child.Attr[:0]
				for _, attribute := range child.Attr {
					key := strings.ToLower(attribute.Key)
					if strings.HasPrefix(key, "on") || key == "srcdoc" || key == "srcset" || key == "ping" || key == "action" || key == "formaction" || key == "target" || key == "background" || key == "autofocus" || key == "autoplay" || key == "is" || key == "nonce" || key == "integrity" {
						continue
					}
					if key == "style" {
						attribute.Val = css(attribute.Val, pageURL)
					}
					if key == "href" || key == "xlink:href" {
						if !strings.HasPrefix(attribute.Val, "#") {
							continue
						}
					}
					if key == "src" || key == "poster" {
						asset, ok := resources[resolveURL(pageURL, attribute.Val)]
						if !ok || asset.uri == "" {
							continue
						}
						attribute.Val = asset.uri
					}
					if key == "contenteditable" {
						continue
					}
					attributes = append(attributes, attribute)
				}
				child.Attr = attributes
				if tag == "style" {
					for text := child.FirstChild; text != nil; text = text.NextSibling {
						if text.Type == html.TextNode {
							text.Data = css(text.Data, pageURL)
						}
					}
				}
			}
			clean(child, depth+1)
			child = next
		}
	}
	clean(root, 0)
	if cleanErr != nil {
		return "", cleanErr
	}
	if head != nil {
		meta := &html.Node{Type: html.ElementNode, Data: "meta", Attr: []html.Attribute{{Key: "http-equiv", Val: "Content-Security-Policy"}, {Key: "content", Val: ReplayCSP}}}
		head.InsertBefore(meta, head.FirstChild)
	}
	output := limitedHTML{limit: 128 << 20, ctx: ctx}
	if err = html.Render(&output, root); err != nil {
		return "", err
	}
	return output.String(), nil
}
