// Command webapp is Mochi Shoes, a tiny web shop with a login form, a product
// search and a cart, that tamesu's browser example tests. It keeps its state
// in memory per session cookie and needs nothing but the standard library.
//
//	go run . --addr 127.0.0.1:18090
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

type product struct {
	ID    string
	Name  string
	Price string
}

var catalog = []product{
	{"laptop-sleeve", "Laptop sleeve sneakers", "89.00"},
	{"trail-runner", "Trail runner", "129.00"},
	{"city-loafer", "City loafer", "99.00"},
	{"kids-velcro", "Kids velcro", "39.00"},
}

type session struct {
	User string
	Cart []product
}

type app struct {
	mu       sync.Mutex
	sessions map[string]*session
	tmpl     *template.Template
}

func main() {
	addr := flag.String("addr", envOr("WEBAPP_ADDR", "127.0.0.1:18090"), "listen address")
	flag.Parse()
	a := &app{sessions: map[string]*session{}, tmpl: template.Must(template.New("page").Parse(page))}
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.home)
	mux.HandleFunc("/login", a.login)
	mux.HandleFunc("/logout", a.logout)
	mux.HandleFunc("/search", a.search)
	mux.HandleFunc("/cart", a.cart)
	mux.HandleFunc("/cart/add", a.add)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("webapp: Mochi Shoes on http://%s", *addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

type view struct {
	Title    string
	User     string
	Query    string
	Products []product
	Cart     []product
	Message  string
	Error    string
	Count    int
}

func (a *app) session(w http.ResponseWriter, r *http.Request) *session {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, err := r.Cookie("sid"); err == nil {
		if s, ok := a.sessions[c.Value]; ok {
			return s
		}
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	s := &session{}
	a.sessions[id] = s
	http.SetCookie(w, &http.Cookie{Name: "sid", Value: id, Path: "/", HttpOnly: true})
	return s
}

func (a *app) render(w http.ResponseWriter, v view, s *session) {
	v.User = s.User
	v.Cart = s.Cart
	v.Count = len(s.Cart)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.tmpl.Execute(w, v); err != nil {
		log.Print(err)
	}
}

func (a *app) home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s := a.session(w, r)
	a.render(w, view{Title: "Mochi Shoes", Products: catalog}, s)
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	s := a.session(w, r)
	if r.Method != http.MethodPost {
		a.render(w, view{Title: "Log in"}, s)
		return
	}
	user, pass := strings.TrimSpace(r.FormValue("user")), r.FormValue("pass")
	if user == "" || pass != "correct-horse" {
		a.render(w, view{Title: "Log in", Error: "Wrong username or password."}, s)
		return
	}
	s.User = user
	http.Redirect(w, r, "/?welcome=1", http.StatusSeeOther)
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	s := a.session(w, r)
	s.User, s.Cart = "", nil
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *app) search(w http.ResponseWriter, r *http.Request) {
	s := a.session(w, r)
	q := strings.ToLower(strings.TrimSpace(r.FormValue("q")))
	var found []product
	for _, p := range catalog {
		if q == "" || strings.Contains(strings.ToLower(p.Name), q) || strings.Contains(p.ID, q) {
			found = append(found, p)
		}
	}
	msg := fmt.Sprintf("%d result(s) for %q", len(found), q)
	a.render(w, view{Title: "Search", Query: q, Products: found, Message: msg}, s)
}

func (a *app) cart(w http.ResponseWriter, r *http.Request) {
	s := a.session(w, r)
	a.render(w, view{Title: "Your cart"}, s)
}

func (a *app) add(w http.ResponseWriter, r *http.Request) {
	s := a.session(w, r)
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	id := r.FormValue("id")
	for _, p := range catalog {
		if p.ID == id {
			s.Cart = append(s.Cart, p)
		}
	}
	http.Redirect(w, r, "/cart", http.StatusSeeOther)
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

const page = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>{{.Title}} · Mochi Shoes</title>
<style>body{font-family:system-ui,sans-serif;max-width:720px;margin:2rem auto;padding:0 1rem;color:#1E2A5A}
nav a{margin-right:1rem}.badge{background:#E5482F;color:#fff;border-radius:999px;padding:0 .5rem}
.product{display:flex;justify-content:space-between;align-items:center;border-bottom:1px solid #ddd;padding:.5rem 0}
.error{color:#E5482F}button{background:#1E2A5A;color:#fff;border:0;padding:.4rem .8rem;border-radius:4px}</style></head>
<body>
<header><nav aria-label="Main"><a href="/">Home</a><a href="/search">Search</a><a href="/cart">Cart <span class="badge" id="cart-count">{{.Count}}</span></a>
{{if .User}}<span>Signed in as <strong>{{.User}}</strong></span> <a href="/logout">Log out</a>{{else}}<a href="/login">Log in</a>{{end}}</nav></header>
<main>
<h1>{{.Title}}</h1>
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
{{if .Message}}<p role="status">{{.Message}}</p>{{end}}
{{if eq .Title "Log in"}}
<form method="post" action="/login">
<p><label for="user">Username</label> <input id="user" name="user" autocomplete="username"></p>
<p><label for="pass">Password</label> <input id="pass" name="pass" type="password" autocomplete="current-password"></p>
<p><button type="submit">Log in</button></p>
</form>
{{else if eq .Title "Your cart"}}
{{if .Cart}}<ul>{{range .Cart}}<li>{{.Name}} — €{{.Price}}</li>{{end}}</ul><p>{{len .Cart}} item(s) in your cart.</p>{{else}}<p>Your cart is empty.</p>{{end}}
{{else}}
<form method="get" action="/search" role="search"><label for="q">Search products</label> <input id="q" name="q" value="{{.Query}}" placeholder="e.g. laptop"> <button type="submit">Search</button></form>
{{range .Products}}<div class="product"><span>{{.Name}} <small>€{{.Price}}</small></span>
<form method="post" action="/cart/add"><input type="hidden" name="id" value="{{.ID}}"><button type="submit" aria-label="Add {{.Name}} to cart">Add to cart</button></form></div>{{end}}
{{end}}
</main>
<footer><p>Mochi Shoes, a demo shop for tamesu.</p></footer>
</body></html>`
