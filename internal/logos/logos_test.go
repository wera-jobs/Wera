package logos

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// pages serves fixed bodies by host+path, standing in for the boards.
type pages map[string]string

func (p pages) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	body, ok := p[r.URL.Host+r.URL.Path]
	if !ok {
		rec.WriteHeader(http.StatusNotFound)
	} else {
		_, _ = rec.WriteString(body)
	}
	resp := rec.Result()
	resp.Request = r
	return resp, nil
}

func finder(p pages) *Finder {
	return &Finder{UA: "test", Client: &http.Client{Transport: p}}
}

func TestFind(t *testing.T) {
	ctx := context.Background()
	p := pages{
		"job-boards.greenhouse.io/acme":         `<meta property="og:image" content="https://s6-recruiting.cdn.greenhouse.io/external_greenhouse_job_boards/logos/400/1/original/acme.png?17&amp;x=1"/>`,
		"job-boards.greenhouse.io/plain":        `<html>no logo here</html>`,
		"jobs.lever.co/badge":                   `<meta property="og:image" content="https://lever-client-logos.s3.us-west-2.amazonaws.com/badge.png">`,
		"jobs.lever.co/header":                  `<meta property="og:image" content="https://lever-client-logos.s3.us-west-2.amazonaws.com/share.png"><a class="main-header-logo"><img alt="Header logo" src="https://lever-client-logos.s3.us-west-2.amazonaws.com/logo.png"></a>`,
		"careers.smartrecruiters.com/Logo":      `<meta property="og:image" content="https://c.smartrecruiters.com/sr-careersite-image-x/cover"><a id="st-companyLogo" href="/Logo" class="header-logo"><img src="https://c.smartrecruiters.com/sr-careersite-image-x/logo?r=1"></a>`,
		"careers.smartrecruiters.com/Text":      `<meta property="og:image" content="https://c.smartrecruiters.com/sr-careersite-image-x/cover"><a id="st-companyLogo" href="/Text"><span>Text Co</span></a>`,
		"jobs.ashbyhq.com/sq":                   `{"logoWordmarkImageUrl":"https://app.ashbyhq.com/wordmark.png","logoSquareImageUrl":"https://app.ashbyhq.com/square.png"}`,
		"jobs.ashbyhq.com/word":                 `{"logoSquareImageUrl":null,"logoWordmarkImageUrl":"https:\/\/app.ashbyhq.com\/wordmark.png"}`,
		"apply.workable.com/api/v1/accounts/wk": `{"name":"WK","logo":"https://workablehr.s3.amazonaws.com/uploads/account/logo/1/logo"}`,
	}
	f := finder(p)
	for _, c := range []struct{ ats, token, want string }{
		{"greenhouse", "acme", "https://s6-recruiting.cdn.greenhouse.io/external_greenhouse_job_boards/logos/400/1/original/acme.png?17&x=1"},
		{"lever", "header", "https://lever-client-logos.s3.us-west-2.amazonaws.com/logo.png"},
		{"smartrecruiters", "Logo", "https://c.smartrecruiters.com/sr-careersite-image-x/logo?r=1"},
		{"ashby", "sq", "https://app.ashbyhq.com/square.png"},
		{"ashby", "word", "https://app.ashbyhq.com/wordmark.png"},
		{"workday", "centene.wd5/Centene_External", "https://centene.wd5.myworkdayjobs.com/Centene_External/assets/logo"},
		{"workable", "wk", "https://workablehr.s3.amazonaws.com/uploads/account/logo/1/logo"},
	} {
		got, err := f.Find(ctx, c.ats, c.token)
		if err != nil || got != c.want {
			t.Errorf("%s %s: %q, %v; want %q", c.ats, c.token, got, err, c.want)
		}
	}
	// No logo of the company's own: a share image (award badge, cover photo) never counts.
	for _, c := range []struct{ ats, token string }{
		{"greenhouse", "plain"}, {"lever", "badge"}, {"smartrecruiters", "Text"},
		{"greenhouse", "missing"}, {"rippling", "x"}, {"workday", "not a token"},
	} {
		if got, err := f.Find(ctx, c.ats, c.token); !errors.Is(err, ErrNone) {
			t.Errorf("%s %s: %q, %v; want none", c.ats, c.token, got, err)
		}
	}
}

func TestSiteIcon(t *testing.T) {
	base, _ := url.Parse("https://www.acme.com/careers/")
	page := []byte(`<link rel="mask-icon" href="/mask.svg"><link rel="icon" href="/favicon.ico">
		<link rel="icon" type="image/png" sizes="32x32" href="/i32.png"><link rel="icon" sizes="192x192" href="/i192.png">
		<link href='/touch.png' rel='apple-touch-icon' sizes='180x180'>`)
	if got := siteIcon(page, base); got != "https://www.acme.com/touch.png" {
		t.Errorf("apple-touch-icon first: %q", got)
	}
	page = []byte(`<link rel="shortcut icon" href="/favicon.ico"><link rel="icon" sizes="32x32" href="i32.png"><link rel="icon" sizes="192x192" href="//cdn.acme.com/i192.png">`)
	if got := siteIcon(page, base); got != "https://cdn.acme.com/i192.png" {
		t.Errorf("largest icon: %q", got)
	}
	if got := siteIcon([]byte(`<link rel="mask-icon" href="/mask.svg"><link rel="icon" href="/favicon.ico">`), base); got != "" {
		t.Errorf("no usable icon: %q", got)
	}
}

func pngOf(w, h int, c color.Color) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestFetch(t *testing.T) {
	ctx := context.Background()
	whiteText := image.NewNRGBA(image.Rect(0, 0, 100, 40))
	for x := 10; x < 90; x++ {
		whiteText.Set(x, 20, color.White)
	}
	var wt bytes.Buffer
	_ = png.Encode(&wt, whiteText)
	f := finder(pages{
		"logos.test/big.png":   string(pngOf(1200, 600, color.NRGBA{200, 0, 0, 255})),
		"logos.test/tiny.png":  string(pngOf(8, 8, color.Black)),
		"logos.test/white.png": wt.String(),
		"logos.test/mark.svg":  `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`,
		"logos.test/page.html": `<html>not an image</html>`,
	})
	l, err := f.Fetch(ctx, "https://logos.test/big.png")
	if err != nil {
		t.Fatal(err)
	}
	img, _ := png.Decode(bytes.NewReader(l.Data))
	if b := img.Bounds(); b.Dx() != 256 || b.Dy() != 128 || l.ContentType != "image/png" {
		t.Errorf("scaled to %v (%s)", b, l.ContentType)
	}
	if _, err := f.Fetch(ctx, "https://logos.test/tiny.png"); err == nil {
		t.Error("a tiny image is not a logo")
	}
	if _, err := f.Fetch(ctx, "https://logos.test/page.html"); err == nil {
		t.Error("a page is not an image")
	}
	if _, err := f.Fetch(ctx, "http://logos.test/big.png"); err == nil {
		t.Error("plain HTTP is refused")
	}
	if l, err := f.Fetch(ctx, "https://logos.test/mark.svg"); err != nil || l.ContentType != "image/svg+xml" {
		t.Errorf("svg: %v %v", l.ContentType, err)
	}
	// White on transparent gets a dark tile, so it shows on a light card.
	l, err = f.Fetch(ctx, "https://logos.test/white.png")
	if err != nil {
		t.Fatal(err)
	}
	img, _ = png.Decode(bytes.NewReader(l.Data))
	if r, g, b, a := img.At(0, 0).RGBA(); a != 0xffff || r > 0x4000 || g > 0x4000 || b > 0x4000 {
		t.Errorf("corner of a white logo: %x %x %x %x, want opaque dark", r, g, b, a)
	}
	// A red logo on transparent stays as it is.
	if whiteOnClear(image.NewNRGBA(image.Rect(0, 0, 10, 10))) {
		t.Error("an empty image is not white on clear")
	}
}

func TestPublicOnly(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:443", "10.1.2.3:443", "192.168.1.5:443", "172.16.0.1:443", "169.254.169.254:80", "100.64.1.1:443", "[::1]:443", "[fd00::1]:443", "0.0.0.0:443", "[::ffff:127.0.0.1]:443"} {
		if err := publicOnly("tcp", addr, nil); err == nil {
			t.Errorf("%s allowed", addr)
		}
	}
	for _, addr := range []string{"8.8.8.8:443", "[2606:4700::1111]:443"} {
		if err := publicOnly("tcp", addr, nil); err != nil {
			t.Errorf("%s refused: %v", addr, err)
		}
	}
	if !strings.Contains(New("ua").UA, "ua") {
		t.Error("UA not kept")
	}
}

// counting answers 403 to every request and counts them.
type counting struct{ n int }

func (c *counting) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n++
	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusForbidden)
	resp := rec.Result()
	resp.Request = r
	return resp, nil
}

func TestRefusingBoardIsLeftAlone(t *testing.T) {
	c := &counting{}
	f := &Finder{UA: "test", Client: &http.Client{Transport: c}}
	for _, tok := range []string{"a", "b", "c"} {
		if _, err := f.Find(context.Background(), "lever", tok); !errors.Is(err, ErrRefused) {
			t.Fatalf("%s: %v, want refused", tok, err)
		}
	}
	if c.n != 1 {
		t.Errorf("asked a refusing board %d times, want once", c.n)
	}
	f.Reset()
	_, _ = f.Find(context.Background(), "lever", "d")
	if c.n != 2 {
		t.Errorf("a new pass asks again: %d", c.n)
	}
}

func TestTrim(t *testing.T) {
	// A small red mark in the middle of a big white canvas.
	img := image.NewNRGBA(image.Rect(0, 0, 400, 200))
	for y := range 200 {
		for x := range 400 {
			img.Set(x, y, color.White)
		}
	}
	for y := 80; y < 120; y++ {
		for x := 150; x < 250; x++ {
			img.Set(x, y, color.NRGBA{200, 0, 0, 255})
		}
	}
	got := trim(img).Bounds()
	if got.Dx() < 100 || got.Dx() > 110 || got.Dy() < 40 || got.Dy() > 50 {
		t.Errorf("trimmed to %v, want about 100x40 plus a little air", got)
	}
	// All margin: left as it is.
	blankImg := image.NewNRGBA(image.Rect(0, 0, 50, 50))
	if trim(blankImg).Bounds() != blankImg.Bounds() {
		t.Error("an empty image was cropped")
	}
}
