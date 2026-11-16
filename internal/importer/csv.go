package importer

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// maxCSVBytes is the upload limit of the server: a bigger file is refused
// even if a caller forgot to limit the reader.
const maxCSVBytes = 20 << 20

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// ParseCSV reads a .csv file (SPEC §4.1.1). The encoding (UTF-8, with or
// without BOM, else Windows-1252) and the delimiter (; or ,) are detected.
// The first non-empty line is the header; the layout is chosen from it like
// for an Excel file. Line numbers are the lines of the file.
func ParseCSV(r io.Reader) (*Result, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxCSVBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	if len(raw) > maxCSVBytes {
		return nil, ErrTooLarge
	}
	text, err := decodeText(raw)
	if err != nil {
		return nil, err
	}

	delim := detectDelimiter(text)
	if tooManyColumns(text, delim) {
		return nil, ErrTooManyColumns
	}
	cr := csv.NewReader(strings.NewReader(text))
	cr.Comma = delim
	cr.LazyQuotes = true
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true

	// Only the mapped columns of each record are kept (cells copied), and
	// blank records are dropped while reading: memory stays proportional to
	// the useful data.
	var (
		records []record
		kept    []int // indexes of the kept columns, once the header is read
		header  bool
	)
	for {
		cells, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnreadable, err)
		}
		if len(cells) > maxFields {
			return nil, ErrTooManyColumns
		}
		if isBlank(cells) {
			continue
		}
		// Line where the record starts, as a text editor shows it.
		line, _ := cr.FieldPos(0)

		if !header {
			header = true
			_, idx, err := mapColumns(cells)
			if err != nil {
				return nil, err
			}
			for _, i := range idx {
				kept = append(kept, i)
			}
			slices.Sort(kept)
			kept = slices.Compact(kept)
		} else if len(records) > maxRecords {
			// The header is records[0].
			return nil, ErrTooLarge
		}
		compact := make([]string, len(kept))
		for j, i := range kept {
			compact[j] = strings.Clone(cell(cells, i))
		}
		records = append(records, record{Line: line, Cells: compact})
	}
	return parseRecords(records)
}

// tooManyColumns reports whether a line holds more than maxFields
// delimiters. It runs before the CSV reader, which would otherwise build
// millions of fields for a crafted one-line file.
func tooManyColumns(text string, delim rune) bool {
	d := byte(delim)
	for len(text) > 0 {
		line, rest, _ := strings.Cut(text, "\n")
		if strings.Count(line, string(d)) > maxFields {
			return true
		}
		text = rest
	}
	return false
}

// decodeText returns the text of a CSV file: UTF-8 as is (BOM removed),
// otherwise Windows-1252, the encoding of the families software export.
// A NUL byte means UTF-16 or a binary file: not supported.
func decodeText(raw []byte) (string, error) {
	raw = bytes.TrimPrefix(raw, utf8BOM)
	if bytes.IndexByte(raw, 0) >= 0 {
		return "", fmt.Errorf("%w: binary or UTF-16 content", ErrUnreadable)
	}
	if utf8.Valid(raw) {
		return string(raw), nil
	}
	out, err := charmap.Windows1252.NewDecoder().Bytes(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	return string(out), nil
}

// detectDelimiter returns ';' or ',', whichever occurs more on the first
// non-empty line (';' on a tie): the export uses ';', English Excel ','.
func detectDelimiter(text string) rune {
	for l := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if strings.Count(l, ",") > strings.Count(l, ";") {
			return ','
		}
		break
	}
	return ';'
}
