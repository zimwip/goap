// Package content tells what the bytes of a file are (ADR 0171): text or not, judged by the bytes themselves rather
// than by the name, the encoding of a text, its media type and the language to show it in. A text is decoded to UTF-8
// (Decode); anything else is a blob, kept elsewhere and named by its address. A leaf package: the git importer reads
// the blobs of a repository through it, the resolvers of the sources the files they open.
package content

import (
	"bytes"
	"net/http"
	"path"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Encodings of a text, as Info.Encoding names them.
const (
	UTF8        = "utf-8"
	UTF16LE     = "utf-16le"
	UTF16BE     = "utf-16be"
	UTF32LE     = "utf-32le"
	UTF32BE     = "utf-32be"
	Windows1252 = "windows-1252"
)

// Octets is the media type of bytes nothing tells more of.
const Octets = "application/octet-stream"

// Info is what the bytes of a file are.
type Info struct {
	// Text is true when the bytes read as text in Encoding.
	Text bool `json:"text"`
	// Encoding is the encoding of a text (UTF8...), empty for a blob.
	Encoding string `json:"encoding,omitempty"`
	// BOM is the length of the byte order mark the text starts with (left out by Decode).
	BOM int `json:"-"`
	// Media is the media type (text/x-go, image/png, application/octet-stream...), with no parameter.
	Media string `json:"media"`
	// Language is the language to show a text in (go, python, markdown...; the names of contracts/codetext), empty
	// for plain text and blobs.
	Language string `json:"language,omitempty"`
}

// SniffBytes is how much of a file Sniff needs to judge it (the rest of it is not read).
const SniffBytes = 8 << 10

// Sniff tells what the bytes of a file named name are, from the first SniffBytes of them. A byte order mark gives the
// encoding; else a NUL byte or a signature of a binary format (an image, an archive, a PDF, a font...) makes a blob;
// else valid UTF-8 with no more than one control character per thousand bytes (tabs, line ends, form feeds and escapes
// being text) is text, and so is Windows-1252 with none. A file cut in the middle of a UTF-8 sequence at SniffBytes is
// judged on what is whole.
func Sniff(name string, data []byte) Info {
	head := data
	if len(head) > SniffBytes {
		head = head[:SniffBytes]
	}
	info := Info{Media: MediaOf(name)}
	if enc, bom := bomOf(head); enc != "" {
		info.Text, info.Encoding, info.BOM = true, enc, bom
	} else if !binarySignature(head) && bytes.IndexByte(head, 0) < 0 {
		switch {
		case utf8.Valid(trimCut(head, len(data) > len(head))) && controls(head) <= len(head)/1000:
			info.Text, info.Encoding = true, UTF8
		case controls(head) == 0 && windows1252(head):
			info.Text, info.Encoding = true, Windows1252
		}
	}
	if !info.Text {
		if m := sniffed(head); m != "" {
			info.Media = m
		} else if info.Media == "" || strings.HasPrefix(info.Media, "text/") {
			info.Media = Octets
		}
		return info
	}
	info.Language = LanguageOf(name)
	if info.Media == "" || !textual(info.Media) {
		info.Media = "text/plain"
	}
	return info
}

// Decode gives the text of bytes Sniff judged text, in UTF-8 (the byte order mark left out). Bytes that are not text
// give "", false.
func Decode(data []byte, info Info) (string, bool) {
	if !info.Text {
		return "", false
	}
	data = data[min(info.BOM, len(data)):]
	switch info.Encoding {
	case UTF8:
		return strings.ToValidUTF8(string(data), "�"), true
	case UTF16LE, UTF16BE:
		u := make([]uint16, 0, len(data)/2)
		for i := 0; i+1 < len(data); i += 2 {
			if info.Encoding == UTF16LE {
				u = append(u, uint16(data[i])|uint16(data[i+1])<<8)
			} else {
				u = append(u, uint16(data[i])<<8|uint16(data[i+1]))
			}
		}
		return string(utf16.Decode(u)), true
	case UTF32LE, UTF32BE:
		var b strings.Builder
		for i := 0; i+3 < len(data); i += 4 {
			var r rune
			if info.Encoding == UTF32LE {
				r = rune(data[i]) | rune(data[i+1])<<8 | rune(data[i+2])<<16 | rune(data[i+3])<<24
			} else {
				r = rune(data[i])<<24 | rune(data[i+1])<<16 | rune(data[i+2])<<8 | rune(data[i+3])
			}
			if !utf8.ValidRune(r) {
				r = utf8.RuneError
			}
			b.WriteRune(r)
		}
		return b.String(), true
	case Windows1252:
		var b strings.Builder
		b.Grow(len(data))
		for _, c := range data {
			if c >= 0x80 && c < 0xa0 {
				b.WriteRune(cp1252[c-0x80])
			} else {
				b.WriteRune(rune(c))
			}
		}
		return b.String(), true
	}
	return "", false
}

// bomOf is the encoding a byte order mark gives, and its length.
func bomOf(b []byte) (string, int) {
	switch {
	case bytes.HasPrefix(b, []byte{0xef, 0xbb, 0xbf}):
		return UTF8, 3
	case bytes.HasPrefix(b, []byte{0xff, 0xfe, 0, 0}):
		return UTF32LE, 4
	case bytes.HasPrefix(b, []byte{0, 0, 0xfe, 0xff}):
		return UTF32BE, 4
	case bytes.HasPrefix(b, []byte{0xff, 0xfe}):
		return UTF16LE, 2
	case bytes.HasPrefix(b, []byte{0xfe, 0xff}):
		return UTF16BE, 2
	}
	return "", 0
}

// trimCut drops an incomplete UTF-8 sequence at the end of a head cut out of a longer file.
func trimCut(b []byte, cut bool) []byte {
	if !cut {
		return b
	}
	for i := 1; i <= utf8.UTFMax && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i]
			}
			break
		}
	}
	return b
}

// controls counts the control characters that are not text: tabs, line ends, form feeds and escapes (ANSI colours)
// are.
func controls(b []byte) int {
	n := 0
	for _, c := range b {
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' && c != '\f' && c != '\v' && c != 0x1b || c == 0x7f {
			n++
		}
	}
	return n
}

// windows1252 reports bytes that hold none of the five bytes Windows-1252 leaves undefined.
func windows1252(b []byte) bool {
	for _, c := range b {
		if c == 0x81 || c == 0x8d || c == 0x8f || c == 0x90 || c == 0x9d {
			return false
		}
	}
	return true
}

// cp1252 is what Windows-1252 makes of the bytes 0x80 to 0x9f (the others are those of ISO-8859-1).
var cp1252 = [32]rune{
	'€', '�', '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', '�', 'Ž', '�',
	'�', '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', '�', 'ž', 'Ÿ',
}

// sniffed is the media type the signature of a binary format gives (http.DetectContentType), "" when none does.
func sniffed(b []byte) string {
	m, _, _ := strings.Cut(http.DetectContentType(b), ";")
	if m == Octets || strings.HasPrefix(m, "text/") {
		return ""
	}
	return m
}

// binarySignature reports bytes that start like a binary format.
func binarySignature(b []byte) bool { return sniffed(b) != "" }

// textual reports a media type whose content is text.
func textual(m string) bool {
	if strings.HasPrefix(m, "text/") || strings.HasSuffix(m, "+xml") || strings.HasSuffix(m, "+json") {
		return true
	}
	switch m {
	case "application/json", "application/xml", "application/javascript", "application/x-sh", "application/toml",
		"application/yaml", "application/sql":
		return true
	}
	return false
}

// MediaOf is the media type the name of a file says, "" when it says none: a fixed table, the same on every machine
// (not the system's).
func MediaOf(name string) string {
	base := path.Base(name)
	if m, ok := mediaByName[strings.ToLower(base)]; ok {
		return m
	}
	return mediaByExt[strings.ToLower(strings.TrimPrefix(path.Ext(base), "."))]
}

// LanguageOf is the language to show a text named name in (the names contracts/codetext and the web's editor know),
// "" for plain text.
func LanguageOf(name string) string {
	base := path.Base(name)
	if l, ok := languageByName[strings.ToLower(base)]; ok {
		return l
	}
	return languageByExt[strings.ToLower(strings.TrimPrefix(path.Ext(base), "."))]
}

// IsImage reports a media type a browser shows as an image.
func IsImage(m string) bool {
	switch m {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp", "image/svg+xml", "image/x-icon", "image/vnd.microsoft.icon":
		return true
	}
	return false
}

// Inline reports a media type a browser shows by itself (an image, a PDF, a video or a sound it plays).
func Inline(m string) bool {
	return IsImage(m) || m == "application/pdf" || strings.HasPrefix(m, "video/") || strings.HasPrefix(m, "audio/")
}

var languageByName = map[string]string{
	"makefile": "makefile", "gnumakefile": "makefile", "dockerfile": "dockerfile", "containerfile": "dockerfile",
	"cmakelists.txt": "cmake", "go.mod": "gomod", "go.sum": "text", "jenkinsfile": "groovy", "vagrantfile": "ruby",
	"gemfile": "ruby", "rakefile": "ruby", ".bashrc": "bash", ".profile": "bash", ".zshrc": "bash",
}

var languageByExt = map[string]string{
	"c": "c", "h": "c",
	"cc": "cpp", "cpp": "cpp", "cxx": "cpp", "hh": "cpp", "hpp": "cpp", "hxx": "cpp",
	"cs": "csharp", "go": "go", "java": "java", "kt": "kotlin", "kts": "kotlin", "scala": "scala", "groovy": "groovy",
	"gradle": "groovy", "py": "python", "pyi": "python", "rb": "ruby", "php": "php", "pl": "perl", "pm": "perl",
	"lua": "lua", "r": "r", "swift": "swift", "m": "objectivec", "mm": "objectivec", "dart": "dart", "zig": "zig",
	"js": "javascript", "mjs": "javascript", "cjs": "javascript", "jsx": "jsx", "ts": "typescript", "mts": "typescript",
	"cts": "typescript", "tsx": "tsx", "svelte": "svelte", "vue": "vue", "rs": "rust", "hs": "haskell", "ml": "ocaml",
	"ex": "elixir", "exs": "elixir", "erl": "erlang", "clj": "clojure", "fs": "fsharp", "sql": "sql",
	"sh": "bash", "bash": "bash", "zsh": "bash", "fish": "fish", "ps1": "powershell", "psm1": "powershell",
	"bat": "batch", "cmd": "batch", "mk": "makefile", "cmake": "cmake",
	"html": "html", "htm": "html", "xml": "xml", "xsd": "xml", "xsl": "xml", "svg": "xml", "css": "css", "scss": "scss",
	"less": "less", "json": "json", "jsonc": "json", "yaml": "yaml", "yml": "yaml", "toml": "toml", "ini": "ini",
	"cfg": "ini", "conf": "ini", "properties": "properties", "md": "markdown", "markdown": "markdown", "rst": "rst",
	"adoc": "asciidoc", "tex": "latex", "proto": "protobuf", "graphql": "graphql", "gql": "graphql", "tf": "hcl",
	"hcl": "hcl", "cypher": "cypher", "cel": "cel", "diff": "diff", "patch": "diff", "csv": "csv", "tsv": "csv",
	"g4": "antlr", "dockerfile": "dockerfile",
}

var mediaByName = map[string]string{
	"makefile": "text/x-makefile", "dockerfile": "text/x-dockerfile", "readme": "text/plain", "license": "text/plain",
}

var mediaByExt = map[string]string{
	// texts
	"txt": "text/plain", "md": "text/markdown", "markdown": "text/markdown", "html": "text/html", "htm": "text/html",
	"css": "text/css", "csv": "text/csv", "tsv": "text/tab-separated-values", "js": "text/javascript",
	"mjs": "text/javascript", "cjs": "text/javascript", "json": "application/json", "xml": "application/xml",
	"yaml": "application/yaml", "yml": "application/yaml", "toml": "application/toml", "sql": "application/sql",
	"sh": "application/x-sh", "go": "text/x-go", "c": "text/x-c", "h": "text/x-c", "cpp": "text/x-c++",
	"cc": "text/x-c++", "hpp": "text/x-c++", "java": "text/x-java", "py": "text/x-python", "rs": "text/x-rust",
	"ts": "text/x-typescript", "tsx": "text/x-typescript", "rb": "text/x-ruby", "svg": "image/svg+xml",
	// blobs
	"png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp",
	"avif": "image/avif", "bmp": "image/bmp", "ico": "image/x-icon", "tif": "image/tiff", "tiff": "image/tiff",
	"pdf": "application/pdf", "zip": "application/zip", "gz": "application/gzip", "tgz": "application/gzip",
	"bz2": "application/x-bzip2", "xz": "application/x-xz", "7z": "application/x-7z-compressed",
	"tar": "application/x-tar", "jar": "application/java-archive", "wasm": "application/wasm",
	"woff": "font/woff", "woff2": "font/woff2", "ttf": "font/ttf", "otf": "font/otf",
	"mp3": "audio/mpeg", "wav": "audio/wav", "ogg": "audio/ogg", "flac": "audio/flac",
	"mp4": "video/mp4", "webm": "video/webm", "mov": "video/quicktime",
	"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"exe":  "application/vnd.microsoft.portable-executable", "so": "application/x-sharedlib", "o": "application/x-object",
}
