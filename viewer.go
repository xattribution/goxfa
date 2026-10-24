package xfa

import (
	_ "embed"
	"encoding/json"
	"html"
	"strings"
)

//go:embed viewer/viewer.js
var viewerJS string

//go:embed viewer/viewer.css
var viewerCSS string

// ViewerOptions shape the page ViewerHTML writes.
type ViewerOptions struct {
	// Title heads the page.
	Title string
	// Endpoint, when set, is where the page sends changes: it posts
	// {"SOM": path, "Value": value} to Endpoint+"/set" and expects the
	// laid-out Doc back as JSON, and posts to Endpoint+"/save" when Save is
	// pressed. Without one, the page is a standalone copy of the form that
	// keeps what is typed only until it is closed.
	Endpoint string
	// SignatureText labels signature fields. The default is "Sign in Acrobat".
	SignatureText string
	// NoSave leaves out the Save button.
	NoSave bool
}

// ViewerJS and ViewerCSS are the viewer's script and style sheet, for
// pages that draw forms themselves: the script defines
// XFAViewer.render(element, doc, {onChange(som, value), signatureText}).
func ViewerJS() string  { return viewerJS }
func ViewerCSS() string { return viewerCSS }

// ViewerHTML writes a self-contained page that draws the laid-out form with
// inputs on its fields.
func ViewerHTML(doc *Doc, o ViewerOptions) []byte {
	data, _ := json.Marshal(doc) // escapes <, > and &, so it is safe inside <script>
	cfg, _ := json.Marshal(o)
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\"><head><meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<title>" + html.EscapeString(o.Title) + "</title>\n<style>\n")
	b.WriteString(viewerCSS)
	b.WriteString("</style></head><body>\n<div class=\"xfa-bar\"><h1>")
	b.WriteString(html.EscapeString(o.Title))
	b.WriteString("</h1><span class=\"xfa-status\" id=\"xfa-status\"></span>")
	if o.Endpoint != "" && !o.NoSave {
		b.WriteString("<button class=\"primary\" id=\"xfa-save\" type=\"button\">Save</button>")
	}
	b.WriteString("<button id=\"xfa-print\" type=\"button\">Print</button></div>\n<div id=\"xfa-doc\"></div>\n<script>\n")
	b.WriteString(viewerJS)
	b.WriteString("\n</script>\n<script>\n(function(){\nconst cfg = ")
	b.Write(cfg)
	b.WriteString(";\nlet doc = ")
	b.Write(data)
	b.WriteString(";\n")
	b.WriteString(viewerBoot)
	b.WriteString("\n})();\n</script>\n</body></html>\n")
	return []byte(b.String())
}

const viewerBoot = `const root = document.getElementById("xfa-doc");
const status = document.getElementById("xfa-status");
const say = (t) => { status.textContent = t; };
let busy = Promise.resolve();
const opts = {
  signatureText: cfg.SignatureText,
  onChange(som, value) {
    if (!cfg.Endpoint) { say("Changes stay in this page"); return; }
    busy = busy.then(async () => {
      say("Updating");
      try {
        const r = await fetch(cfg.Endpoint + "/set", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ SOM: som, Value: value }) });
        if (!r.ok) { say((await r.text()).trim()); return; }
        doc = await r.json();
        const y = window.scrollY;
        XFAViewer.render(root, doc, opts);
        window.scrollTo(0, y);
        say("Not saved");
      } catch (e) { say("Couldn't reach the program: " + e.message); }
    });
  },
};
XFAViewer.render(root, doc, opts);
document.getElementById("xfa-print").onclick = () => window.print();
const save = document.getElementById("xfa-save");
if (save) save.onclick = () => busy.then(async () => {
  say("Saving");
  const r = await fetch(cfg.Endpoint + "/save", { method: "POST" });
  say((await r.text()).trim());
});`
