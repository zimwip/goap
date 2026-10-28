package lnk

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/zimwip/goap/contracts/content"
)

// TypePage is the type of the things of a web source.
const TypePage = "Page"

// TargetPage is the kind of target of a web address.
const TargetPage = "page"

// Web resolves the addresses of the schemes http and https, and the sources of kind KindWeb: Location is the base
// address of a site, the key of a page its path under it (lnk:handbook/Page/guide/intro.html). It fetches nothing:
// a web address is opened by the reader's browser (OpenExternal), the service never reaches it on a reader's behalf.
type Web struct{}

// Schemes are the schemes of the web.
func (Web) Schemes() []string { return []string{"http", "https"} }

// ResolveDirect reads a web address: the page it names, opened externally.
func (Web) ResolveDirect(_ context.Context, _ Source, u *url.URL) (Target, error) {
	if u.Host == "" || u.User != nil {
		return Target{}, fmt.Errorf("%s: want a web address with a host and no credentials: %w", u.Redacted(), ErrInvalid)
	}
	return page(u, u.String()), nil
}

// Resolve reads the page of a URL of a web source: its address under the base of the source.
func (Web) Resolve(_ context.Context, src Source, u URL) (Target, error) {
	if u.Version != 0 {
		return Target{}, fmt.Errorf("%s: a web source keeps no versions: %w", u, ErrInvalid)
	}
	if u.Type != TypePage {
		return Target{}, fmt.Errorf("%s: a web source holds pages: %w", u, ErrNotFound)
	}
	p, err := cleanKey(u.Key)
	if err != nil {
		return Target{}, fmt.Errorf("%s: %w", u, err)
	}
	base, err := url.Parse(strings.TrimSuffix(src.Location, "/") + "/")
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return Target{}, fmt.Errorf("source %s: location %q is not a web address: %w", src.Name, src.Location, ErrInvalid)
	}
	at := base.JoinPath(strings.Split(p, "/")...)
	t := page(at, at.String())
	t.URL, t.Key = u, p
	return t, nil
}

// URLOf gives the URL of a page: Target.Location under the base of the source, else Target.Key.
func (Web) URLOf(_ context.Context, src Source, t Target) (URL, error) {
	p := t.Key
	if t.Location != "" {
		base := strings.TrimSuffix(src.Location, "/") + "/"
		rest, ok := strings.CutPrefix(t.Location, base)
		if !ok {
			return URL{}, fmt.Errorf("%s is not under %s: %w", t.Location, src.Location, ErrInvalid)
		}
		k, err := url.PathUnescape(rest)
		if err != nil {
			return URL{}, fmt.Errorf("%s: %w", t.Location, ErrInvalid)
		}
		p = k
	}
	p, err := cleanKey(p)
	if err != nil {
		return URL{}, err
	}
	return New(src.Name, TypePage, p), nil
}

// page is the target of a web address, opened by the reader's browser.
func page(u *url.URL, address string) Target {
	title := path.Base(u.Path)
	if title == "/" || title == "." || title == "" {
		title = u.Host
	}
	return Target{
		Kind: TargetPage, Type: TypePage, Key: address, Title: title, Location: address,
		Open: &Open{Mode: OpenExternal, Href: address, Media: content.MediaOf(u.Path)},
	}
}
