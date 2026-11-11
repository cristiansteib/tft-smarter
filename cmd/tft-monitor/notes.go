package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cristiansteib/tft-smarter/internal/notify"
)

// inbox keeps the notices sent with tft-notify (or the desktop app): the active ones are shown full screen, most
// urgent first, until they expire or are tapped away; the closed ones are kept for a while so senders can wait on them.
type inbox struct {
	mu     sync.Mutex
	active []*notify.Notice
	recent []*notify.Notice // closed, newest first
}

var notes = &inbox{}

const keepRecent = 30

func newID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// add shows a message. A message with the id of a notice already there (active or recently closed) updates it and
// shows it again.
func (b *inbox) add(m notify.Message, now time.Time) (notify.Notice, error) {
	ttl, err := m.Normalize()
	if err != nil {
		return notify.Notice{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire(now)
	n := &notify.Notice{State: "active", Created: now}
	if m.ID == "" {
		m.ID = newID()
	} else if i := b.find(b.active, m.ID); i >= 0 {
		n.Created = b.active[i].Created
		b.active = append(b.active[:i], b.active[i+1:]...)
	} else if i := b.find(b.recent, m.ID); i >= 0 {
		b.recent = append(b.recent[:i], b.recent[i+1:]...)
	}
	n.Message, n.Updated = m, now
	if ttl > 0 {
		e := now.Add(ttl)
		n.Expires = &e
	}
	b.active = append(b.active, n)
	for len(b.active) > notify.MaxActive { // too many: the least urgent, oldest one goes
		b.sort()
		b.close(b.active[len(b.active)-1], "expired", "", now)
	}
	b.sort()
	return *n, nil
}

func (b *inbox) find(list []*notify.Notice, id string) int {
	for i, n := range list {
		if n.ID == id {
			return i
		}
	}
	return -1
}

func prio(n *notify.Notice) int { _, p, _ := notify.LevelOf(n.Level); return p }

// sort: most urgent first, then newest first.
func (b *inbox) sort() {
	sort.SliceStable(b.active, func(i, j int) bool {
		if pi, pj := prio(b.active[i]), prio(b.active[j]); pi != pj {
			return pi > pj
		}
		return b.active[i].Updated.After(b.active[j].Updated)
	})
}

func (b *inbox) close(n *notify.Notice, state, by string, now time.Time) {
	if i := b.find(b.active, n.ID); i >= 0 {
		b.active = append(b.active[:i], b.active[i+1:]...)
	}
	t := now
	n.State, n.ClosedAt, n.DismissedBy = state, &t, by
	b.recent = append([]*notify.Notice{n}, b.recent...)
	if len(b.recent) > keepRecent {
		b.recent = b.recent[:keepRecent]
	}
}

func (b *inbox) expire(now time.Time) {
	for _, n := range append([]*notify.Notice(nil), b.active...) {
		if n.Expires != nil && !now.Before(*n.Expires) {
			b.close(n, "expired", "", now)
		}
	}
}

// dismiss closes one notice (or all of them) and returns the ids it closed.
func (b *inbox) dismiss(id string, all bool, by string, now time.Time) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var ids []string
	for _, n := range append([]*notify.Notice(nil), b.active...) {
		if all || n.ID == id {
			b.close(n, "dismissed", by, now)
			ids = append(ids, n.ID)
		}
	}
	return ids
}

// get finds a notice, active or recently closed.
func (b *inbox) get(id string, now time.Time) (notify.Notice, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire(now)
	for _, l := range [][]*notify.Notice{b.active, b.recent} {
		if i := b.find(l, id); i >= 0 {
			return *l[i], true
		}
	}
	return notify.Notice{}, false
}

// list returns copies of the active notices (screen order) and the recently closed ones.
func (b *inbox) list(now time.Time) (active, recent []notify.Notice) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire(now)
	active, recent = []notify.Notice{}, []notify.Notice{}
	for _, n := range b.active {
		active = append(active, *n)
	}
	for _, n := range b.recent {
		recent = append(recent, *n)
	}
	return
}

// ---- layout ----

// Glyph advances (x pixels) of the board's fonts for ' '..'~', so text is wrapped exactly where the board would cut it.
var (
	advBody = advances("6,8,8,13,13,21,16,5,8,8,9,14,7,8,6,7,13,13,13,13,13,13,13,13,13,13,6,6,14,14,14,13,24,16,16,17,17,15,14,18,17,7,13,16,14,20,18,19,16,19,17,16,15,17,15,22,16,16,15,7,7,7,11,13,6,13,13,12,13,13,7,13,13,5,6,12,5,19,13,13,13,13,8,12,7,13,12,17,11,11,12,8,6,8,12")
	advBold = advances("7,8,11,13,13,21,17,6,8,8,9,14,6,8,6,7,13,14,13,13,13,13,13,13,13,13,6,6,14,14,14,15,23,17,17,17,17,16,15,18,18,7,14,17,15,21,18,19,16,19,17,16,15,18,16,23,16,15,15,8,7,8,14,13,6,14,15,13,15,14,8,15,14,7,7,14,6,21,15,15,15,15,9,13,8,15,13,19,13,13,12,9,7,9,12")
	advBig  = advances("10,12,17,19,19,31,25,9,12,12,14,20,9,12,9,10,19,19,19,19,19,19,19,19,19,19,9,9,20,20,20,21,34,24,25,25,25,23,22,27,26,11,20,25,22,30,26,27,24,27,25,24,23,26,23,34,24,22,21,12,10,12,20,19,9,20,22,20,22,20,12,21,21,10,10,20,9,31,21,21,22,22,14,19,12,21,19,27,19,19,18,14,10,14,18")
)

func advances(csv string) (a [95]int) {
	for i, f := range strings.Split(csv, ",") {
		fmt.Sscan(f, &a[i])
	}
	return
}

func textW(adv *[95]int, s string) int {
	w := 0
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 32 && c <= 126 {
			w += adv[c-32]
		}
	}
	return w
}

// fit cuts s to at most w pixels, ending in "..." when it had to cut.
func fit(adv *[95]int, s string, w int) string {
	if textW(adv, s) <= w {
		return s
	}
	for len(s) > 0 && textW(adv, s+"...") > w {
		s = s[:len(s)-1]
	}
	return strings.TrimRight(s, " ") + "..."
}

// wrap breaks text into at most n lines of w pixels (on spaces; a word longer than a line is split), honouring
// newlines. The last line ends in "..." when the text did not fit.
func wrap(adv *[95]int, text string, w, n int) []string {
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		cur := ""
		for _, word := range strings.Fields(para) {
			for textW(adv, word) > w { // split a word that is wider than a line
				k := len(word)
				for k > 1 && textW(adv, word[:k]) > w {
					k--
				}
				if cur != "" {
					lines, cur = append(lines, cur), ""
				}
				lines, word = append(lines, word[:k]), word[k:]
			}
			if cur == "" {
				cur = word
			} else if textW(adv, cur+" "+word) <= w {
				cur += " " + word
			} else {
				lines, cur = append(lines, cur), word
			}
		}
		if cur != "" {
			lines = append(lines, cur)
		}
	}
	if len(lines) > n {
		lines = lines[:n]
		lines[n-1] = fit(adv, lines[n-1]+" ...", w)
	}
	return lines
}

// ascii folds what the board's fonts cannot draw: accents lose their mark, typographic quotes and dashes become
// plain ones, anything else unknown becomes '?'.
func ascii(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteByte(' ')
		case r == '\n' || (r >= 32 && r < 127):
			b.WriteRune(r)
		case r < 32:
		default:
			if f, ok := fold[r]; ok {
				b.WriteString(f)
			} else {
				b.WriteByte('?')
			}
		}
	}
	return b.String()
}

var fold = func() map[rune]string {
	m := map[rune]string{'‘': "'", '’': "'", '‚': "'", '“': `"`, '”': `"`, '„': `"`, '«': `"`, '»': `"`, '–': "-", '—': "-", '−': "-",
		'…': "...", '•': "*", '·': "-", '×': "x", '÷': "/", '°': "o", '€': "EUR", '£': "GBP", '©': "(c)", '®': "(r)", '™': "TM",
		'→': "->", '←': "<-", '⇒': "=>", '✓': "v", '✔': "v", '✗': "x", '✘': "x", '¡': "!", '¿': "?", ' ': " ", 'ß': "ss", 'æ': "ae", 'Æ': "AE", 'ø': "o", 'Ø': "O"}
	for _, g := range []string{"aàáâãäåā", "AÀÁÂÃÄÅĀ", "cçćč", "CÇĆČ", "eèéêëēě", "EÈÉÊËĒĚ", "iìíîïī", "IÌÍÎÏĪ", "nñń", "NÑŃ",
		"oòóôõöō", "OÒÓÔÕÖŌ", "uùúûüū", "UÙÚÛÜŪ", "yýÿ", "YÝŸ", "sšś", "SŠŚ", "zžź", "ZŽŹ"} {
		r := []rune(g)
		for _, x := range r[1:] {
			m[x] = string(r[0])
		}
	}
	return m
}()

const (
	noteTextW = 444 // label area of a notice row: x 22..466
	noteRows  = 7   // content rows 1..7; row 0 is the header, row 8 the footer
	pAccent   = 9   // the notice's accent (set with frameColor)
	pAccentLo = 10  // dimmed accent: the blinking header of "action"
)

// noteView picks the layout: the detail view when there is more to show than a title and a short line.
func noteView(m notify.Message) string {
	if m.View != "auto" && m.View != "" {
		return m.View
	}
	if m.Action != "" || m.Progress != nil || len(m.Fields) > 0 || len(m.Body) > 70 {
		return "detail"
	}
	return "simple"
}

// noteCells lays out a notice: row 0 is the header (drawn on the accent), 1..7 the content, 8 the footer.
func noteCells(n notify.Notice, idx, count int, now time.Time) []cell {
	lv, _, _ := notify.LevelOf(n.Level)
	head := byte(pAccent)
	if n.Level == "action" && now.UnixMilli()/700%2 == 1 { // blink
		head = pAccentLo
	}
	title, body, action := ascii(n.Title), ascii(n.Body), ascii(n.Action)
	var right []string
	if count > 1 {
		right = append(right, fmt.Sprintf("%d/%d", idx+1, count))
	}
	detail := noteView(n.Message) == "detail"
	if n.Source != "" {
		right = append(right, ascii(n.Source))
	}
	right = append(right, n.Updated.Local().Format("15:04"))
	rv := fit(&advBold, strings.Join(right, "  "), 200)
	cs := []cell{{0, head | sBig, 0, fit(&advBig, lv.Label, noteTextW-textW(&advBold, rv)-16) + "\t" + rv}}
	var rows []cell
	line := func(style byte, pct int, text string) { rows = append(rows, cell{0, style, byte(pct), text}) }
	if !detail {
		tl := wrap(&advBig, title, noteTextW, 3)
		bl := []string{}
		if body != "" {
			bl = wrap(&advBody, body, noteTextW, noteRows-len(tl)-1)
		}
		for _, t := range tl {
			line(pWhite|sBig|sBright, 0, t)
		}
		if len(bl) > 0 {
			line(pWhite, 0, "") // a quiet gap between title and body
		}
		for _, t := range bl {
			line(pWhite|sBright, 0, t)
		}
		pad := (noteRows - len(rows)) / 2 // centre the block
		for i := range rows {
			rows[i].slot = byte(1 + pad + i)
		}
	} else {
		tl := wrap(&advBig, title, noteTextW, 2)
		al := []string{}
		if action != "" {
			al = wrap(&advBody, "> "+action, noteTextW, 2)
		}
		pr := b2i(n.Progress != nil)
		fl := len(n.Fields)
		budget := noteRows - len(tl) - len(al) - pr - fl
		for budget < 1 && body != "" && fl > 0 { // keep at least a line of body: drop fields from the end
			fl, budget = fl-1, budget+1
		}
		if budget < 0 {
			fl = max(0, fl+budget)
		}
		for _, t := range tl {
			line(pWhite|sBig|sBright, 0, t)
		}
		if body != "" && budget > 0 {
			for _, t := range wrap(&advBody, body, noteTextW, budget) {
				line(pWhite|sBright, 0, t)
			}
		}
		for _, t := range al {
			line(pAccent|sBright, 0, t)
		}
		if n.Progress != nil {
			line(pAccent|sBar, *n.Progress, fmt.Sprintf("Progress\t%d%%", *n.Progress))
		}
		for _, f := range n.Fields[:fl] {
			l := fit(&advBody, ascii(f.Label), 170)
			line(pWhite|sBright, 0, l+"\t"+fit(&advBold, ascii(f.Value), noteTextW-textW(&advBody, l)-16))
		}
		for i := range rows {
			rows[i].slot = byte(1 + i)
		}
	}
	cs = append(cs, rows...)
	hint := "Tap to dismiss"
	if count > 1 {
		hint = "Tap: dismiss, swipe: next"
	}
	when, wc := "until you tap", byte(pGrey)
	if n.Level == "action" {
		when, wc = "waiting for you", pAccent
	}
	if n.Expires != nil {
		when = shortLeft(n.Expires.Sub(now)) + " left"
	}
	return append(cs, cell{8, wc, 0, hint + "\t" + when})
}

// shortLeft: "45s", "4m", "2h".
func shortLeft(d time.Duration) string {
	s := int(d.Seconds() + 0.999)
	switch {
	case s >= 2*3600:
		return fmt.Sprintf("%dh", s/3600)
	case s >= 120:
		return fmt.Sprintf("%dm", s/60)
	}
	return fmt.Sprintf("%ds", max(s, 0))
}
