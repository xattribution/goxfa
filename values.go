package xfa

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Values: what a field holds (its raw value, as stored in the data), what
// it shows (the display picture applied), and writing new values.

// initialValue is a field's value from its data node, or its template
// default when it has none.
func (f *Form) initialValue(in *Inst) string {
	if in.dnode != nil {
		v := dataText(in.dnode)
		if ui, _ := uiKind(in.T); in.Kind == "exclGroup" || ui == "checkButton" || ui == "choiceList" || ui == "dateTimeEdit" || ui == "numericEdit" {
			v = strings.TrimSpace(v)
		}
		return v
	}
	if in.Kind == "exclGroup" {
		// no data: the member whose own default is its on value
		for _, m := range in.T.Children("field") {
			on, _, _ := checkStates(m)
			if v := strings.TrimSpace(templateValue(m)); v != "" && v == on {
				return on
			}
		}
		return strings.TrimSpace(templateValue(in.T))
	}
	return templateValue(in.T)
}

// dataText is a data value's text. Rich text values (an xhtml body inside)
// give their plain text.
func dataText(d *Node) string {
	return d.TextContent()
}

// templateValue is the text of a container's <value> element.
func templateValue(t *Node) string {
	v := t.Child("value")
	if v == nil {
		return ""
	}
	for _, k := range v.Elems() {
		switch k.Local {
		case "image", "line", "rectangle", "arc":
			return ""
		}
		return k.TextContent()
	}
	return ""
}

// Field describes one field a person can fill.
type Field struct {
	SOM      string   `json:"som"`
	Name     string   `json:"name"`
	Kind     string   `json:"kind"` // text, multiline, check, radio, choice, date, number, password, signature, image
	Value    string   `json:"value"`
	Display  string   `json:"display,omitempty"`
	Options  []string `json:"options,omitempty"` // choices: what's shown
	Saves    []string `json:"saves,omitempty"`   // choices: what's stored, when it differs
	On       string   `json:"on,omitempty"`      // checkbox: its "checked" value
	Label    string   `json:"label,omitempty"`
	Tip      string   `json:"tip,omitempty"`
	Required bool     `json:"required,omitempty"`
	ReadOnly bool     `json:"readOnly,omitempty"`
	MaxChars int      `json:"maxChars,omitempty"`
	Comb     int      `json:"comb,omitempty"`
	Hidden   bool     `json:"hidden,omitempty"`
	Bound    bool     `json:"bound"` // the value lives in the form's data
}

// Fields lists the fields of the merged form in document order.
func (f *Form) Fields() []Field {
	var out []Field
	for _, in := range f.order {
		if in.match == "member" {
			continue
		}
		out = append(out, f.describe(in))
	}
	return out
}

func (f *Form) describe(in *Inst) Field {
	fd := Field{SOM: in.SOM, Name: in.Name, Value: in.raw, Bound: in.dnode != nil || in.dpath != nil}
	t := in.T
	fd.Label = strings.TrimSpace(captionText(t))
	if a := t.Child("assist"); a != nil {
		fd.Tip = strings.TrimSpace(a.Child("toolTip").TextContent())
		if fd.Tip == "" {
			fd.Tip = strings.TrimSpace(a.Child("speak").TextContent())
		}
	}
	if v := t.Child("validate"); v != nil && v.Attr("nullTest") == "error" {
		fd.Required = true
	}
	switch t.Attr("access") {
	case "readOnly", "protected", "nonInteractive":
		fd.ReadOnly = true
	}
	fd.Hidden = hiddenUp(in)
	if in.Kind == "exclGroup" {
		fd.Kind = "radio"
		for _, m := range in.Kids {
			if m.Kind != "field" {
				continue
			}
			on, _, _ := checkStates(m.T)
			lbl := strings.TrimSpace(captionText(m.T))
			if lbl == "" {
				lbl = m.Name
			}
			fd.Options = append(fd.Options, lbl)
			fd.Saves = append(fd.Saves, on)
		}
		return fd
	}
	ui, un := uiKind(t)
	switch ui {
	case "checkButton":
		fd.Kind = "check"
		fd.On, _, _ = checkStates(t)
	case "choiceList":
		fd.Kind = "choice"
		fd.Options, fd.Saves = choiceItems(t)
	case "dateTimeEdit":
		fd.Kind = "date"
	case "numericEdit":
		fd.Kind = "number"
	case "passwordEdit":
		fd.Kind = "password"
	case "signature":
		fd.Kind = "signature"
	case "imageEdit":
		fd.Kind = "image"
	case "button", "barcode":
		fd.Kind = ui
	default:
		fd.Kind = "text"
		if un != nil && un.Attr("multiLine") == "1" {
			fd.Kind = "multiline"
		}
		if c := un.Child("comb"); c != nil {
			fd.Comb = atoiDef(c.Attr("numberOfCells"), 0)
		}
	}
	if v := t.Child("value"); v != nil {
		if tx := v.Child("text"); tx != nil {
			fd.MaxChars = atoiDef(tx.Attr("maxChars"), 0)
		}
	}
	fd.Display = f.display(in)
	return fd
}

func hiddenUp(in *Inst) bool {
	for p := in; p != nil; p = p.Parent {
		if p.Presence == "hidden" || p.Presence == "inactive" {
			return true
		}
	}
	return false
}

// captionText is a container's caption as plain text.
func captionText(t *Node) string {
	c := t.Child("caption")
	if c == nil || presence(c) == "hidden" {
		return ""
	}
	v := c.Child("value")
	if v == nil {
		return ""
	}
	return strings.Join(strings.Fields(v.TextContent()), " ")
}

// checkStates are a checkbox's on, off and neutral values from its items:
// "1", "0" and "" when it has none.
func checkStates(t *Node) (on, off, neutral string) {
	on, off = "1", "0"
	it := t.Child("items")
	if it == nil {
		return
	}
	els := it.Elems()
	if len(els) > 0 {
		on = strings.TrimSpace(els[0].TextContent())
	}
	if len(els) > 1 {
		off = strings.TrimSpace(els[1].TextContent())
	}
	if len(els) > 2 {
		neutral = strings.TrimSpace(els[2].TextContent())
	}
	return
}

// choiceItems are a choice list's shown items and, when it has a second
// <items save="1"> list, the stored values.
func choiceItems(t *Node) (show, save []string) {
	var lists [][]string
	saveIdx := -1
	for i, it := range t.Children("items") {
		var l []string
		for _, e := range it.Elems() {
			l = append(l, e.TextContent())
		}
		if it.Attr("save") == "1" {
			saveIdx = i
		}
		lists = append(lists, l)
	}
	switch len(lists) {
	case 0:
		return nil, nil
	case 1:
		return lists[0], nil
	}
	if saveIdx == 0 {
		return lists[1], lists[0]
	}
	return lists[0], lists[1]
}

// display is what a field shows: its value through its display picture,
// a choice's shown text for its stored value, or a checkbox's state.
func (f *Form) display(in *Inst) string {
	t := in.T
	raw := in.raw
	ui, _ := uiKind(t)
	switch ui {
	case "checkButton":
		on, _, _ := checkStates(t)
		if raw == on {
			return "checked"
		}
		return ""
	case "choiceList":
		show, save := choiceItems(t)
		if save != nil {
			for i, s := range save {
				if s == raw && i < len(show) {
					return show[i]
				}
			}
		}
		return raw
	}
	if raw == "" {
		return ""
	}
	if pic := displayPicture(t); pic != "" {
		if s, ok := formatPicture(pic, raw, ui); ok {
			return s
		}
	}
	return raw
}

// displayPicture is the field's display pattern from <format><picture>.
func displayPicture(t *Node) string {
	if fm := t.Child("format"); fm != nil {
		return strings.TrimSpace(fm.Child("picture").TextContent())
	}
	return ""
}

// bindPicture is how the value is stored, from <bind><picture>.
func bindPicture(t *Node) string {
	if b := t.Child("bind"); b != nil {
		return strings.TrimSpace(b.Child("picture").TextContent())
	}
	return ""
}

// Value is a field's stored value by SOM path.
func (f *Form) Value(som string) (string, bool) {
	in := f.bySOM[som]
	if in == nil || (in.Kind != "field" && in.Kind != "exclGroup") {
		return "", false
	}
	if in.match == "member" {
		return in.Parent.raw, true
	}
	return in.raw, true
}

// Values are every field's stored value by SOM path.
func (f *Form) Values() map[string]string {
	out := map[string]string{}
	for _, in := range f.order {
		if in.match != "member" {
			out[in.SOM] = in.raw
		}
	}
	return out
}

// ErrNoField means no field has that SOM path.
var ErrNoField = errors.New("xfa: no field has that SOM path")

// Set stores a field's value: the data value, as the field would store it.
// For a checkbox, the value is its on or off value (or "1"/"0", "true" and
// "false", which are mapped); for an exclusion group, a member's on value or
// its label; for a choice list, a stored value or a shown item. Dates take
// YYYY-MM-DD and are stored through the field's bind picture.
func (f *Form) Set(som, value string) error {
	in := f.bySOM[som]
	if in == nil || (in.Kind != "field" && in.Kind != "exclGroup") {
		return fmt.Errorf("%w: %s", ErrNoField, som)
	}
	if in.match == "member" {
		// a radio button: setting it on sets its group
		on, off, _ := checkStates(in.T)
		g := in.Parent
		switch {
		case value == on || isTrue(value):
			return f.Set(g.SOM, on)
		case value == off || isFalse(value) || value == "":
			if g.raw == on {
				return f.Set(g.SOM, "")
			}
			return nil
		}
		return fmt.Errorf("xfa: %s takes %q or %q", som, on, off)
	}
	raw, err := f.stored(in, value)
	if err != nil {
		return err
	}
	if raw == in.raw && (in.dnode != nil || in.match == "none") {
		return nil
	}
	if in.match == "none" {
		// unbound: the value lives in the template, which this package doesn't rewrite
		return fmt.Errorf("xfa: %s isn't bound to the form's data", som)
	}
	d := f.makeData(in)
	if d == nil {
		return fmt.Errorf("xfa: %s has no place in the form's data", som)
	}
	d.SetText(raw)
	d.DelAttr("nil")
	in.raw = raw
	for _, o := range f.order {
		if o != in && o.dnode == d {
			o.raw = raw // fields sharing the value (global, or bound to the same node)
		}
	}
	f.dirty = true
	return nil
}

func isTrue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on", "x", "checked":
		return true
	}
	return false
}

func isFalse(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}

// stored turns a value a person gives into what the field stores.
func (f *Form) stored(in *Inst, v string) (string, error) {
	t := in.T
	if in.Kind == "exclGroup" {
		if v == "" {
			return "", nil
		}
		var ons []string
		for _, m := range in.Kids {
			on, _, _ := checkStates(m.T)
			if v == on || strings.EqualFold(v, strings.TrimSpace(captionText(m.T))) || v == m.Name {
				return on, nil
			}
			ons = append(ons, on)
		}
		return "", fmt.Errorf("xfa: %s takes one of %s", in.SOM, strings.Join(ons, ", "))
	}
	ui, _ := uiKind(t)
	switch ui {
	case "checkButton":
		on, off, neutral := checkStates(t)
		switch {
		case v == on, v == off, v == neutral && neutral != "":
			return v, nil
		case isTrue(v):
			return on, nil
		case isFalse(v), v == "":
			return off, nil
		}
		return "", fmt.Errorf("xfa: %s takes %q or %q", in.SOM, on, off)
	case "choiceList":
		show, save := choiceItems(t)
		if save != nil {
			for i, s := range show {
				if s == v && i < len(save) {
					return save[i], nil
				}
			}
		}
		return v, nil
	case "dateTimeEdit":
		if v == "" {
			return "", nil
		}
		// typed the way it's shown, or in a stored form: kept the way the
		// form stores it
		d, ok := parseDate(v)
		for _, pic := range []string{displayPicture(t), editPicture(t), bindPicture(t)} {
			if !ok && pic != "" {
				d, ok = parsePicture(pic, v)
			}
		}
		if !ok {
			return v, nil
		}
		iso := d.Format("2006-01-02")
		if pic := bindPicture(t); pic != "" {
			if s, ok := formatPicture(pic, iso, ui); ok {
				return s, nil
			}
		}
		return iso, nil
	case "signature", "button":
		return "", fmt.Errorf("xfa: %s is a %s, not a value", in.SOM, ui)
	}
	if t.Child("value") != nil {
		if tx := t.Child("value").Child("text"); tx != nil {
			if n := atoiDef(tx.Attr("maxChars"), 0); n > 0 && len([]rune(v)) > n {
				return "", fmt.Errorf("xfa: %s takes at most %d characters", in.SOM, n)
			}
		}
	}
	return v, nil
}

// SetValues sets several values; it stops at the first error.
func (f *Form) SetValues(vals map[string]string) error {
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := f.Set(k, vals[k]); err != nil {
			return err
		}
	}
	return nil
}

// editPicture is the picture a field is typed in (ui/picture), if any.
func editPicture(t *Node) string {
	if u := t.Child("ui"); u != nil {
		return strings.TrimSpace(u.Child("picture").TextContent())
	}
	return ""
}
