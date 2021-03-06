// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tiddly

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"

	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"
	"google.golang.org/appengine/user"
)

// Re Authentication
//
// There are currently three redundant layers of authentication checks here.
//
// 1. app.yaml says 'login: admin'.
// 2. The installed handlers are wrapped in authCheck during registration in func init.
// 3. The write operations contain an extra mustBeAdmin check.
//
// The redundancy is mainly cautionary, to contain accidents.
//
// It should be possible to make a world-readable, admin-writable TiddlyWiki
// by removing 1 and 2 and double-checking 3.
//
// If you remove 'login: admin' from app.yaml you can replace it with 'login: required',
// requiring a login from any viewer, or you can delete the line entirely,
// making it possible to fetch pages with no authentication.
// In that case, users who do have write access (admins) will need to take the extra
// step of logging in. One way to do this is to make the /auth URL require login
// and have them start there when visiting, by listing that separately in app.yaml
// before the default handler:
//
//	handlers:
//	- url: /auth
//	  login: admin
//	  secure: always
//	  script: _go_app
//
//	- url: /.*
//	  secure: always
//	  script: _go_app
//
// If you do this, then unauthenticated users will be able to read content,
// and TiddlyWiki will let them edit content in their browser, but writes back
// to the server will fail, producing yellow pop-up error messages in the
// browser window. In general these are probably good, but this includes
// attempts to update $:/StoryList, which happens as viewers click around
// in the wiki. It seems like the TiddlyWeb plugin or the core syncer module
// would need changes to understand a new "read-only" mode.

// WriteTiddlerHistory controls if tiddler alterations are written as TiddlerHistory entries in the datastore.
const WriteTiddlerHistory = false

func init() {
	http.HandleFunc("/", authCheck(main))
	http.HandleFunc("/auth", authCheck(auth))
	http.HandleFunc("/status", authCheck(status))
	http.HandleFunc("/search/", authCheck(search))
	http.HandleFunc("/recipes/all/tiddlers/", authCheck(tiddler))
	http.HandleFunc("/recipes/all/tiddlers.json", authCheck(tiddlerList))
	http.HandleFunc("/bags/bag/tiddlers/", authCheck(deleteTiddler))
}

func authCheck(f http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !mustBeAdmin(w, r) {
			return
		}
		f(w, r)
	}
}

func mustBeAdmin(w http.ResponseWriter, r *http.Request) bool {
	ctx := appengine.NewContext(r)
	u := user.Current(ctx)
	if u == nil || !user.IsAdmin(ctx) {
		http.Error(w, "permission denied", 403)
		return false
	}
	return true
}

type Tiddler struct {
	Rev  int    `datastore:"Rev,noindex"`
	Meta string `datastore:"Meta,noindex"`
	Text string `datastore:"Text,noindex"`
}

func main(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "bad method", 405)
		return
	}
	if r.URL.Path != "/" {
		http.Error(w, "not found", 404)
		return
	}

	http.ServeFile(w, r, "index.html")
}

func auth(w http.ResponseWriter, r *http.Request) {
	ctx := appengine.NewContext(r)
	u := user.Current(ctx)
	name := "GUEST"
	if u != nil {
		name = u.String()
	}
	fmt.Fprintf(w, "<html>\nYou are logged in as %s.\n\n<a href=\"/\">Main page</a>.\n", name)
}

func status(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "bad method", 405)
		return
	}
	ctx := appengine.NewContext(r)
	w.Header().Set("Content-Type", "application/json")
	u := user.Current(ctx)
	name := "GUEST"
	if u != nil {
		name = u.String()
	}
	w.Write([]byte(`{"username": "` + name + `", "space": {"recipe": "all"}}`))
}

func tiddlerList(w http.ResponseWriter, r *http.Request) {
	ctx := appengine.NewContext(r)
	q := datastore.NewQuery("Tiddler")
	// Only need Meta, but get no results if we do this.
	if false {
		q = q.Project("Meta")
	}
	it := q.Run(ctx)
	var buf bytes.Buffer
	sep := ""
	buf.WriteString("[")
	for {
		var t Tiddler
		_, err := it.Next(&t)
		if err != nil {
			if err == datastore.Done {
				break
			}
			println("ERR", err.Error())
			http.Error(w, err.Error(), 500)
			return
		}
		if len(t.Meta) == 0 {
			continue
		}
		meta := t.Meta

		// Tiddlers containing macros don't take effect until
		// they are loaded. Force them to be loaded by including
		// their bodies in the skinny tiddler list.
		// Might need to expand this to other kinds of tiddlers
		// in the future as we discover them.
		if strings.Contains(meta, `"$:/tags/Macro"`) {
			var js map[string]interface{}
			err := json.Unmarshal([]byte(meta), &js)
			if err != nil {
				continue
			}
			js["text"] = string(t.Text)
			data, err := json.Marshal(js)
			if err != nil {
				continue
			}
			meta = string(data)
		}

		buf.WriteString(sep)
		sep = ","
		buf.WriteString(meta)
	}
	buf.WriteString("]")
	w.Header().Set("Content-Type", "application/json")
	w.Write(buf.Bytes())
}

func tiddler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		getTiddler(w, r)
	case "PUT":
		putTiddler(w, r)
	default:
		http.Error(w, "bad method", 405)
	}
}

func getTiddler(w http.ResponseWriter, r *http.Request) {
	ctx := appengine.NewContext(r)
	title := strings.TrimPrefix(r.URL.Path, "/recipes/all/tiddlers/")
	key := datastore.NewKey(ctx, "Tiddler", title, 0, nil)
	var t Tiddler
	if err := datastore.Get(ctx, key, &t); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	var js map[string]interface{}
	err := json.Unmarshal([]byte(t.Meta), &js)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	js["text"] = string(t.Text)
	data, err := json.Marshal(js)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func putTiddler(w http.ResponseWriter, r *http.Request) {
	if !mustBeAdmin(w, r) {
		return
	}
	ctx := appengine.NewContext(r)
	title := strings.TrimPrefix(r.URL.Path, "/recipes/all/tiddlers/")
	key := datastore.NewKey(ctx, "Tiddler", title, 0, nil)
	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "cannot read data", 400)
		return
	}
	var js map[string]interface{}
	err = json.Unmarshal(data, &js)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	js["bag"] = "bag"

	rev := 1
	var old Tiddler
	if err := datastore.Get(ctx, key, &old); err == nil {
		rev = old.Rev + 1
	}
	js["revision"] = rev

	var t Tiddler
	text, ok := js["text"].(string)
	if ok {
		t.Text = text
	}
	delete(js, "text")
	t.Rev = rev
	meta, err := json.Marshal(js)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	t.Meta = string(meta)
	_, err = datastore.Put(ctx, key, &t)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	if WriteTiddlerHistory {
		key2 := datastore.NewKey(ctx, "TiddlerHistory", title+"#"+fmt.Sprint(t.Rev), 0, nil)
		if _, err := datastore.Put(ctx, key2, &t); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}

	etag := fmt.Sprintf("\"bag/%s/%d:%x\"", url.QueryEscape(title), rev, md5.Sum(data))
	w.Header().Set("Etag", etag)
}

func deleteTiddler(w http.ResponseWriter, r *http.Request) {
	if !mustBeAdmin(w, r) {
		return
	}
	ctx := appengine.NewContext(r)
	if r.Method != "DELETE" {
		http.Error(w, "bad method", 405)
		return
	}
	title := strings.TrimPrefix(r.URL.Path, "/bags/bag/tiddlers/")
	key := datastore.NewKey(ctx, "Tiddler", title, 0, nil)
	var t Tiddler
	if err := datastore.Get(ctx, key, &t); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if WriteTiddlerHistory {
		t.Rev++
		t.Meta = ""
		t.Text = ""
		if _, err := datastore.Put(ctx, key, &t); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		key2 := datastore.NewKey(ctx, "TiddlerHistory", title+"#"+fmt.Sprint(t.Rev), 0, nil)
		if _, err := datastore.Put(ctx, key2, &t); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	} else {
		if err := datastore.Delete(ctx, key); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
}

// search is a very simple hack at present, just exact match on the text in the path after "/search/".
// TODO path unescape and pattern matching + integration into TiddlyWiki...
func search(w http.ResponseWriter, r *http.Request) {
	url, err := url.Parse(r.RequestURI)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	term := strings.TrimPrefix(url.Path, "/search/")
	ctx := appengine.NewContext(r)
	q := datastore.NewQuery("Tiddler")
	it := q.Run(ctx)
	var buf bytes.Buffer
	sep := ""
	buf.WriteString("[")
	for {
		var t Tiddler
		_, err := it.Next(&t)
		if err != nil {
			if err == datastore.Done {
				break
			}
			println("ERR", err.Error())
			http.Error(w, err.Error(), 500)
			return
		}
		if len(t.Text) == 0 || len(t.Meta) == 0 {
			continue
		}
		if strings.Contains(t.Text, term) {
			var js map[string]interface{}
			err := json.Unmarshal([]byte(t.Meta), &js)
			if err != nil {
				continue
			}
			if title, ok := js["title"].(string); ok {
				title = strings.Replace(title, `"`, `\"`, -1) // escape double quotes
				buf.WriteString(sep)
				sep = ","
				buf.WriteString(`"` + title + `"`)
			}
		}
	}
	buf.WriteString("]")
	w.Header().Set("Content-Type", "application/json")
	w.Write(buf.Bytes())
}
