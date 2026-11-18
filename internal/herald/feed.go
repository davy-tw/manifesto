/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package herald

import (
	"bytes"
	"encoding/xml"
	"errors"
	"time"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/render"
)

var errNoTheme = errors.New("the Gazette's theme has never parsed")

type rss struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	DC      string     `xml:"xmlns:dc,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Language    string    `xml:"language,omitempty"`
	Items       []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string  `xml:"title"`
	Link        string  `xml:"link"`
	GUID        rssGUID `xml:"guid"`
	PubDate     string  `xml:"pubDate,omitempty"`
	Description string  `xml:"description,omitempty"`
	// Creator is the author's display name. RSS's own <author> must be an
	// email address, so the Dublin Core element is used instead.
	Creator    string   `xml:"dc:creator,omitempty"`
	Categories []string `xml:"category"`
}

type rssGUID struct {
	IsPermaLink bool   `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

// wire renders the Gazette's RSS 2.0 feed of ds.
func wire(s *render.Site, ds []*v1.Dispatch) ([]byte, error) {
	g := s.GazetteView()
	feed := rss{Version: "2.0", DC: "http://purl.org/dc/elements/1.1/", Channel: rssChannel{
		Title: g.Title, Link: g.URL + "/", Description: g.Tagline, Language: g.Language,
	}}
	if feed.Channel.Description == "" {
		feed.Channel.Description = g.Title
	}
	for _, d := range ds {
		v := s.DispatchView(d)
		item := rssItem{Title: v.Title, Link: v.URL, GUID: rssGUID{IsPermaLink: true, Value: v.URL}, Description: v.Summary}
		if v.PublishedAt != nil {
			item.PubDate = v.PublishedAt.UTC().Format(time.RFC1123Z)
		}
		if v.Scribe != nil {
			item.Creator = v.Scribe.DisplayName
		}
		if v.Section != nil {
			item.Categories = append(item.Categories, v.Section.DisplayName)
		}
		for _, st := range v.Stamps {
			item.Categories = append(item.Categories, st.DisplayName)
		}
		feed.Channel.Items = append(feed.Channel.Items, item)
	}
	return marshalXML(feed)
}

type urlset struct {
	XMLName xml.Name     `xml:"urlset"`
	XMLNS   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	XMLName xml.Name `xml:"url"`
	Loc     string   `xml:"loc"`
	LastMod string   `xml:"lastmod,omitempty"`
}

// sitemap lists the front page, the listings and every Published Dispatch.
// Unlisted Dispatches are left out.
func sitemap(s *render.Site, ds []*v1.Dispatch, listings []string) ([]byte, error) {
	base := s.GazetteView().URL
	m := urlset{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	m.URLs = append(m.URLs, sitemapURL{Loc: base + "/"})
	for _, p := range listings {
		m.URLs = append(m.URLs, sitemapURL{Loc: base + p})
	}
	for _, d := range ds {
		u := sitemapURL{Loc: render.DispatchURL(s.Gazette.Spec.Domain, d.Name)}
		if d.Status.PublishedAt != nil {
			u.LastMod = d.Status.PublishedAt.UTC().Format(time.RFC3339)
		}
		m.URLs = append(m.URLs, u)
	}
	return marshalXML(m)
}

func marshalXML(v any) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}
