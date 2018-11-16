package main

import (
	"bytes"
	"fmt"
	"github.com/terorie/oddb-go/ds/redblackhash"
	"github.com/valyala/fasthttp"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/net/html"
	"path"
	"strconv"
	"strings"
	"time"
)

var client fasthttp.Client

func GetDir(j *Job, f *File) (links []fasthttp.URI, err error) {
	f.IsDir = true
	f.Name = path.Base(string(j.Uri.Path()))

	req := fasthttp.AcquireRequest()
	req.SetRequestURI(j.UriStr)

	res := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(res)

	err = client.DoTimeout(req, res, config.Timeout)
	fasthttp.ReleaseRequest(req)

	if err != nil { return }

	err = checkStatusCode(res.StatusCode())
	if err != nil { return }

	body := res.Body()
	doc := html.NewTokenizer(bytes.NewReader(body))

	var linkHref string
	for {
		tokenType := doc.Next()
		if tokenType == html.ErrorToken {
			break
		}

		switch tokenType {
		case html.StartTagToken:
			name, hasAttr := doc.TagName()
			if len(name) == 1 && name[0] == 'a' {
				for hasAttr {
					var ks, vs []byte
					ks, vs, hasAttr = doc.TagAttr()
					if bytes.Equal(ks, []byte("href")) {
						// TODO Check escape
						linkHref = string(vs)
						break
					}
				}
			}

		case html.EndTagToken:
			name, _ := doc.TagName()
			if len(name) == 1 && name[0] == 'a' {
				// Copy params
				href := linkHref

				// Reset params
				linkHref = ""

				if strings.LastIndexByte(href, '?') != -1 {
					goto nextToken
				}

				switch href {
				case "", " ", ".", "..", "/":
					goto nextToken
				}

				if strings.Contains(href, "../") {
					goto nextToken
				}

				var link fasthttp.URI
				j.Uri.CopyTo(&link)
				link.Update(href)
				if err != nil { continue }

				if !bytes.Equal(link.Scheme(), j.Uri.Scheme()) ||
					!bytes.Equal(link.Host(), j.Uri.Host()) ||
					bytes.Equal(link.Path(), j.Uri.Path()) ||
					!bytes.HasPrefix(link.Path(), j.Uri.Path()) {
					continue
				}

				links = append(links, link)
			}
		}

		nextToken:
	}

	return
}

func GetFile(u fasthttp.URI, f *File) (err error) {
	f.IsDir = false
	cleanPath := path.Clean(string(u.Path()))
	u.SetPath(cleanPath)
	f.Name = path.Base(cleanPath)
	f.Path = strings.Trim(cleanPath, "/")

	req := fasthttp.AcquireRequest()
	req.Header.SetMethod("HEAD")
	req.SetRequestURI(u.String())

	res := fasthttp.AcquireResponse()
	res.SkipBody = true
	defer fasthttp.ReleaseResponse(res)

	err = client.DoTimeout(req, res, config.Timeout)
	fasthttp.ReleaseRequest(req)

	if err != nil { return }

	err = checkStatusCode(res.StatusCode())
	if err != nil { return }

	f.applyContentLength(string(res.Header.Peek("content-length")))
	f.applyLastModified(string(res.Header.Peek("last-modified")))

	return nil
}

func (f *File) HashDir(links []fasthttp.URI) (o redblackhash.Key) {
	h, _ := blake2b.New256(nil)
	h.Write([]byte(f.Name))
	for _, link := range links {
		h.Write(link.Path())
	}
	sum := h.Sum(nil)
	copy(o[:redblackhash.KeySize], sum)
	return
}

func (f *File) applyContentLength(v string) {
	if v == "" { return }
	size, err := strconv.ParseInt(v, 10, 64)
	if err != nil { return }
	if size < 0 { return }
	f.Size = size
}

func (f *File) applyLastModified(v string) {
	if v == "" { return }
	var err error
	f.MTime, err = time.Parse(time.RFC1123, v)
	if err == nil { return }
	f.MTime, err = time.Parse(time.RFC850, v)
	if err == nil { return }
	// TODO Parse asctime
	f.MTime, err = time.Parse("2006-01-02", v[:10])
	if err == nil { return }
}

func checkStatusCode(status int) error {
	switch status {
	case fasthttp.StatusOK:
		return nil

	case fasthttp.StatusTooManyRequests:
		return ErrRateLimit

	case fasthttp.StatusForbidden,
		fasthttp.StatusUnauthorized:
		return ErrForbidden

	default:
		return fmt.Errorf("got HTTP status %d", status)
	}
}
