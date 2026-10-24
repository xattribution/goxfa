package xfa

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// HandlerOptions shape the handler Form.Handler returns.
type HandlerOptions struct {
	// Title heads the page.
	Title string
	// Save receives the saved PDF (the original with the new values
	// appended) when Save is pressed. Without it the page has no Save
	// button.
	Save func(pdf []byte) error
	// SignatureText labels signature fields.
	SignatureText string
	// Sign, when set, makes unsigned signature fields sign when clicked.
	// It gives the options for one signature (the signer, its chain and
	// anything else); Field is filled in. The signed PDF goes to Save,
	// which Sign needs. Asking for a PIN is the signer's business.
	Sign func(field string) (SignOptions, error)
}

// Handler serves the form for filling in a browser:
//
//	GET  /      the page, laid out with the current values
//	POST /set   {"SOM": path, "Value": value}: sets a value and answers
//	            with the form laid out again (Doc as JSON)
//	POST /save  saves, and answers with the result of opts.Save
//
// The page calls ./set and ./save, so the handler can be mounted anywhere
// with http.StripPrefix, as long as the page's address ends in a slash. It does no authentication: the program that mounts it
// decides who may reach it. Calls are serialized, so one handler can serve
// one form to several tabs.
func (f *Form) Handler(o HandlerOptions) http.Handler {
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		doc, err := f.Layout()
		mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		page := ViewerHTML(doc, ViewerOptions{Title: o.Title, Endpoint: ".", SignatureText: o.SignatureText, NoSave: o.Save == nil, CanSign: o.Sign != nil && o.Save != nil})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(page)
	})
	mux.HandleFunc("POST /set", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ SOM, Value string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "The request couldn't be read.", http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if err := f.Set(req.SOM, req.Value); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		doc, err := f.Layout()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(doc)
	})
	mux.HandleFunc("POST /sign", func(w http.ResponseWriter, r *http.Request) {
		if o.Sign == nil || o.Save == nil {
			http.NotFound(w, r)
			return
		}
		var req struct{ SOM string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			http.Error(w, "The request couldn't be read.", http.StatusBadRequest)
			return
		}
		so, err := o.Sign(req.SOM)
		if err != nil {
			http.Error(w, "Not signed: "+err.Error(), http.StatusBadRequest)
			return
		}
		so.Field = req.SOM
		mu.Lock()
		defer mu.Unlock()
		signed, err := f.Sign(so)
		if err == nil {
			err = o.Save(signed)
		}
		if err != nil {
			http.Error(w, "Not signed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		doc, err := f.Layout()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(doc)
	})
	mux.HandleFunc("POST /save", func(w http.ResponseWriter, r *http.Request) {
		if o.Save == nil {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		b, err := f.Save()
		mu.Unlock()
		if err == nil {
			err = o.Save(b)
		}
		if err != nil {
			http.Error(w, strings.TrimSpace("Not saved: "+err.Error()), http.StatusInternalServerError)
			return
		}
		w.Write([]byte("Saved"))
	})
	return mux
}
