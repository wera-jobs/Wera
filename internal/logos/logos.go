// Package logos finds a company's logo on its applicant system's job
// board and turns it into a small image Wera can serve itself, so a
// browser never asks a third party for one.
package logos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"image"
	"image/color"
	_ "image/gif" // decoders for the formats boards use
	_ "image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // WebP logos (Ashby serves some)
)

// Limits on what is fetched and kept.
const (
	maxPage  = 4 << 20   // a board page
	maxImage = 3 << 20   // a logo as downloaded
	maxSVG   = 200 << 10 // an SVG kept as is
	maxSide  = 256       // the longest side kept, in pixels
	minSide  = 16        // smaller is a stray icon, not a logo
)

// ErrNone means the board shows no logo of its own.
var ErrNone = errors.New("no logo on the board")

// Finder reads boards and downloads logos, politely: one request at a
// time per board host, spaced out, and none more to a host that has
// started refusing (403 or 429) until the next pass.
type Finder struct {
	Client *http.Client
	UA     string

	mu      sync.Mutex
	next    map[string]time.Time // when a host may be asked again
	refused map[string]bool
}

// Gaps between requests to one host.
const (
	boardGap = 3 * time.Second        // a board's own pages (Greenhouse refuses faster)
	otherGap = 250 * time.Millisecond // image hosts and company sites
)

// ErrRefused means a board host has started refusing this pass's requests.
var ErrRefused = errors.New("the board refused: trying again on the next pass")

// Reset forgets refusals, for a new pass.
func (f *Finder) Reset() {
	f.mu.Lock()
	f.refused = nil
	f.mu.Unlock()
}

// wait holds a request to host until its turn, or reports the host refused.
func (f *Finder) wait(ctx context.Context, u *url.URL) error {
	gap := otherGap
	if boardHost(u) || strings.HasSuffix(u.Hostname(), ".myworkdayjobs.com") {
		gap = boardGap
	}
	f.mu.Lock()
	if f.refused[u.Host] {
		f.mu.Unlock()
		return ErrRefused
	}
	if f.next == nil {
		f.next = map[string]time.Time{}
	}
	at := time.Now()
	if n := f.next[u.Host]; n.After(at) {
		at = n
	}
	f.next[u.Host] = at.Add(gap)
	f.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Until(at)):
		return nil
	}
}

func (f *Finder) refuse(host string) {
	f.mu.Lock()
	if f.refused == nil {
		f.refused = map[string]bool{}
	}
	f.refused[host] = true
	f.mu.Unlock()
}

// New returns a Finder whose client connects only to public addresses over
// HTTPS: a board may send it to the company's own site, and nothing a page
// says may point it inside the network.
func New(ua string) *Finder {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: publicOnly}
	return &Finder{UA: ua, Client: &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			MaxIdleConnsPerHost: 2,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to %s is not HTTPS", req.URL)
			}
			return nil
		},
	}}
}

// publicOnly refuses connections to loopback, private, link-local and other
// non-public addresses (it runs after DNS, on the address dialed).
func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("refusing %s: not an IP address", address)
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip) {
		return fmt.Errorf("refusing to connect to %s: not a public address", address)
	}
	return nil
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// Supported reports whether logos are looked for on this system's boards.
func Supported(ats string) bool {
	switch ats {
	case "greenhouse", "lever", "ashby", "workday", "smartrecruiters", "workable":
		return true
	}
	return false
}

var (
	greenhouseLogo = regexp.MustCompile(`https://(?:s\d*-)?recruiting\.cdn\.greenhouse\.io/external_greenhouse_job_boards/logos/[^"'\s<>]+`)
	// The header's <img>, never the share image (og:image), which some
	// boards set to an award badge.
	leverLogo     = regexp.MustCompile(`<img[^>]*\bsrc="(https://lever-client-logos\.s3[a-z0-9.-]*\.amazonaws\.com/[^"]+)"`)
	ashbySquare   = regexp.MustCompile(`"logoSquareImageUrl"\s*:\s*("[^"]+")`)
	ashbyWordmark = regexp.MustCompile(`"logoWordmarkImageUrl"\s*:\s*("[^"]+")`)
	// The header's logo link: an image when the company set one, else its
	// name as text. Never the share image, which is often a cover photo.
	srLogo       = regexp.MustCompile(`id="st-companyLogo"[^>]*>\s*<img[^>]*\bsrc="(https://c\.smartrecruiters\.com/[^"]+)"`)
	workdayToken = regexp.MustCompile(`^([a-z0-9-]+)\.(wd\d+)/([A-Za-z0-9_-]+)$`)
)

// Find returns the URL of the company's logo on its board, or ErrNone.
func (f *Finder) Find(ctx context.Context, ats, token string) (string, error) {
	switch ats {
	case "greenhouse":
		return f.greenhouse(ctx, token)
	case "lever":
		return f.fromPage(ctx, "https://jobs.lever.co/"+url.PathEscape(token), leverLogo)
	case "smartrecruiters":
		return f.fromPage(ctx, "https://careers.smartrecruiters.com/"+url.PathEscape(token), srLogo)
	case "ashby":
		page, err := f.get(ctx, "https://jobs.ashbyhq.com/"+url.PathEscape(token), maxPage)
		if err != nil {
			return "", err
		}
		for _, re := range []*regexp.Regexp{ashbySquare, ashbyWordmark} {
			if m := re.FindSubmatch(page); m != nil {
				var u string
				if json.Unmarshal(m[1], &u) == nil && u != "" {
					return u, nil
				}
			}
		}
		return "", ErrNone
	case "workday":
		m := workdayToken.FindStringSubmatch(token)
		if m == nil {
			return "", ErrNone
		}
		// Workday serves the logo itself at a fixed path; Fetch checks it is an image.
		return fmt.Sprintf("https://%s.%s.myworkdayjobs.com/%s/assets/logo", m[1], m[2], m[3]), nil
	case "workable":
		body, err := f.get(ctx, "https://apply.workable.com/api/v1/accounts/"+url.PathEscape(token), maxPage)
		if err != nil {
			return "", err
		}
		var acct struct {
			Logo string `json:"logo"`
		}
		if json.Unmarshal(body, &acct) != nil || acct.Logo == "" {
			return "", ErrNone
		}
		return acct.Logo, nil
	}
	return "", ErrNone
}

// greenhouse reads the board page; a board that sends visitors to the
// company's own careers site gets that site's icon, and a page without a
// logo gets one more look on the embedded board.
func (f *Finder) greenhouse(ctx context.Context, token string) (string, error) {
	page, final, err := f.fetch(ctx, "https://job-boards.greenhouse.io/"+url.PathEscape(token), maxPage)
	if err != nil {
		return "", err
	}
	if m := greenhouseLogo.Find(page); m != nil {
		return html.UnescapeString(string(m)), nil
	}
	if !boardHost(final) {
		if icon := siteIcon(page, final); icon != "" {
			return icon, nil
		}
		if icon := f.homeIcon(ctx, final); icon != "" {
			return icon, nil
		}
	}
	return f.fromPage(ctx, "https://boards.greenhouse.io/embed/job_board?for="+url.QueryEscape(token), greenhouseLogo)
}

var (
	linkTag   = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	attrValue = func(name string) *regexp.Regexp {
		return regexp.MustCompile(`(?is)\b` + name + `\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	}
	relAttr, hrefAttr, sizesAttr = attrValue("rel"), attrValue("href"), attrValue("sizes")
)

func attr(re *regexp.Regexp, tag string) string {
	m := re.FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	return html.UnescapeString(m[1] + m[2] + m[3])
}

// siteIcon picks a company site's own icon from its page: an
// apple-touch-icon first (they are large), else the largest declared icon.
// ICO files are skipped (they can't be read here and are usually tiny).
func siteIcon(page []byte, base *url.URL) string {
	type icon struct {
		href  string
		touch bool
		size  int
	}
	var icons []icon
	for _, tag := range linkTag.FindAllString(string(page), 50) {
		rel := strings.ToLower(attr(relAttr, tag))
		href := attr(hrefAttr, tag)
		if href == "" || strings.Contains(strings.ToLower(href), ".ico") {
			continue
		}
		touch, plain := false, false
		for _, t := range strings.Fields(rel) {
			touch = touch || t == "apple-touch-icon" || t == "apple-touch-icon-precomposed"
			plain = plain || t == "icon" // not mask-icon: Safari's one-color outline
		}
		if !touch && !plain {
			continue
		}
		size := 0
		if s := strings.ToLower(attr(sizesAttr, tag)); s != "" {
			size, _ = strconv.Atoi(strings.SplitN(s, "x", 2)[0])
		}
		icons = append(icons, icon{href, touch, size})
	}
	sort.SliceStable(icons, func(i, j int) bool {
		if icons[i].touch != icons[j].touch {
			return icons[i].touch
		}
		return icons[i].size > icons[j].size
	})
	for _, ic := range icons {
		u, err := base.Parse(ic.href)
		if err == nil && u.Scheme == "https" {
			return u.String()
		}
	}
	return ""
}

// homeIcon looks for the site's icon on its home page, then at the
// conventional /apple-touch-icon.png.
func (f *Finder) homeIcon(ctx context.Context, site *url.URL) string {
	home := &url.URL{Scheme: "https", Host: site.Host, Path: "/"}
	if page, final, err := f.fetch(ctx, home.String(), maxPage); err == nil {
		if icon := siteIcon(page, final); icon != "" {
			return icon
		}
	}
	touch := &url.URL{Scheme: "https", Host: site.Host, Path: "/apple-touch-icon.png"}
	if body, err := f.get(ctx, touch.String(), maxImage); err == nil && strings.HasPrefix(http.DetectContentType(body), "image/") {
		return touch.String()
	}
	return ""
}

func (f *Finder) fromPage(ctx context.Context, page string, re *regexp.Regexp) (string, error) {
	body, err := f.get(ctx, page, maxPage)
	if err != nil {
		return "", err
	}
	m := re.FindSubmatch(body)
	if m == nil {
		return "", ErrNone
	}
	if len(m) > 1 {
		return html.UnescapeString(string(m[1])), nil
	}
	return html.UnescapeString(string(m[0])), nil
}

// Logo is an image ready to serve.
type Logo struct {
	Data        []byte
	ContentType string // image/png or image/svg+xml
}

// Fetch downloads the image at src over HTTPS and returns it as a PNG no
// larger than maxSide (an SVG is kept as it is).
func (f *Finder) Fetch(ctx context.Context, src string) (Logo, error) {
	u, err := url.Parse(src)
	if err != nil || u.Scheme != "https" {
		return Logo{}, fmt.Errorf("logo URL not allowed: %q", src)
	}
	body, err := f.get(ctx, src, maxImage)
	if err != nil {
		return Logo{}, err
	}
	if looksSVG(body) {
		if len(body) > maxSVG {
			return Logo{}, errors.New("SVG logo too large")
		}
		return Logo{Data: body, ContentType: "image/svg+xml"}, nil
	}
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return Logo{}, fmt.Errorf("not an image: %w", err)
	}
	return encode(img)
}

func looksSVG(b []byte) bool {
	head := strings.ToLower(string(b[:min(len(b), 512)]))
	return strings.Contains(head, "<svg")
}

// encode trims empty margins, scales img down to maxSide on its longest
// side and writes a PNG.
func encode(img image.Image) (Logo, error) {
	img = trim(img)
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < minSide || h < minSide {
		return Logo{}, fmt.Errorf("image too small: %dx%d", w, h)
	}
	if w > maxSide || h > maxSide {
		scale := float64(maxSide) / float64(max(w, h))
		nw, nh := max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
		dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
		img = dst
	}
	if whiteOnClear(img) {
		img = onDark(img)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return Logo{}, err
	}
	return Logo{Data: buf.Bytes(), ContentType: "image/png"}, nil
}

// blank reports a pixel that shows nothing on a white tile: transparent,
// or near white.
func blank(c color.Color) bool {
	r, g, b, a := c.RGBA()
	if a < 0x1000 {
		return true
	}
	return a > 0xf000 && min(r, g, b) > 0xf400
}

// trim crops margins that show nothing, keeping a little air, so a logo
// fills the small tile it is shown in. An image that is all margin, or a
// white-on-clear one (whose white is the logo), is left as it is.
func trim(img image.Image) image.Image {
	b := img.Bounds()
	minX, minY, maxX, maxY := b.Max.X, b.Max.Y, b.Min.X-1, b.Min.Y-1
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if blank(img.At(x, y)) {
				continue
			}
			minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x), max(maxY, y)
		}
	}
	if maxX < minX || whiteOnClear(img) {
		return img
	}
	pad := max(1, max(maxX-minX, maxY-minY)/40)
	r := image.Rect(minX-pad, minY-pad, maxX+1+pad, maxY+1+pad).Intersect(b)
	if r.Dx() < minSide || r.Dy() < minSide || r == b {
		return img
	}
	dst := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(dst, dst.Bounds(), img, r.Min, draw.Src)
	return dst
}

// whiteOnClear reports a logo drawn in white on a transparent background,
// made for dark pages: on a light card it would vanish.
func whiteOnClear(img image.Image) bool {
	b := img.Bounds()
	visible, white := 0, 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			if a < 0x3000 {
				continue
			}
			visible++
			// Un-premultiply, then call it white when every channel is near full.
			if min(r, g, bl)*0xffff/a > 0xeb00 {
				white++
			}
		}
	}
	return visible > 0 && white*10 >= visible*9 && visible*10 < b.Dx()*b.Dy()*9
}

// onDark puts a logo on an opaque dark tile, so it shows on any page.
func onDark(img image.Image) image.Image {
	b := img.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(darkTile), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Over)
	return dst
}

var darkTile = color.NRGBA{R: 0x1f, G: 0x23, B: 0x2b, A: 0xff}

func (f *Finder) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	body, _, err := f.fetch(ctx, u, limit)
	return body, err
}

// fetch is get that also returns the URL the body came from, after redirects.
func (f *Finder) fetch(ctx context.Context, u string, limit int64) ([]byte, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	if err := f.wait(ctx, req.URL); err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", f.UA)
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) && boardHost(resp.Request.URL) {
		f.refuse(resp.Request.URL.Host)
		return nil, nil, ErrRefused
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return nil, nil, ErrNone
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(body)) > limit {
		return nil, nil, fmt.Errorf("GET %s: larger than %d bytes", u, limit)
	}
	return body, resp.Request.URL, nil
}

// boardHost reports whether u is a board page a lookup may be redirected
// within (a board moving between its own domains).
func boardHost(u *url.URL) bool {
	h := strings.ToLower(u.Hostname())
	switch h {
	case "job-boards.greenhouse.io", "boards.greenhouse.io", "jobs.lever.co", "jobs.ashbyhq.com",
		"careers.smartrecruiters.com", "jobs.smartrecruiters.com", "apply.workable.com":
		return u.Scheme == "https"
	}
	return false
}
