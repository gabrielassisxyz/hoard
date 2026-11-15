package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"os"
	"strings"

	"github.com/gabrielassisxyz/hoard/internal/store"
)

// runShowCommand writes one held page, made viewable from disk: a <base> element naming where
// the site served it from, so the page's relative stylesheets and images are asked of the
// site, and no integrity attributes, which a stylesheet from another origin fails without
// CORS. It reads the store and changes nothing in it.
func runShowCommand(args []string, stdout, stderr io.Writer) int {
	opts, exitCode, ok := parseShowArguments(args, stderr)
	if !ok {
		return exitCode
	}
	viewable, err := viewableCopyOfHeldPage(opts.storeDir, opts.address)
	if err != nil {
		fmt.Fprintf(stderr, "hoard show: %v\n", err)
		return exitCodeIncomplete
	}
	if opts.outPath == "" {
		_, err = stdout.Write(viewable)
	} else {
		err = os.WriteFile(opts.outPath, viewable, 0o644)
	}
	if err != nil {
		fmt.Fprintf(stderr, "hoard show: %v\n", err)
		return exitCodeIncomplete
	}
	return exitCodeComplete
}

func viewableCopyOfHeldPage(storeDir, address string) ([]byte, error) {
	st, err := store.OpenExisting(storeDir)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	page, found, err := st.Page(address)
	if err != nil {
		return nil, err
	}
	switch {
	case !found:
		return nil, fmt.Errorf("the store does not hold %s", address)
	case page.State != store.StateOK:
		return nil, fmt.Errorf("%s is %s, so there is no body to show", address, page.State)
	}
	body, err := st.ReadBody(page.SHA256)
	if err != nil {
		return nil, fmt.Errorf("read the body of %s: %w", address, err)
	}
	base := page.FinalAddress
	if base == "" {
		base = page.Address
	}
	return makeViewable(body, base), nil
}

type showOptions struct {
	storeDir, address, outPath string
}

func parseShowArguments(args []string, stderr io.Writer) (showOptions, int, bool) {
	var opts showOptions
	flags := flag.NewFlagSet("hoard show", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.storeDir, "store", "", "directory that holds the ledger and the raw bodies (required)")
	flags.StringVar(&opts.address, "address", "", "the page's address, as it was requested (required)")
	flags.StringVar(&opts.outPath, "out", "", "file to write the copy to, instead of standard output")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: hoard show --store <dir> --address <url> [--out <file>]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, exitCodeComplete, false
		}
		return opts, exitCodeUsage, false
	}
	problem := ""
	switch {
	case flags.NArg() > 0:
		problem = "unexpected arguments after the flags"
	case opts.storeDir == "":
		problem = "--store is required"
	case opts.address == "":
		problem = "--address is required"
	}
	if problem != "" {
		fmt.Fprintf(stderr, "hoard show: %s\n", problem)
		flags.Usage()
		return opts, exitCodeUsage, false
	}
	return opts, exitCodeComplete, true
}

// makeViewable works on the bytes and never on a parsed page: the body may be in any charset,
// and both changes are ASCII, so everything else comes out as it went in. A page that has a
// <base> of its own keeps it; ours comes first in the head, and a browser uses the first.
func makeViewable(body []byte, baseAddress string) []byte {
	insertAt, removals := scanForEdits(body)
	element := `<base href="` + html.EscapeString(baseAddress) + `">`
	out := make([]byte, 0, len(body)+len(element))
	copied := 0
	insertDone := false
	insert := func() {
		out = append(out, body[copied:insertAt]...)
		out = append(out, element...)
		copied = insertAt
		insertDone = true
	}
	for _, removal := range removals {
		if !insertDone && insertAt <= removal.start {
			insert()
		}
		out = append(out, body[copied:removal.start]...)
		copied = removal.end
	}
	if !insertDone {
		insert()
	}
	return append(out, body[copied:]...)
}

type byteSpan struct{ start, end int }

// scanForEdits finds where the <base> goes, which is just after the first opening head tag,
// or 0 when there is none, and the spans of every integrity attribute with the whitespace
// before it. Comments and the bodies of script and style elements are skipped, so a tag
// written inside one is not taken for a tag.
func scanForEdits(body []byte) (int, []byteSpan) {
	insertAt, headSeen := 0, false
	var removals []byteSpan
	for i := 0; i < len(body); {
		next := bytes.IndexByte(body[i:], '<')
		if next < 0 {
			break
		}
		i += next
		if bytes.HasPrefix(body[i:], []byte("<!--")) {
			i = endOfComment(body, i)
			continue
		}
		if i+1 >= len(body) || !isASCIILetter(body[i+1]) {
			i++
			continue
		}
		tag := scanOpenTag(body, i)
		removals = append(removals, tag.integrity...)
		switch {
		case tag.name == "head" && !headSeen:
			insertAt, headSeen = tag.end, true
		case tag.name == "script" || tag.name == "style":
			tag.end = indexOfClosingTag(body, tag.end, tag.name)
		}
		i = tag.end
	}
	return insertAt, removals
}

type openTag struct {
	name      string
	end       int // just after the closing >, or len(body) for a tag that never closes
	integrity []byteSpan
}

// scanOpenTag reads attributes the way an HTML tokenizer does, so a > inside a quoted value
// does not end the tag and the word integrity inside another attribute's value is not an
// attribute. A tag that never closes is left alone: nothing in it is removed.
func scanOpenTag(body []byte, start int) openTag {
	n := len(body)
	j := start + 1
	for j < n && !isTagSpace(body[j]) && body[j] != '/' && body[j] != '>' {
		j++
	}
	tag := openTag{name: strings.ToLower(string(body[start+1 : j]))}
	var found []byteSpan
	for {
		for j < n && (isTagSpace(body[j]) || body[j] == '/') {
			j++
		}
		if j >= n {
			tag.end = n
			return tag
		}
		if body[j] == '>' {
			tag.end, tag.integrity = j+1, found
			return tag
		}
		nameStart := j
		var closed bool
		j, closed = skipAttribute(body, j)
		if !closed {
			tag.end = n
			return tag
		}
		if bytes.EqualFold(attributeNameOf(body[nameStart:j]), []byte("integrity")) {
			found = append(found, byteSpan{precedingSpaceStart(body, nameStart), j})
		}
	}
}

// skipAttribute returns the index just after the attribute that starts at j, with its value
// if it has one. closed is false when a quoted value runs to the end of the body.
func skipAttribute(body []byte, j int) (int, bool) {
	n := len(body)
	j++
	for j < n && !isTagSpace(body[j]) && body[j] != '/' && body[j] != '=' && body[j] != '>' {
		j++
	}
	k := j
	for k < n && isTagSpace(body[k]) {
		k++
	}
	if k >= n || body[k] != '=' {
		return j, true
	}
	k++
	for k < n && isTagSpace(body[k]) {
		k++
	}
	if k < n && (body[k] == '"' || body[k] == '\'') {
		close := bytes.IndexByte(body[k+1:], body[k])
		if close < 0 {
			return n, false
		}
		return k + 1 + close + 1, true
	}
	for k < n && !isTagSpace(body[k]) && body[k] != '>' {
		k++
	}
	return k, true
}

// attributeNameOf cuts a skipped attribute down to its name.
func attributeNameOf(attribute []byte) []byte {
	end := 1
	for end < len(attribute) && !isTagSpace(attribute[end]) && attribute[end] != '=' && attribute[end] != '/' {
		end++
	}
	return attribute[:end]
}

func precedingSpaceStart(body []byte, at int) int {
	for at > 0 && isTagSpace(body[at-1]) {
		at--
	}
	return at
}

func endOfComment(body []byte, start int) int {
	end := bytes.Index(body[start+4:], []byte("-->"))
	if end < 0 {
		return len(body)
	}
	return start + 4 + end + 3
}

// indexOfClosingTag returns where </name begins, or the end of the body when the element
// never closes.
func indexOfClosingTag(body []byte, from int, name string) int {
	for i := from; i+2+len(name) <= len(body); i++ {
		if body[i] != '<' || body[i+1] != '/' || !strings.EqualFold(string(body[i+2:i+2+len(name)]), name) {
			continue
		}
		after := i + 2 + len(name)
		if after == len(body) || isTagSpace(body[after]) || body[after] == '/' || body[after] == '>' {
			return i
		}
	}
	return len(body)
}

func isTagSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
