package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// storageState is Playwright's storage state file: cookies and per-origin
// localStorage.
type storageState struct {
	Cookies []struct {
		Name     string  `json:"name"`
		Value    string  `json:"value"`
		Domain   string  `json:"domain"`
		Path     string  `json:"path"`
		Expires  float64 `json:"expires"`
		HTTPOnly bool    `json:"httpOnly"`
		Secure   bool    `json:"secure"`
		SameSite string  `json:"sameSite"`
	} `json:"cookies"`
	Origins []struct {
		Origin       string `json:"origin"`
		LocalStorage []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"localStorage"`
	} `json:"origins"`
}

// loadStorageState sets the cookies and, for the target's origin, the
// localStorage entries (by visiting the origin once).
func (s *session) loadStorageState(ctx context.Context, path string, target *url.URL) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var st storageState
	if err := json.Unmarshal(b, &st); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var cookies []*network.CookieParam
	for _, c := range st.Cookies {
		cp := &network.CookieParam{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, HTTPOnly: c.HTTPOnly, Secure: c.Secure}
		if c.Expires > 0 {
			t := cdp.TimeSinceEpoch(epoch(c.Expires))
			cp.Expires = &t
		}
		if c.Path == "" {
			cp.Path = "/"
		}
		cookies = append(cookies, cp)
	}
	if len(cookies) > 0 {
		if err := s.do(ctx, network.SetCookies(cookies)); err != nil {
			return fmt.Errorf("cookies: %w", err)
		}
	}
	origin := target.Scheme + "://" + target.Host
	for _, o := range st.Origins {
		if strings.TrimSuffix(o.Origin, "/") != origin || len(o.LocalStorage) == 0 {
			continue
		}
		items, _ := json.Marshal(o.LocalStorage)
		js := fmt.Sprintf(`(() => { for (const it of %s) localStorage.setItem(it.name, it.value); return true; })()`, items)
		if err := s.do(ctx, chromedp.Navigate(origin+"/"), chromedp.WaitReady("body"), chromedp.Evaluate(js, nil)); err != nil {
			return fmt.Errorf("localStorage for %s: %w", origin, err)
		}
	}
	s.event("storage_state", path)
	return nil
}
